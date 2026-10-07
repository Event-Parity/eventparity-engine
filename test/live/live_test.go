//go:build live

// Opt-in network test: go test -tags live ./test/live
// It reads the corpus ledger range from the real testnet endpoints and checks that the live adapters agree with each
// other and with the committed ground truth. RPC keeps only about seven days of history, so once the corpus range
// leaves its window this test asserts the coverage gap instead (which is itself the behavior under test).
package live

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Anasabubakar/eventparity-engine/internal/compare"
	"github.com/Anasabubakar/eventparity-engine/internal/horizon"
	"github.com/Anasabubakar/eventparity-engine/internal/ingest"
	"github.com/Anasabubakar/eventparity-engine/internal/model"
	"github.com/Anasabubakar/eventparity-engine/internal/rpcsrc"
)

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func TestLiveHorizonAndRPCOverTheCorpusRange(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	run := func(name string, cfg ingest.Config) *model.Stream {
		cfg.OutPath = filepath.Join(dir, name)
		cfg.From, cfg.To = 5071650, 5071658
		if _, err := ingest.Run(ctx, cfg); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		f, _ := os.Open(cfg.OutPath)
		defer f.Close()
		s, err := model.ReadStream(f)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	h := run("h.jsonl", ingest.Config{Source: horizon.New(env("EVENTPARITY_HORIZON", "https://horizon-testnet.stellar.org"))})
	r := run("r.jsonl", ingest.Config{Source: rpcsrc.New(env("EVENTPARITY_RPC", "https://soroban-testnet.stellar.org"))})
	if len(h.Coverage.Gaps) != 0 || len(h.Payments) != 10 {
		t.Fatalf("Horizon: gaps %v, payments %d (want 10)", h.Coverage.Gaps, len(h.Payments))
	}
	res, err := compare.Compare(compare.Input{Reference: h, Candidate: r})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Coverage.Gaps) == 0 {
		if res.Verdict != compare.Parity || res.Matched != 10 {
			t.Fatalf("expected parity over 10 payments: %s matched=%d diffs=%v", res.Verdict, res.Matched, res.Differences)
		}
		return
	}
	t.Logf("RPC no longer retains this range (%v); asserting the gap is reported honestly", r.Coverage.Gaps)
	if res.Verdict == compare.Parity || len(res.Differences) != 0 {
		t.Fatalf("a gap must give an inconclusive verdict with no invented differences: %s %v", res.Verdict, res.Differences)
	}
}
