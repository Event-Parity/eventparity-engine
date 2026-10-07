package ingest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Anasabubakar/eventparity-engine/internal/model"
	"github.com/Anasabubakar/eventparity-engine/internal/source"
)

const (
	addrA = "GAUGPIMKP4ZQ22FEUGDSTBJWTGIS5LDB5ZYUSQI33G37DFWLTY34OFAZ"
	addrB = "GBYFGKRQVWELTTR3425EQXZP4US5KSHAMJUQNH42AYUI54IJN6M4RQB4"
)

// fake serves one payment per ledger and an unsupported op in even ledgers.
type fake struct {
	bounds  source.Bounds
	failAt  uint32 // return this error when asked for this ledger
	failErr error
	skip    uint32 // emit out of order by skipping this ledger
}

func (f *fake) Adapter() string                               { return "fake" }
func (f *fake) Provider() string                              { return "https://fake.example" }
func (f *fake) Bounds(context.Context) (source.Bounds, error) { return f.bounds, nil }
func (f *fake) Stream(ctx context.Context, from, to uint32, emit func(source.LedgerData) error) error {
	for l := from; l <= to; l++ {
		if f.failAt == l {
			return f.failErr
		}
		if f.skip == l {
			continue
		}
		d := source.LedgerData{Ledger: l, Unsupported: map[string]int{}}
		d.Payments = []model.Payment{{Ledger: l, ApplicationOrder: 1, TxHash: strings.Repeat("a", 60) + fmt.Sprintf("%04d", l), OpIndex: 0,
			From: addrA, To: addrB, Asset: model.Asset{Type: "native"}, Amount: "1.0000000"}}
		if l%2 == 0 {
			d.Unsupported["manage_data"] = 1
		}
		if err := emit(d); err != nil {
			return err
		}
	}
	return nil
}

func cfg(t *testing.T, src source.Source, from, to uint32) Config {
	dir := t.TempDir()
	return Config{Source: src, From: from, To: to, OutPath: filepath.Join(dir, "s.jsonl"),
		Now: func() time.Time { return time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC) }}
}

func read(t *testing.T, path string) (*model.Stream, string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := model.ReadStream(strings.NewReader(string(b)))
	if err != nil {
		t.Fatalf("stream is not valid: %v\n%s", err, b)
	}
	return s, string(b)
}

func TestPlan(t *testing.T) {
	req := model.Range{From: 100, To: 200}
	cases := []struct {
		name     string
		b        source.Bounds
		covered  *model.Range
		gapCount int
	}{
		{"fully served", source.Bounds{Oldest: 1, Latest: 999}, &model.Range{From: 100, To: 200}, 0},
		{"starts before history", source.Bounds{Oldest: 150, Latest: 999}, &model.Range{From: 150, To: 200}, 1},
		{"ends after latest", source.Bounds{Oldest: 1, Latest: 180}, &model.Range{From: 100, To: 180}, 1},
		{"both ends", source.Bounds{Oldest: 120, Latest: 180}, &model.Range{From: 120, To: 180}, 2},
		{"entirely before history", source.Bounds{Oldest: 500, Latest: 999}, nil, 1},
		{"entirely after latest", source.Bounds{Oldest: 1, Latest: 50}, nil, 1},
	}
	for _, c := range cases {
		cov, gaps := Plan(req, c.b)
		if (cov == nil) != (c.covered == nil) || (cov != nil && *cov != *c.covered) || len(gaps) != c.gapCount {
			t.Errorf("%s: covered %v gaps %v", c.name, cov, gaps)
		}
	}
}

func TestFullRunWritesAValidStreamWithExactCoverage(t *testing.T) {
	c := cfg(t, &fake{bounds: source.Bounds{Oldest: 1, Latest: 100, Network: "n"}}, 10, 14)
	cov, err := Run(context.Background(), c)
	if err != nil || !cov.Complete {
		t.Fatalf("%v %+v", err, cov)
	}
	s, _ := read(t, c.OutPath)
	if len(s.Payments) != 5 || s.Unsupported["manage_data"] != 3 || s.Header.Network != "n" {
		t.Fatalf("unexpected stream: %d payments, unsupported %v", len(s.Payments), s.Unsupported)
	}
}

func TestOutOfRetentionRangeBecomesAGapNotMissingPayments(t *testing.T) {
	c := cfg(t, &fake{bounds: source.Bounds{Oldest: 12, Latest: 100}}, 10, 14)
	cov, err := Run(context.Background(), c)
	if err != nil || cov.Complete || len(cov.Gaps) != 1 || cov.Gaps[0].Range != (model.Range{From: 10, To: 11}) {
		t.Fatalf("%v %+v", err, cov)
	}
	s, _ := read(t, c.OutPath)
	if len(s.Payments) != 3 {
		t.Fatalf("payments in the covered part only, got %d", len(s.Payments))
	}
}

func TestProviderLosingHistoryMidRunRecordsAGapFromThatLedger(t *testing.T) {
	c := cfg(t, &fake{bounds: source.Bounds{Oldest: 1, Latest: 100}, failAt: 13, failErr: fmt.Errorf("x: %w", source.ErrHistoryUnavailable)}, 10, 15)
	cov, err := Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if cov.Complete || len(cov.Covered) != 1 || cov.Covered[0] != (model.Range{From: 10, To: 12}) || cov.Gaps[0].Range != (model.Range{From: 13, To: 15}) {
		t.Fatalf("%+v", cov)
	}
	read(t, c.OutPath)
}

// The crash test: the process dies after a ledger's lines are written but before the checkpoint is updated.
func TestRestartAfterACrashProducesTheSameStreamAsAnUninterruptedRun(t *testing.T) {
	src := &fake{bounds: source.Bounds{Oldest: 1, Latest: 100}}

	clean := cfg(t, src, 10, 19)
	if _, err := Run(context.Background(), clean); err != nil {
		t.Fatal(err)
	}
	_, want := read(t, clean.OutPath)

	crash := cfg(t, src, 10, 19)
	crash.afterWrite = func(l uint32) error {
		if l == 14 {
			return errors.New("simulated crash between write and checkpoint")
		}
		return nil
	}
	if _, err := Run(context.Background(), crash); err == nil {
		t.Fatal("expected the simulated crash")
	}
	partial, _ := os.ReadFile(crash.OutPath)
	if !strings.Contains(string(partial), "0014") {
		t.Fatal("test setup: ledger 14's lines should already be on disk before the restart")
	}

	crash.afterWrite = nil
	if _, err := Run(context.Background(), crash); err != nil {
		t.Fatal(err)
	}
	_, got := read(t, crash.OutPath)
	if got != want {
		t.Fatalf("restarted stream differs from an uninterrupted one (duplicate or skipped ledger?)\nwant:\n%s\ngot:\n%s", want, got)
	}
	if n := strings.Count(got, "0014"); n != 1 {
		t.Fatalf("ledger 14's payment appears %d times", n)
	}
}

func TestRestartAfterASourceErrorResumesFromTheCheckpoint(t *testing.T) {
	c := cfg(t, &fake{bounds: source.Bounds{Oldest: 1, Latest: 100}, failAt: 16, failErr: errors.New("network down")}, 10, 19)
	if _, err := Run(context.Background(), c); err == nil {
		t.Fatal("expected the source error")
	}
	cp, _ := os.ReadFile(c.OutPath + ".checkpoint")
	if !strings.Contains(string(cp), `"lastLedger": 15`) {
		t.Fatalf("checkpoint should record ledger 15:\n%s", cp)
	}
	c.Source = &fake{bounds: source.Bounds{Oldest: 1, Latest: 100}}
	if _, err := Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	s, _ := read(t, c.OutPath)
	if len(s.Payments) != 10 {
		t.Fatalf("got %d payments", len(s.Payments))
	}
}

func TestRefusals(t *testing.T) {
	src := &fake{bounds: source.Bounds{Oldest: 1, Latest: 100}}
	c := cfg(t, src, 10, 12)
	if _, err := Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), c); err == nil || !strings.Contains(err.Error(), "already complete") {
		t.Errorf("a finished stream must not be rerun silently: %v", err)
	}

	c2 := cfg(t, src, 10, 12)
	os.WriteFile(c2.OutPath, []byte("precious"), 0o644)
	if _, err := Run(context.Background(), c2); err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Errorf("an existing file without a checkpoint must not be overwritten: %v", err)
	}
	if b, _ := os.ReadFile(c2.OutPath); string(b) != "precious" {
		t.Error("the existing file was modified")
	}

	c3 := cfg(t, &fake{bounds: source.Bounds{Oldest: 1, Latest: 100}, failAt: 11, failErr: errors.New("down")}, 10, 12)
	Run(context.Background(), c3)
	c3.Source = src
	c3.To = 13 // a different job
	if _, err := Run(context.Background(), c3); err == nil || !strings.Contains(err.Error(), "different job") {
		t.Errorf("resuming a different job must be refused: %v", err)
	}
	if _, err := Run(context.Background(), Config{Source: src, From: 5, To: 4, OutPath: filepath.Join(t.TempDir(), "x")}); err == nil {
		t.Error("an inverted range must be refused")
	}
}

func TestAdapterOrderViolationsAreCaught(t *testing.T) {
	c := cfg(t, &fake{bounds: source.Bounds{Oldest: 1, Latest: 100}, skip: 12}, 10, 14)
	if _, err := Run(context.Background(), c); err == nil || !strings.Contains(err.Error(), "expected 12") {
		t.Fatalf("a skipped ledger must be an error, got %v", err)
	}
}
