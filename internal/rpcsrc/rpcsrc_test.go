package rpcsrc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/network"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/txnbuild"

	"github.com/Anasabubakar/eventparity-engine/internal/source"
)

var (
	kpA, kpB, kpI = keypair.MustRandom(), keypair.MustRandom(), keypair.MustRandom()
	nat           = txnbuild.NativeAsset{}
	usdx          = txnbuild.CreditAsset{Code: "USDX", Issuer: kpI.Address()}
)

func envelope(t *testing.T, src *keypair.Full, ops ...txnbuild.Operation) string {
	t.Helper()
	acct := txnbuild.NewSimpleAccount(src.Address(), 1)
	tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{SourceAccount: &acct, IncrementSequenceNum: true, Operations: ops,
		BaseFee: txnbuild.MinBaseFee, Preconditions: txnbuild.Preconditions{TimeBounds: txnbuild.NewInfiniteTimeout()}})
	if err != nil {
		t.Fatal(err)
	}
	tx, err = tx.Sign(network.TestNetworkPassphrase, src)
	if err != nil {
		t.Fatal(err)
	}
	b64, err := tx.Base64()
	if err != nil {
		t.Fatal(err)
	}
	return b64
}

func feeBump(t *testing.T, src *keypair.Full, fee *keypair.Full, ops ...txnbuild.Operation) string {
	t.Helper()
	acct := txnbuild.NewSimpleAccount(src.Address(), 1)
	inner, err := txnbuild.NewTransaction(txnbuild.TransactionParams{SourceAccount: &acct, IncrementSequenceNum: true, Operations: ops,
		BaseFee: txnbuild.MinBaseFee, Preconditions: txnbuild.Preconditions{TimeBounds: txnbuild.NewInfiniteTimeout()}})
	if err != nil {
		t.Fatal(err)
	}
	inner, _ = inner.Sign(network.TestNetworkPassphrase, src)
	fb, err := txnbuild.NewFeeBumpTransaction(txnbuild.FeeBumpTransactionParams{Inner: inner, FeeAccount: fee.Address(), BaseFee: txnbuild.MinBaseFee * 10})
	if err != nil {
		t.Fatal(err)
	}
	fb, _ = fb.Sign(network.TestNetworkPassphrase, fee)
	b64, _ := fb.Base64()
	return b64
}

func hashN(n int) string { return fmt.Sprintf("%064x", n) }

type txRec struct {
	Status           string `json:"status"`
	TxHash           string `json:"txHash"`
	ApplicationOrder uint32 `json:"applicationOrder"`
	Ledger           uint32 `json:"ledger"`
	EnvelopeXdr      string `json:"envelopeXdr"`
}

type fakeRPC struct {
	txs        []txRec
	pageSize   int
	oldest     uint32
	latest     uint32
	startError string
	calls      atomic.Int32
}

// serve implements the JSON-RPC methods the adapter uses, paging by index through txs.
func (f *fakeRPC) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		var req struct {
			Method string `json:"method"`
			Params struct {
				StartLedger uint32 `json:"startLedger"`
				Pagination  struct {
					Limit  int    `json:"limit"`
					Cursor string `json:"cursor"`
				} `json:"pagination"`
			} `json:"params"`
		}
		b, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(b, &req); err != nil {
			t.Error(err)
		}
		reply := func(v any) { json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": v}) }
		switch req.Method {
		case "getHealth":
			reply(map[string]any{"status": "healthy", "latestLedger": f.latest, "oldestLedger": f.oldest})
		case "getNetwork":
			reply(map[string]any{"passphrase": network.TestNetworkPassphrase, "protocolVersion": 25})
		case "getTransactions":
			if f.startError != "" && req.Params.Pagination.Cursor == "" {
				json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "error": map[string]any{"code": -32602, "message": f.startError}})
				return
			}
			start := 0
			if c := req.Params.Pagination.Cursor; c != "" {
				fmt.Sscan(c, &start)
			} else {
				for start < len(f.txs) && f.txs[start].Ledger < req.Params.StartLedger {
					start++
				}
			}
			end := min(start+f.pageSize, len(f.txs))
			reply(map[string]any{"transactions": f.txs[start:end], "cursor": fmt.Sprint(end), "latestLedger": f.latest, "oldestLedger": f.oldest})
		default:
			http.NotFound(w, r)
		}
	}
}

func client(t *testing.T, f *fakeRPC) *Client {
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	c := New(srv.URL)
	c.sleep = func(context.Context, time.Duration) error { return nil }
	return c
}

func collect(t *testing.T, c *Client, from, to uint32) map[uint32]source.LedgerData {
	t.Helper()
	got := map[uint32]source.LedgerData{}
	var order []uint32
	err := c.Stream(context.Background(), from, to, func(d source.LedgerData) error { got[d.Ledger] = d; order = append(order, d.Ledger); return nil })
	if err != nil {
		t.Fatal(err)
	}
	for i, l := range order {
		if l != from+uint32(i) {
			t.Fatalf("ledgers must be emitted in order exactly once: %v", order)
		}
	}
	if len(order) != int(to-from+1) {
		t.Fatalf("every ledger must be emitted, got %v", order)
	}
	return got
}

func pay(dst string, a txnbuild.Asset, amt string) *txnbuild.Payment {
	return &txnbuild.Payment{Destination: dst, Asset: a, Amount: amt}
}

func TestDecodesPaymentsAndCountsUnsupportedOperationsPerLedger(t *testing.T) {
	f := &fakeRPC{pageSize: 100, oldest: 1, latest: 100, txs: []txRec{
		{Status: "SUCCESS", TxHash: hashN(1), ApplicationOrder: 1, Ledger: 12,
			EnvelopeXdr: envelope(t, kpA, pay(kpB.Address(), nat, "12.3456789"), &txnbuild.ManageData{Name: "k", Value: []byte("v")}, pay(kpB.Address(), usdx, "0.0000001"))},
	}}
	got := collect(t, client(t, f), 10, 14)
	d := got[12]
	if len(d.Payments) != 2 || d.Unsupported["manage_data"] != 1 {
		t.Fatalf("%+v", d)
	}
	p0, p1 := d.Payments[0], d.Payments[1]
	if p0.OpIndex != 0 || p0.Amount != "12.3456789" || p0.Asset.Type != "native" || p0.From != kpA.Address() || p0.To != kpB.Address() || p0.ApplicationOrder != 1 {
		t.Fatalf("%+v", p0)
	}
	if p1.OpIndex != 2 || p1.Amount != "0.0000001" || p1.Asset.Code != "USDX" || p1.Asset.Issuer != kpI.Address() {
		t.Fatalf("op index must count unsupported operations too: %+v", p1)
	}
	for _, l := range []uint32{10, 11, 13, 14} {
		if len(got[l].Payments) != 0 {
			t.Errorf("ledger %d should be empty", l)
		}
	}
}

func TestFailedTransactionsAreExcluded(t *testing.T) {
	f := &fakeRPC{pageSize: 100, oldest: 1, latest: 100, txs: []txRec{
		{Status: "FAILED", TxHash: hashN(1), ApplicationOrder: 1, Ledger: 10, EnvelopeXdr: envelope(t, kpA, pay(kpB.Address(), nat, "1.0000000"))},
		{Status: "SUCCESS", TxHash: hashN(2), ApplicationOrder: 2, Ledger: 10, EnvelopeXdr: envelope(t, kpA, pay(kpB.Address(), nat, "2.0000000"))},
	}}
	d := collect(t, client(t, f), 10, 10)[10]
	if len(d.Payments) != 1 || d.Payments[0].Amount != "2.0000000" {
		t.Fatalf("%+v", d)
	}
}

func TestFeeBumpUsesTheInnerTransactionSource(t *testing.T) {
	f := &fakeRPC{pageSize: 100, oldest: 1, latest: 100, txs: []txRec{
		{Status: "SUCCESS", TxHash: hashN(1), ApplicationOrder: 1, Ledger: 10, EnvelopeXdr: feeBump(t, kpA, kpB, pay(kpB.Address(), nat, "0.2500000"))},
	}}
	d := collect(t, client(t, f), 10, 10)[10]
	if len(d.Payments) != 1 || d.Payments[0].From != kpA.Address() {
		t.Fatalf("the payer is the inner source, not the fee account: %+v", d.Payments)
	}
}

func TestMuxedDestinationAndOperationSourceOverride(t *testing.T) {
	var m strkey.MuxedAccount
	if err := m.SetAccountID(kpB.Address()); err != nil {
		t.Fatal(err)
	}
	m.SetID(42)
	addr, _ := m.Address()
	op := &txnbuild.Payment{SourceAccount: kpI.Address(), Destination: addr, Asset: nat, Amount: "1.0000000"}
	f := &fakeRPC{pageSize: 100, oldest: 1, latest: 100, txs: []txRec{{Status: "SUCCESS", TxHash: hashN(1), ApplicationOrder: 1, Ledger: 10, EnvelopeXdr: envelope(t, kpA, op)}}}
	p := collect(t, client(t, f), 10, 10)[10].Payments[0]
	if p.To != kpB.Address() || p.ToMuxedID != "42" || p.From != kpI.Address() {
		t.Fatalf("%+v", p)
	}
}

func TestPagesAcrossLedgersWithoutLosingOrDuplicatingTransactions(t *testing.T) {
	var txs []txRec
	for i := 0; i < 7; i++ {
		txs = append(txs, txRec{Status: "SUCCESS", TxHash: hashN(i + 1), ApplicationOrder: uint32(i%2) + 1, Ledger: 10 + uint32(i/2),
			EnvelopeXdr: envelope(t, kpA, pay(kpB.Address(), nat, fmt.Sprintf("%d.0000000", i+1)))})
	}
	f := &fakeRPC{pageSize: 3, oldest: 1, latest: 100, txs: txs}
	got := collect(t, client(t, f), 10, 14)
	total := 0
	for l := uint32(10); l <= 13; l++ {
		total += len(got[l].Payments)
	}
	if total != 7 || len(got[10].Payments) != 2 || len(got[13].Payments) != 1 || len(got[14].Payments) != 0 {
		t.Fatalf("paging lost or duplicated payments: %d %v", total, got)
	}
	if f.calls.Load() < 3 {
		t.Fatalf("expected several pages, got %d calls", f.calls.Load())
	}
}

func TestStopsAtTheRequestedEndLedger(t *testing.T) {
	f := &fakeRPC{pageSize: 100, oldest: 1, latest: 100, txs: []txRec{
		{Status: "SUCCESS", TxHash: hashN(1), ApplicationOrder: 1, Ledger: 10, EnvelopeXdr: envelope(t, kpA, pay(kpB.Address(), nat, "1.0000000"))},
		{Status: "SUCCESS", TxHash: hashN(2), ApplicationOrder: 1, Ledger: 12, EnvelopeXdr: envelope(t, kpA, pay(kpB.Address(), nat, "2.0000000"))},
	}}
	got := collect(t, client(t, f), 10, 11)
	if _, ok := got[12]; ok || len(got[10].Payments) != 1 {
		t.Fatalf("%+v", got)
	}
}

func TestUnorderedProviderDataIsAnError(t *testing.T) {
	f := &fakeRPC{pageSize: 100, oldest: 1, latest: 100, txs: []txRec{
		{Status: "SUCCESS", TxHash: hashN(1), ApplicationOrder: 1, Ledger: 11, EnvelopeXdr: envelope(t, kpA, pay(kpB.Address(), nat, "1.0000000"))},
		{Status: "SUCCESS", TxHash: hashN(2), ApplicationOrder: 1, Ledger: 10, EnvelopeXdr: envelope(t, kpA, pay(kpB.Address(), nat, "2.0000000"))},
	}}
	err := client(t, f).Stream(context.Background(), 10, 12, func(source.LedgerData) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "unordered") {
		t.Fatalf("got %v", err)
	}
}

func TestCorruptEnvelopeIsAnErrorNotASkippedTransaction(t *testing.T) {
	f := &fakeRPC{pageSize: 100, oldest: 1, latest: 100, txs: []txRec{{Status: "SUCCESS", TxHash: hashN(1), ApplicationOrder: 1, Ledger: 10, EnvelopeXdr: "AAAA"}}}
	if err := client(t, f).Stream(context.Background(), 10, 10, func(source.LedgerData) error { return nil }); err == nil {
		t.Fatal("expected an error")
	}
}

func TestStartLedgerOutsideRetentionIsAHistoryGap(t *testing.T) {
	f := &fakeRPC{pageSize: 100, oldest: 500, latest: 600, startError: "start is before oldest ledger"}
	err := client(t, f).Stream(context.Background(), 10, 12, func(source.LedgerData) error { return nil })
	if !errors.Is(err, source.ErrHistoryUnavailable) || !errors.Is(err, ErrOutOfRetention) {
		t.Fatalf("got %v", err)
	}
}

func TestBoundsCombinesHealthAndNetwork(t *testing.T) {
	f := &fakeRPC{oldest: 500, latest: 600}
	b, err := client(t, f).Bounds(context.Background())
	if err != nil || b.Oldest != 500 || b.Latest != 600 || b.Network != network.TestNetworkPassphrase {
		t.Fatalf("%+v %v", b, err)
	}
}

func TestRetriesServerErrorsThenGivesUp(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { n.Add(1); http.Error(w, "x", 502) }))
	defer srv.Close()
	c := New(srv.URL)
	c.sleep = func(context.Context, time.Duration) error { return nil }
	c.MaxRetries = 2
	if _, err := c.Bounds(context.Background()); err == nil || n.Load() != 3 {
		t.Fatalf("err=%v attempts=%d", err, n.Load())
	}
}

func TestProviderIsOriginOnly(t *testing.T) {
	if p := New("https://rpc.example/secret-key").Provider(); p != "https://rpc.example" {
		t.Fatalf("provider %q", p)
	}
}
