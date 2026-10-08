// Package rpcsrc is the Stellar RPC-backed ingestion adapter. RPC does not serve classic payments as events, so this
// adapter reads getTransactions and decodes each transaction envelope's operations from XDR.
package rpcsrc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/Anasabubakar/eventparity-engine/internal/model"
	"github.com/Anasabubakar/eventparity-engine/internal/source"
)

// Client implements source.Source over JSON-RPC.
type Client struct {
	Base       string
	HTTP       *http.Client
	MaxRetries int
	Backoff    time.Duration
	MaxBackoff time.Duration
	PageLimit  int
	UserAgent  string
	sleep      func(context.Context, time.Duration) error
}

// New returns a client for an RPC endpoint.
func New(base string) *Client {
	return &Client{Base: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: 60 * time.Second}}
}

func (c *Client) Adapter() string { return "rpc" }

func (c *Client) Provider() string {
	u, err := url.Parse(c.Base)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func (c *Client) defaults() {
	if c.MaxRetries == 0 {
		c.MaxRetries = 4
	}
	if c.Backoff == 0 {
		c.Backoff = 500 * time.Millisecond
	}
	if c.MaxBackoff == 0 {
		c.MaxBackoff = 8 * time.Second
	}
	if c.PageLimit == 0 {
		c.PageLimit = 200
	}
	if c.sleep == nil {
		c.sleep = func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		}
	}
	if c.UserAgent == "" {
		c.UserAgent = "eventparity-engine"
	}
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

// ErrOutOfRetention reports that RPC refused a start ledger outside the window it serves.
var ErrOutOfRetention = fmt.Errorf("rpc: start ledger is outside the provider's retention window: %w", source.ErrHistoryUnavailable)

func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	c.defaults()
	reqBody, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		return err
	}
	backoff := c.Backoff
	var last error
	for attempt := 0; attempt <= c.MaxRetries; attempt++ {
		if attempt > 0 {
			if err := c.sleep(ctx, backoff); err != nil {
				return err
			}
			backoff = min(backoff*2, c.MaxBackoff)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base, bytes.NewReader(reqBody))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", c.UserAgent)
		resp, err := c.HTTP.Do(req)
		if err != nil {
			last = err
			continue
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if rerr != nil {
			last = rerr
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			last = fmt.Errorf("rpc: HTTP %d", resp.StatusCode)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("rpc: HTTP %d", resp.StatusCode)
		}
		var env struct {
			Result json.RawMessage `json:"result"`
			Error  *rpcError       `json:"error"`
		}
		if err := json.Unmarshal(body, &env); err != nil {
			return fmt.Errorf("rpc: %s: %w", method, err)
		}
		if env.Error != nil {
			return env.Error
		}
		return json.Unmarshal(env.Result, out)
	}
	return fmt.Errorf("rpc: giving up after %d attempts: %w", c.MaxRetries+1, last)
}

// Bounds reads getHealth for the retention window and getNetwork for the passphrase.
func (c *Client) Bounds(ctx context.Context) (source.Bounds, error) {
	var h struct {
		Status       string `json:"status"`
		LatestLedger uint32 `json:"latestLedger"`
		OldestLedger uint32 `json:"oldestLedger"`
	}
	if err := c.call(ctx, "getHealth", map[string]any{}, &h); err != nil {
		return source.Bounds{}, err
	}
	if h.LatestLedger == 0 {
		return source.Bounds{}, fmt.Errorf("rpc: getHealth reported no latest ledger (status %q)", h.Status)
	}
	var n struct {
		Passphrase string `json:"passphrase"`
	}
	if err := c.call(ctx, "getNetwork", map[string]any{}, &n); err != nil {
		return source.Bounds{}, err
	}
	return source.Bounds{Oldest: h.OldestLedger, Latest: h.LatestLedger, Network: n.Passphrase}, nil
}

type rpcTx struct {
	Status           string `json:"status"`
	TxHash           string `json:"txHash"`
	ApplicationOrder uint32 `json:"applicationOrder"`
	Ledger           uint32 `json:"ledger"`
	EnvelopeXdr      string `json:"envelopeXdr"`
}

// txPage keeps a missing `transactions` key distinguishable from an empty list.
type txPage struct {
	Transactions *[]rpcTx `json:"transactions"`
	Cursor       string   `json:"cursor"`
	LatestLedger uint32   `json:"latestLedger"`
	OldestLedger uint32   `json:"oldestLedger"`
}

// maxRPCPages bounds paging so a provider that never ends the listing cannot loop forever.
const maxRPCPages = 100_000

// Stream emits every ledger in [from, to] in order. getTransactions returns transactions in ledger order starting at
// startLedger; ledgers without transactions are emitted empty.
func (c *Client) Stream(ctx context.Context, from, to uint32, emit func(source.LedgerData) error) error {
	c.defaults()
	next := from // next ledger to emit
	cur := source.LedgerData{Ledger: from, Unsupported: map[string]int{}}
	flushThrough := func(upTo uint32) error { // emit all ledgers < upTo
		for next < upTo {
			if cur.Ledger != next {
				cur = source.LedgerData{Ledger: next, Unsupported: map[string]int{}}
			}
			if err := emit(cur); err != nil {
				return err
			}
			next++
			cur = source.LedgerData{Ledger: next, Unsupported: map[string]int{}}
		}
		return nil
	}

	cursor := ""
	for pages := 0; ; pages++ {
		if pages >= maxRPCPages {
			return fmt.Errorf("rpc: more than %d pages; refusing to continue", maxRPCPages)
		}
		params := map[string]any{"pagination": map[string]any{"limit": c.PageLimit}}
		if cursor == "" {
			params["startLedger"] = from
		} else {
			params["pagination"] = map[string]any{"limit": c.PageLimit, "cursor": cursor}
		}
		var pg txPage
		if err := c.call(ctx, "getTransactions", params, &pg); err != nil {
			var re *rpcError
			if errors.As(err, &re) && strings.Contains(strings.ToLower(re.Message), "ledger") && cursor == "" {
				return fmt.Errorf("%w: %v", ErrOutOfRetention, err)
			}
			return err
		}
		if pg.Transactions == nil {
			return fmt.Errorf("rpc: getTransactions response has no `transactions` list; refusing to treat it as an empty range")
		}
		txs := *pg.Transactions
		if len(txs) == 0 {
			// An empty page ends the listing only if the provider says it has reached the end of the range.
			if pg.LatestLedger < to {
				return fmt.Errorf("rpc: empty page but the provider's latest ledger %d is behind the requested end %d; the remaining ledgers are not known to be empty", pg.LatestLedger, to)
			}
			break
		}
		done := false
		for _, tx := range txs {
			if tx.Ledger > to {
				done = true
				break
			}
			if tx.Ledger < next {
				return fmt.Errorf("rpc: transaction %s in ledger %d arrived after ledger %d was complete (provider returned unordered data)", tx.TxHash, tx.Ledger, next)
			}
			if err := flushThrough(tx.Ledger); err != nil {
				return err
			}
			// Only the two documented outcomes are understood. A missing or unknown status must not be
			// mistaken for a failed transaction: that would drop a payment while coverage still looks complete.
			switch tx.Status {
			case "SUCCESS":
			case "FAILED":
				continue
			default:
				return fmt.Errorf("rpc: transaction %q in ledger %d has status %q, expected SUCCESS or FAILED", tx.TxHash, tx.Ledger, tx.Status)
			}
			if tx.TxHash == "" || tx.EnvelopeXdr == "" {
				return fmt.Errorf("rpc: a successful transaction in ledger %d is missing its hash or envelope", tx.Ledger)
			}
			if err := decodeTx(tx, &cur); err != nil {
				return err
			}
		}
		if done {
			break
		}
		if pg.Cursor == "" {
			return fmt.Errorf("rpc: a page of transactions came back with no cursor, so completeness cannot be established")
		}
		if pg.Cursor == cursor {
			return fmt.Errorf("rpc: the cursor repeated; paging made no progress")
		}
		cursor = pg.Cursor
	}
	return flushThrough(to + 1)
}

func decodeTx(tx rpcTx, into *source.LedgerData) error {
	var env xdr.TransactionEnvelope
	if err := xdr.SafeUnmarshalBase64(tx.EnvelopeXdr, &env); err != nil {
		return fmt.Errorf("rpc: transaction %s: envelope XDR: %w", tx.TxHash, err)
	}
	txSource := env.SourceAccount()
	for i, op := range env.Operations() {
		name := source.OpTypeName(op.Body.Type.String())
		if op.Body.Type != xdr.OperationTypePayment {
			into.Unsupported[name]++
			continue
		}
		pay := op.Body.MustPaymentOp()
		src := txSource
		if op.SourceAccount != nil {
			src = *op.SourceAccount
		}
		p := model.Payment{
			Ledger:           tx.Ledger,
			ApplicationOrder: tx.ApplicationOrder,
			TxHash:           tx.TxHash,
			OpIndex:          uint32(i),
		}
		var err error
		if p.From, p.FromMuxedID, err = account(src); err != nil {
			return fmt.Errorf("rpc: transaction %s op %d source: %w", tx.TxHash, i, err)
		}
		if p.To, p.ToMuxedID, err = account(pay.Destination); err != nil {
			return fmt.Errorf("rpc: transaction %s op %d destination: %w", tx.TxHash, i, err)
		}
		if p.Asset, err = asset(pay.Asset); err != nil {
			return fmt.Errorf("rpc: transaction %s op %d: %w", tx.TxHash, i, err)
		}
		p.Amount = model.FormatAmount(int64(pay.Amount))
		if err := p.Validate(); err != nil {
			return fmt.Errorf("rpc: transaction %s op %d: %w", tx.TxHash, i, err)
		}
		into.Payments = append(into.Payments, p)
	}
	return nil
}

func account(m xdr.MuxedAccount) (g, muxID string, err error) {
	id := m.ToAccountId()
	g, err = id.GetAddress()
	if err != nil {
		return "", "", err
	}
	if m.Type == xdr.CryptoKeyTypeKeyTypeMuxedEd25519 {
		muxID = strconv.FormatUint(uint64(m.Med25519.Id), 10)
	}
	return g, muxID, nil
}

func asset(a xdr.Asset) (model.Asset, error) {
	switch a.Type {
	case xdr.AssetTypeAssetTypeNative:
		return model.Asset{Type: "native"}, nil
	case xdr.AssetTypeAssetTypeCreditAlphanum4, xdr.AssetTypeAssetTypeCreditAlphanum12:
		var typ, code, issuer string
		if err := a.Extract(&typ, &code, &issuer); err != nil {
			return model.Asset{}, err
		}
		return model.Asset{Type: "credit", Code: code, Issuer: issuer}, nil
	}
	return model.Asset{}, fmt.Errorf("unsupported asset type %v", a.Type)
}
