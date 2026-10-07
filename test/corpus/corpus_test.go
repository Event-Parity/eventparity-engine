// Package corpus tests the adapters, runner, comparator and reports against real recorded Stellar testnet data.
//
// Expectations come from two independent places: ground-truth.json (what mkcorpus built and submitted, never read
// from any adapter) and the hand-reviewed counts named in each test. Nothing here asks the code under test what
// the answer should be.
package corpus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Anasabubakar/eventparity-engine/internal/compare"
	"github.com/Anasabubakar/eventparity-engine/internal/horizon"
	"github.com/Anasabubakar/eventparity-engine/internal/ingest"
	"github.com/Anasabubakar/eventparity-engine/internal/model"
	"github.com/Anasabubakar/eventparity-engine/internal/recorder"
	"github.com/Anasabubakar/eventparity-engine/internal/report"
	"github.com/Anasabubakar/eventparity-engine/internal/rpcsrc"
)

const (
	main = "../../corpus/testnet-2026-10-07"
	gap  = "../../corpus/retention-gap-2026-10-07"
)

func chk(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	chk(t, err)
	return b
}

func parseStream(t *testing.T, b []byte) *model.Stream {
	t.Helper()
	s, err := model.ReadStream(bytes.NewReader(b))
	chk(t, err)
	return s
}

func parseTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	chk(t, err)
	return v
}

func newReplayer(t *testing.T, dir string) *recorder.Replayer {
	t.Helper()
	r, err := recorder.NewReplayer(dir)
	chk(t, err)
	return r
}

func doCompare(t *testing.T, in compare.Input) *compare.Result {
	t.Helper()
	r, err := compare.Compare(in)
	chk(t, err)
	return r
}

func readStream(t *testing.T, path string) (*model.Stream, []byte) {
	t.Helper()
	b := readFile(t, path)
	return parseStream(t, b), b
}

func headerTime(t *testing.T, path string) time.Time {
	s, _ := readStream(t, path)
	return parseTime(t, s.Header.CreatedAt)
}

// replayHorizon and replayRPC build adapters whose HTTP layer serves only the recorded real responses.
func replayHorizon(t *testing.T, dir string) *horizon.Client {
	c := horizon.New("https://horizon-testnet.stellar.org")
	c.HTTP = &http.Client{Transport: newReplayer(t, filepath.Join(dir, "recorded", "horizon"))}
	return c
}

func replayRPC(t *testing.T, dir string) *rpcsrc.Client {
	c := rpcsrc.New("https://soroban-testnet.stellar.org")
	c.HTTP = &http.Client{Transport: newReplayer(t, filepath.Join(dir, "recorded", "rpc"))}
	return c
}

func rerun(t *testing.T, dir, name string, from, to uint32) string {
	t.Helper()
	committed := filepath.Join(dir, name+".jsonl")
	out := filepath.Join(t.TempDir(), name+".jsonl")
	cfg := ingest.Config{From: from, To: to, OutPath: out, Tool: "eventparity-engine record", Now: func() time.Time { return headerTime(t, committed) }}
	if name == "horizon" {
		cfg.Source = replayHorizon(t, dir)
	} else {
		cfg.Source = replayRPC(t, dir)
	}
	_, err := ingest.Run(context.Background(), cfg)
	chk(t, err)
	return out
}

func TestReplayedAdaptersReproduceTheCommittedStreamsExactly(t *testing.T) {
	for _, tc := range []struct {
		dir, name string
		from, to  uint32
	}{
		{main, "horizon", 5071650, 5071658}, {main, "rpc", 5071650, 5071658},
		{gap, "horizon", 4950790, 4950810}, {gap, "rpc", 4950790, 4950810},
	} {
		got := readFile(t, rerun(t, tc.dir, tc.name, tc.from, tc.to))
		want := readFile(t, filepath.Join(tc.dir, tc.name+".jsonl"))
		if !bytes.Equal(got, want) {
			t.Errorf("%s/%s: replayed stream differs from the committed one", filepath.Base(tc.dir), tc.name)
		}
	}
}

type groundTruth struct {
	Txs []struct {
		Label      string `json:"label"`
		Hash       string `json:"hash"`
		Ledger     uint32 `json:"ledger"`
		Successful bool   `json:"successful"`
		Ops        []struct {
			Index       uint32 `json:"index"`
			Type        string `json:"type"`
			From        string `json:"from"`
			To          string `json:"to"`
			ToMuxedID   string `json:"toMuxedId"`
			AssetType   string `json:"assetType"`
			AssetCode   string `json:"assetCode"`
			AssetIssuer string `json:"assetIssuer"`
			Amount      string `json:"amount"`
		} `json:"ops"`
	} `json:"txs"`
}

func TestBothStreamsMatchTheIndependentGroundTruth(t *testing.T) {
	var gt groundTruth
	chk(t, json.Unmarshal(readFile(t, filepath.Join(main, "ground-truth.json")), &gt))
	for _, name := range []string{"horizon", "rpc"} {
		s, _ := readStream(t, filepath.Join(main, name+".jsonl"))
		byKey := map[string]model.Payment{}
		for _, p := range s.Payments {
			byKey[p.Key()] = p
		}
		wantPayments := 0
		for _, tx := range gt.Txs {
			for _, op := range tx.Ops {
				key := tx.Hash + ":" + string(rune('0'+op.Index))
				got, found := byKey[key]
				if op.Type != "payment" || !tx.Successful {
					if found {
						t.Errorf("%s: %s op %d (%s, successful=%v) must not appear as a payment", name, tx.Label, op.Index, op.Type, tx.Successful)
					}
					continue
				}
				wantPayments++
				if !found {
					t.Errorf("%s: %s op %d missing", name, tx.Label, op.Index)
					continue
				}
				asset := model.Asset{Type: "native"}
				if op.AssetType == "credit" {
					asset = model.Asset{Type: "credit", Code: op.AssetCode, Issuer: op.AssetIssuer}
				}
				if got.From != op.From || got.To != op.To || got.ToMuxedID != op.ToMuxedID || got.Asset != asset || got.Amount != op.Amount || got.Ledger != tx.Ledger {
					t.Errorf("%s: %s op %d differs from ground truth:\n got %+v\nwant %+v", name, tx.Label, op.Index, got, op)
				}
			}
		}
		// 8 payments were submitted by the generator; the same ledgers also hold 2 payments by other accounts.
		if wantPayments != 8 || len(s.Payments) != 10 {
			t.Errorf("%s: ground truth lists %d payments (want 8), stream holds %d (want 10)", name, wantPayments, len(s.Payments))
		}
		// Unsupported operations the generator submitted must be counted, per ledger, by their Horizon type name.
		for _, tx := range gt.Txs {
			if !tx.Successful {
				continue
			}
			for _, op := range tx.Ops {
				if op.Type != "payment" && s.UnsupportedByLedger[tx.Ledger][op.Type] < 1 {
					t.Errorf("%s: unsupported %s in ledger %d not counted", name, op.Type, tx.Ledger)
				}
			}
		}
	}
}

func TestHorizonAndRPCAgreeOnTheCorpus(t *testing.T) {
	h, _ := readStream(t, filepath.Join(main, "horizon.jsonl"))
	r, _ := readStream(t, filepath.Join(main, "rpc.jsonl"))
	res := doCompare(t, compare.Input{Reference: h, Candidate: r})
	if res.Verdict != compare.Parity || res.Matched != 10 || len(res.Differences) != 0 || len(res.Unsupported) != 0 {
		t.Fatalf("expected parity over 10 payments, got %s matched=%d diffs=%v unsupported=%v", res.Verdict, res.Matched, res.Differences, res.Unsupported)
	}
}

func TestDefectiveCandidateIsCaughtWithExactEvidence(t *testing.T) {
	h, _ := readStream(t, filepath.Join(main, "horizon.jsonl"))
	c, _ := readStream(t, filepath.Join(main, "candidate-defective.jsonl"))
	res := doCompare(t, compare.Input{Reference: h, Candidate: c})
	const dropped = "7024ffc7195b4b2295b075375c42419e39c37a902fd9b5141e487657fb4b2ae6:0"    // issued-asset-payment
	const duplicated = "35308107ee38795d1d2e734407245ca50797ab97aaa3ccd16ca60e7ee7fba3e5:1" // multi-op, 2.0000000 USDX
	if res.Verdict != compare.Differences || len(res.Differences) != 2 || res.Matched != 9 {
		t.Fatalf("verdict %s, %d differences, matched %d", res.Verdict, len(res.Differences), res.Matched)
	}
	byClass := map[compare.Class]compare.Difference{}
	for _, d := range res.Differences {
		byClass[d.Class] = d
	}
	if d := byClass[compare.MissingInCandidate]; d.Key != dropped || len(d.Reference) != 1 || d.Reference[0].Amount != "5.0000000" {
		t.Errorf("dropped payment not identified: %+v", d)
	}
	if d := byClass[compare.DuplicatedInCandidate]; d.Key != duplicated || len(d.Candidate) != 2 || d.Candidate[0].Amount != "2.0000000" {
		t.Errorf("duplicated payment not identified: %+v", d)
	}
}

func TestRetentionBoundaryIsACoverageGapNotMissingPayments(t *testing.T) {
	h, _ := readStream(t, filepath.Join(gap, "horizon.jsonl"))
	r, _ := readStream(t, filepath.Join(gap, "rpc.jsonl"))
	if len(h.Coverage.Gaps) != 0 || h.Coverage.Covered[0] != (model.Range{From: 4950790, To: 4950810}) {
		t.Fatalf("Horizon should cover the whole range: %+v", h.Coverage)
	}
	if len(r.Coverage.Gaps) != 1 || r.Coverage.Gaps[0].Range != (model.Range{From: 4950790, To: 4950801}) || r.Coverage.Covered[0] != (model.Range{From: 4950802, To: 4950810}) {
		t.Fatalf("RPC coverage should start at its oldest ledger: %+v", r.Coverage)
	}
	res := doCompare(t, compare.Input{Reference: h, Candidate: r})
	if res.Verdict != compare.Inconclusive || len(res.Differences) != 0 || res.NotComparedInGaps != 11 || res.Matched != 10 {
		t.Fatalf("verdict %s diffs %d notCompared %d matched %d", res.Verdict, len(res.Differences), res.NotComparedInGaps, res.Matched)
	}
	if hits := 0; true {
		for _, p := range h.Payments {
			if p.Ledger <= 4950801 {
				hits++
			}
		}
		if hits != 11 {
			t.Fatalf("test premise: Horizon holds 11 payments in the RPC gap, found %d", hits)
		}
	}
}

func TestRestartingARealAdapterRunAfterACrashReproducesTheCommittedStream(t *testing.T) {
	committed := filepath.Join(main, "horizon.jsonl")
	out := filepath.Join(t.TempDir(), "horizon.jsonl")
	cfg := ingest.Config{Source: replayHorizon(t, main), From: 5071650, To: 5071658, OutPath: out, Tool: "eventparity-engine record",
		Now: func() time.Time { return headerTime(t, committed) }}
	cfg.AfterWrite = func(l uint32) error {
		if l == 5071654 {
			return errors.New("simulated crash")
		}
		return nil
	}
	if _, err := ingest.Run(context.Background(), cfg); err == nil {
		t.Fatal("expected the simulated crash")
	}
	cfg.AfterWrite = nil
	cfg.Source = replayHorizon(t, main)
	if _, err := ingest.Run(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readFile(t, out), readFile(t, committed)) {
		t.Fatal("restarted run differs from the committed stream (duplicate or skipped ledger)")
	}
}

type golden struct{ name, ref, cand string }

var goldens = []golden{
	{"report-parity", main + "/horizon.jsonl", main + "/rpc.jsonl"},
	{"report-defective", main + "/horizon.jsonl", main + "/candidate-defective.jsonl"},
	{"report-retention-gap", gap + "/horizon.jsonl", gap + "/rpc.jsonl"},
}

func buildReport(t *testing.T, g golden) *report.Report {
	h, hb := readStream(t, g.ref)
	c, cb := readStream(t, g.cand)
	res := doCompare(t, compare.Input{Reference: h, Candidate: c, ReferenceName: strings.TrimPrefix(g.ref, "../../"), CandidateName: strings.TrimPrefix(g.cand, "../../")})
	return report.Build(res, report.Inputs{ReferenceBytes: hb, CandidateBytes: cb, Now: time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)})
}

// TestGoldenReports pins the exact JSON reports. Run with UPDATE_GOLDEN=1 to regenerate after an intended change.
func TestGoldenReports(t *testing.T) {
	for _, g := range goldens {
		var buf bytes.Buffer
		chk(t, buildReport(t, g).JSON(&buf))
		path := filepath.Join("../../corpus/reports", g.name+".json")
		if os.Getenv("UPDATE_GOLDEN") == "1" {
			chk(t, os.MkdirAll(filepath.Dir(path), 0o755))
			chk(t, os.WriteFile(path, buf.Bytes(), 0o644))
			continue
		}
		if want := readFile(t, path); !bytes.Equal(want, buf.Bytes()) {
			t.Errorf("%s differs from the committed golden report; rerun with UPDATE_GOLDEN=1 if the change is intended", g.name)
		}
	}
}
