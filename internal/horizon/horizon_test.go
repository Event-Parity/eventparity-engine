package horizon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Anasabubakar/eventparity-engine/internal/source"
)

const (
	alice = "GAUGPIMKP4ZQ22FEUGDSTBJWTGIS5LDB5ZYUSQI33G37DFWLTY34OFAZ"
	bob   = "GBYFGKRQVWELTTR3425EQXZP4US5KSHAMJUQNH42AYUI54IJN6M4RQB4"
	iss   = "GDM3YHMRTRHLXI4MHQSMT4N42TAGPFCMSMZPO7UHJ4I7ZHZ5MVWXGPK2"
	hash1 = "79cc1ba2a38e512f5046d43d4bfeea84b110eed7531460eb70d6efced16d647b"
)

func toid(ledger, order, op uint64) string { return fmt.Sprint(ledger<<32 | order<<12 | op) }

func payRec(ledger, order, op uint64, amount string) string {
	return fmt.Sprintf(`{"id":"%s","type":"payment","transaction_hash":"%s","transaction_successful":true,"from":"%s","to":"%s","asset_type":"native","amount":"%s"}`,
		toid(ledger, order, op+1), hash1, alice, bob, amount)
}

func pageJSON(next string, recs ...string) string {
	return `{"_links":{"next":{"href":"` + next + `"}},"_embedded":{"records":[` + strings.Join(recs, ",") + `]}}`
}

// listing serves what real Horizon does: the records, a next link, then an explicit empty page.
func listing(recs ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host + r.URL.Path
		if r.URL.Query().Get("cursor") == "" {
			fmt.Fprint(w, pageJSON(base+"?cursor=end", recs...))
			return
		}
		fmt.Fprint(w, pageJSON(base+"?cursor=end"))
	}
}

func newClient(t *testing.T, h http.HandlerFunc) *Client {
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New(srv.URL)
	c.sleep = func(context.Context, time.Duration) error { return nil }
	return c
}

func TestLedgerReadsPaymentsAcrossPagesAndCountsOtherTypes(t *testing.T) {
	var base string
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cursor") == "" {
			fmt.Fprint(w, pageJSON(base+"/ledgers/50/operations?cursor=c1", payRec(50, 1, 0, "1.5000000"),
				`{"id":"`+toid(50, 1, 2)+`","type":"manage_data","transaction_hash":"`+hash1+`","transaction_successful":true}`))
			return
		}
		if r.URL.Query().Get("cursor") == "c1" {
			fmt.Fprint(w, pageJSON(base+"/ledgers/50/operations?cursor=c2", payRec(50, 2, 0, "2.0000000")))
			return
		}
		fmt.Fprint(w, pageJSON(base+"/x"))
	})
	base = c.Base
	d, err := c.Ledger(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Payments) != 2 || d.Unsupported["manage_data"] != 1 || d.Payments[1].ApplicationOrder != 2 || d.Payments[0].OpIndex != 0 {
		t.Fatalf("%+v", d)
	}
	if d.Payments[0].Amount != "1.5000000" {
		t.Fatalf("amount %q", d.Payments[0].Amount)
	}
}

func TestOperationIdentityComesFromTheTOID(t *testing.T) {
	c := newClient(t, listing(payRec(77, 5, 3, "1.0000000")))
	d, err := c.Ledger(context.Background(), 77)
	if err != nil {
		t.Fatal(err)
	}
	p := d.Payments[0]
	if p.Ledger != 77 || p.ApplicationOrder != 5 || p.OpIndex != 3 {
		t.Fatalf("%+v", p)
	}
}

func TestIssuedAssetMuxedAndFailedTransactionHandling(t *testing.T) {
	failed := `{"id":"` + toid(9, 1, 1) + `","type":"payment","transaction_hash":"` + hash1 + `","transaction_successful":false,"from":"` + alice + `","to":"` + bob + `","asset_type":"native","amount":"1.0000000"}`
	issued := `{"id":"` + toid(9, 2, 1) + `","type":"payment","transaction_hash":"` + hash1 + `","transaction_successful":true,"from":"` + alice + `","to":"` + bob + `","to_muxed_id":"42","asset_type":"credit_alphanum4","asset_code":"USDX","asset_issuer":"` + iss + `","amount":"5"}`
	c := newClient(t, listing(failed, issued))
	d, err := c.Ledger(context.Background(), 9)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Payments) != 1 {
		t.Fatalf("a failed transaction's payment must be excluded, got %d payments", len(d.Payments))
	}
	p := d.Payments[0]
	if p.Asset.Code != "USDX" || p.Asset.Issuer != iss || p.ToMuxedID != "42" || p.Amount != "5.0000000" {
		t.Fatalf("%+v", p)
	}
}

func TestMalformedOrInconsistentRecordsFailInsteadOfBeingSkipped(t *testing.T) {
	cases := map[string]string{
		"wrong ledger":       pageJSON("", payRec(51, 1, 0, "1.0000000")),
		"bad amount":         pageJSON("", payRec(50, 1, 0, "1.00000001")),
		"unknown asset type": pageJSON("", strings.Replace(payRec(50, 1, 0, "1.0000000"), `"native"`, `"weird"`, 1)),
		"duplicate op id":    pageJSON("", payRec(50, 1, 0, "1.0000000"), payRec(50, 1, 0, "1.0000000")),
		"bad address":        pageJSON("", strings.Replace(payRec(50, 1, 0, "1.0000000"), bob, "GBAD", 1)),
		"not json":           "{nope",
	}
	for name, body := range cases {
		c := newClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
		if _, err := c.Ledger(context.Background(), 50); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestNotFoundIsAHistoryGap(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	_, err := c.Ledger(context.Background(), 5)
	if !errors.Is(err, source.ErrHistoryUnavailable) || !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestRetriesTransientFailuresThenSucceeds(t *testing.T) {
	var n atomic.Int32
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) < 3 {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, pageJSON(""))
	})
	if _, err := c.Ledger(context.Background(), 50); err != nil || n.Load() != 3 {
		t.Fatalf("err=%v attempts=%d", err, n.Load())
	}
}

func TestGivesUpAfterBoundedRetries(t *testing.T) {
	var n atomic.Int32
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) { n.Add(1); http.Error(w, "down", 503) })
	c.MaxRetries = 2
	_, err := c.Ledger(context.Background(), 50)
	if err == nil || n.Load() != 3 || !strings.Contains(err.Error(), "giving up after 3 attempts") {
		t.Fatalf("err=%v attempts=%d", err, n.Load())
	}
}

func TestClientErrorsAreNotRetriedAndDoNotLeakQueryStrings(t *testing.T) {
	var n atomic.Int32
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) { n.Add(1); http.Error(w, "bad", 400) })
	_, err := c.Ledger(context.Background(), 50)
	if err == nil || n.Load() != 1 || strings.Contains(err.Error(), "limit=") {
		t.Fatalf("err=%v attempts=%d", err, n.Load())
	}
}

func TestContextCancellationStopsRetries(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", 503) })
	ctx, cancel := context.WithCancel(context.Background())
	c.sleep = func(ctx context.Context, d time.Duration) error { cancel(); return ctx.Err() }
	if _, err := c.Ledger(ctx, 50); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestBoundsReadsHistoryWindowAndNetwork(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"history_elder_ledger":128,"history_latest_ledger":5071757,"network_passphrase":"Test SDF Network ; September 2015"}`)
	})
	b, err := c.Bounds(context.Background())
	if err != nil || b.Oldest != 128 || b.Latest != 5071757 || b.Network != "Test SDF Network ; September 2015" {
		t.Fatalf("%+v %v", b, err)
	}
	c2 := newClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{}`) })
	if _, err := c2.Bounds(context.Background()); err == nil {
		t.Fatal("a root document without a latest ledger must be an error")
	}
}

func TestStreamEmitsEveryLedgerInOrderIncludingEmptyOnes(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/ledgers/11/") {
			listing(payRec(11, 1, 0, "1.0000000"))(w, r)
			return
		}
		listing()(w, r)
	})
	var got []uint32
	var pays int
	err := c.Stream(context.Background(), 10, 12, func(d source.LedgerData) error { got = append(got, d.Ledger); pays += len(d.Payments); return nil })
	if err != nil || len(got) != 3 || got[0] != 10 || got[2] != 12 || pays != 1 {
		t.Fatalf("%v %v pays=%d", got, err, pays)
	}
}

func TestProviderIsOriginOnly(t *testing.T) {
	if p := New("https://user:pw@horizon.example/path?key=secret").Provider(); p != "https://horizon.example" {
		t.Fatalf("provider %q leaks credentials or path", p)
	}
}

func TestIncompleteResponsesAreErrorsNotEmptyLedgers(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"empty object":             func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{}`) },
		"embedded without records": func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"_embedded":{},"_links":{}}`) },
		"records without a next link": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, pageJSON("", payRec(50, 1, 0, "1.0000000")))
		},
		"links without next": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"_links":{},"_embedded":{"records":[`+payRec(50, 1, 0, "1.0000000")+`]}}`)
		},
		"next link that never advances": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, pageJSON("http://"+r.Host+r.URL.RequestURI(), payRec(50, 1, 0, "1.0000000")))
		},
	}
	for name, h := range cases {
		c := newClient(t, h)
		if d, err := c.Ledger(context.Background(), 50); err == nil {
			t.Errorf("%s: expected an error, got %d payments and no error", name, len(d.Payments))
		}
	}
}

func TestAnExplicitEmptyPageIsAnEmptyLedger(t *testing.T) {
	c := newClient(t, listing())
	d, err := c.Ledger(context.Background(), 50)
	if err != nil || len(d.Payments) != 0 {
		t.Fatalf("%+v %v", d, err)
	}
}
