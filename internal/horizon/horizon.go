// Package horizon is the Horizon-backed ingestion adapter. It reads operations ledger by ledger over plain HTTP.
package horizon

import (
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

	"github.com/Anasabubakar/eventparity-engine/internal/model"
	"github.com/Anasabubakar/eventparity-engine/internal/source"
)

// ErrNotFound means Horizon has no record of the ledger (outside its history), which is a coverage gap.
var ErrNotFound = errors.New("horizon: ledger not found")

// Client implements source.Source.
type Client struct {
	Base       string
	HTTP       *http.Client
	MaxRetries int           // retries after the first attempt; default 4
	Backoff    time.Duration // initial backoff, doubled per retry; default 500ms
	MaxBackoff time.Duration // default 8s
	PageLimit  int           // default 200 (Horizon's maximum)
	UserAgent  string
	sleep      func(context.Context, time.Duration) error
}

// New returns a client for base (an https URL, or http for a local test server).
func New(base string) *Client {
	return &Client{Base: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) Adapter() string { return "horizon" }

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

// get fetches one URL with bounded, context-aware retries on transport errors, 429 and 5xx.
func (c *Client) get(ctx context.Context, u string) ([]byte, error) {
	c.defaults()
	backoff := c.Backoff
	var last error
	for attempt := 0; attempt <= c.MaxRetries; attempt++ {
		if attempt > 0 {
			if err := c.sleep(ctx, backoff); err != nil {
				return nil, err
			}
			backoff = min(backoff*2, c.MaxBackoff)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", c.UserAgent)
		resp, err := c.HTTP.Do(req)
		if err != nil {
			last = err
			continue
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		switch {
		case rerr != nil:
			last = rerr
		case resp.StatusCode == http.StatusOK:
			return body, nil
		case resp.StatusCode == http.StatusNotFound:
			return nil, ErrNotFound
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			last = fmt.Errorf("horizon: HTTP %d", resp.StatusCode)
			if ra, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && ra > 0 {
				backoff = min(time.Duration(ra)*time.Second, c.MaxBackoff)
			}
		default:
			return nil, fmt.Errorf("horizon: HTTP %d for %s", resp.StatusCode, redactQuery(u))
		}
	}
	return nil, fmt.Errorf("horizon: giving up after %d attempts: %w", c.MaxRetries+1, last)
}

func redactQuery(u string) string {
	if i := strings.IndexByte(u, '?'); i >= 0 {
		return u[:i]
	}
	return u
}

// Bounds reads Horizon's root document for the history window and network passphrase.
func (c *Client) Bounds(ctx context.Context) (source.Bounds, error) {
	body, err := c.get(ctx, c.Base+"/")
	if err != nil {
		return source.Bounds{}, err
	}
	var root struct {
		Elder   uint32 `json:"history_elder_ledger"`
		Latest  uint32 `json:"history_latest_ledger"`
		Network string `json:"network_passphrase"`
	}
	if err := json.Unmarshal(body, &root); err != nil {
		return source.Bounds{}, fmt.Errorf("horizon: root document: %w", err)
	}
	if root.Latest == 0 {
		return source.Bounds{}, fmt.Errorf("horizon: root document has no history_latest_ledger")
	}
	return source.Bounds{Oldest: root.Elder, Latest: root.Latest, Network: root.Network}, nil
}

type opRecord struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	TxHash      string `json:"transaction_hash"`
	Successful  *bool  `json:"transaction_successful"`
	From        string `json:"from"`
	FromMuxedID string `json:"from_muxed_id"`
	To          string `json:"to"`
	ToMuxedID   string `json:"to_muxed_id"`
	AssetType   string `json:"asset_type"`
	AssetCode   string `json:"asset_code"`
	AssetIssuer string `json:"asset_issuer"`
	Amount      string `json:"amount"`
}

type page struct {
	Links struct {
		Next struct {
			Href string `json:"href"`
		} `json:"next"`
	} `json:"_links"`
	Embedded struct {
		Records []opRecord `json:"records"`
	} `json:"_embedded"`
}

// Ledger reads every successful operation in one ledger. Failed transactions are excluded by the request and again by the check below.
func (c *Client) Ledger(ctx context.Context, seq uint32) (source.LedgerData, error) {
	c.defaults()
	out := source.LedgerData{Ledger: seq, Unsupported: map[string]int{}}
	next := fmt.Sprintf("%s/ledgers/%d/operations?limit=%d&order=asc&include_failed=false", c.Base, seq, c.PageLimit)
	seen := map[string]bool{}
	for {
		body, err := c.get(ctx, next)
		if err != nil {
			return out, err
		}
		var pg page
		if err := json.Unmarshal(body, &pg); err != nil {
			return out, fmt.Errorf("horizon: ledger %d: %w", seq, err)
		}
		if len(pg.Embedded.Records) == 0 {
			return out, nil
		}
		for _, r := range pg.Embedded.Records {
			if seen[r.ID] {
				return out, fmt.Errorf("horizon: ledger %d: operation %s returned twice while paging", seq, r.ID)
			}
			seen[r.ID] = true
			if r.Successful != nil && !*r.Successful {
				continue
			}
			toid, err := strconv.ParseUint(r.ID, 10, 64)
			if err != nil {
				return out, fmt.Errorf("horizon: operation id %q: %w", r.ID, err)
			}
			if got := uint32(toid >> 32); got != seq {
				return out, fmt.Errorf("horizon: operation %s belongs to ledger %d, requested %d", r.ID, got, seq)
			}
			if r.Type != "payment" {
				out.Unsupported[r.Type]++
				continue
			}
			p, err := payment(r, toid)
			if err != nil {
				return out, fmt.Errorf("horizon: operation %s: %w", r.ID, err)
			}
			out.Payments = append(out.Payments, p)
		}
		if pg.Links.Next.Href == "" || pg.Links.Next.Href == next {
			return out, nil
		}
		next = pg.Links.Next.Href
	}
}

func payment(r opRecord, toid uint64) (model.Payment, error) {
	amt, err := model.CanonicalAmount(r.Amount)
	if err != nil {
		return model.Payment{}, err
	}
	p := model.Payment{
		Ledger:           uint32(toid >> 32),
		ApplicationOrder: uint32((toid >> 12) & 0xFFFFF),
		TxHash:           r.TxHash,
		OpIndex:          uint32(toid&0xFFF) - 1,
		From:             r.From,
		FromMuxedID:      r.FromMuxedID,
		To:               r.To,
		ToMuxedID:        r.ToMuxedID,
		Amount:           amt,
	}
	switch r.AssetType {
	case "native":
		p.Asset = model.Asset{Type: "native"}
	case "credit_alphanum4", "credit_alphanum12":
		p.Asset = model.Asset{Type: "credit", Code: r.AssetCode, Issuer: r.AssetIssuer}
	default:
		return model.Payment{}, fmt.Errorf("unknown asset type %q", r.AssetType)
	}
	if err := p.Validate(); err != nil {
		return model.Payment{}, err
	}
	return p, nil
}

// Stream emits every ledger in [from, to] in order.
func (c *Client) Stream(ctx context.Context, from, to uint32, emit func(source.LedgerData) error) error {
	for l := from; l <= to; l++ {
		d, err := c.Ledger(ctx, l)
		if err != nil {
			return fmt.Errorf("ledger %d: %w", l, err)
		}
		if err := emit(d); err != nil {
			return err
		}
	}
	return nil
}
