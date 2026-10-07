// Package ingest runs an adapter over a ledger range and writes a stream file with an exact coverage line.
//
// Restart semantics: after every complete ledger the runner fsyncs the stream and then atomically replaces a small
// checkpoint file holding the last completed ledger and the stream's byte length at that moment. On restart the stream
// is truncated to that length before continuing, so a crash between writing a ledger and updating the checkpoint
// re-reads that ledger instead of duplicating or skipping it.
package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/Anasabubakar/eventparity-engine/internal/model"
	"github.com/Anasabubakar/eventparity-engine/internal/source"
)

// Checkpoint is the sidecar state file.
type Checkpoint struct {
	Version     int         `json:"version"`
	Adapter     string      `json:"adapter"`
	Provider    string      `json:"provider"`
	Network     string      `json:"network,omitempty"`
	Requested   model.Range `json:"requested"`
	Covered     model.Range `json:"covered"`    // the ledgers this run set out to read
	LastLedger  uint32      `json:"lastLedger"` // last ledger fully written; 0 before the first
	OutputBytes int64       `json:"outputBytes"`
	Done        bool        `json:"done"`
}

// Config configures a run.
type Config struct {
	Source         source.Source
	From, To       uint32
	OutPath        string
	CheckpointPath string // default OutPath + ".checkpoint"
	Now            func() time.Time
	Tool           string
	Log            func(format string, args ...any)

	// AfterWrite is a fault-injection hook (used by tests) called after a ledger's lines are written and synced, before the checkpoint is updated.
	AfterWrite func(ledger uint32) error
}

func (c *Config) defaults() {
	if c.CheckpointPath == "" {
		c.CheckpointPath = c.OutPath + ".checkpoint"
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Log == nil {
		c.Log = func(string, ...any) {}
	}
}

// Plan splits the requested range into what the provider can serve now and the gaps.
func Plan(req model.Range, b source.Bounds) (covered *model.Range, gaps []model.Gap) {
	lo, hi := req.From, req.To
	if b.Oldest > lo {
		gapTo := min(b.Oldest-1, req.To)
		gaps = append(gaps, model.Gap{Range: model.Range{From: lo, To: gapTo}, Reason: fmt.Sprintf("before the provider's oldest available ledger (%d)", b.Oldest)})
		lo = gapTo + 1
	}
	if lo <= req.To && b.Latest < req.To {
		gapFrom := max(b.Latest+1, lo)
		if gapFrom <= req.To {
			gaps = append(gaps, model.Gap{Range: model.Range{From: gapFrom, To: req.To}, Reason: fmt.Sprintf("after the provider's latest ledger (%d)", b.Latest)})
			hi = gapFrom - 1
		}
	}
	if lo <= hi && lo <= req.To {
		r := model.Range{From: lo, To: hi}
		return &r, gaps
	}
	return nil, gaps
}

// Run reads the range and writes the stream. It returns the final coverage.
func Run(ctx context.Context, cfg Config) (*model.Coverage, error) {
	cfg.defaults()
	if cfg.From == 0 || cfg.To < cfg.From {
		return nil, fmt.Errorf("invalid ledger range %d-%d", cfg.From, cfg.To)
	}
	bounds, err := cfg.Source.Bounds(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading provider bounds: %w", err)
	}
	req := model.Range{From: cfg.From, To: cfg.To}
	covered, gaps := Plan(req, bounds)

	ident := Checkpoint{Version: 1, Adapter: cfg.Source.Adapter(), Provider: cfg.Source.Provider(), Network: bounds.Network, Requested: req}
	if covered != nil {
		ident.Covered = *covered
	}

	f, cp, err := openOutput(cfg, ident)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	if cp.Done {
		return nil, fmt.Errorf("%s is already complete; remove it and %s to fetch again", cfg.OutPath, cfg.CheckpointPath)
	}
	if cp.LastLedger == 0 && cp.OutputBytes == 0 {
		h := model.Header{Type: "header", Format: model.StreamFormat, Adapter: ident.Adapter, Provider: ident.Provider, Network: bounds.Network,
			Requested: req, CreatedAt: cfg.Now().UTC().Format(time.RFC3339), Scope: model.Scope, Tool: cfg.Tool}
		if err := model.Encode(f, h); err != nil {
			return nil, err
		}
		if err := commit(f, cfg, &cp); err != nil {
			return nil, err
		}
	}

	final := &model.Coverage{Type: "coverage", Covered: []model.Range{}, Gaps: gaps}
	if covered != nil {
		start := covered.From
		if cp.LastLedger >= start {
			start = cp.LastLedger + 1
		}
		expect := start
		emit := func(d source.LedgerData) error {
			if d.Ledger != expect {
				return fmt.Errorf("adapter emitted ledger %d, expected %d", d.Ledger, expect)
			}
			if len(d.Unsupported) > 0 {
				if err := model.Encode(f, model.LedgerNote{Type: "ledger", Ledger: d.Ledger, Unsupported: d.Unsupported}); err != nil {
					return err
				}
			}
			for _, p := range d.Payments {
				if p.Ledger != d.Ledger {
					return fmt.Errorf("payment %s reports ledger %d inside ledger %d", p.Key(), p.Ledger, d.Ledger)
				}
				if err := model.Encode(f, struct {
					Type string `json:"type"`
					model.Payment
				}{"payment", p}); err != nil {
					return err
				}
			}
			if err := f.Sync(); err != nil {
				return err
			}
			if cfg.AfterWrite != nil {
				if err := cfg.AfterWrite(d.Ledger); err != nil {
					return err
				}
			}
			cp.LastLedger = d.Ledger
			expect++
			return commit(f, cfg, &cp)
		}
		if start <= covered.To {
			cfg.Log("reading ledgers %d-%d from %s", start, covered.To, ident.Provider)
			if err := cfg.Source.Stream(ctx, start, covered.To, emit); err != nil {
				if errors.Is(err, source.ErrHistoryUnavailable) && expect > covered.From {
					// The provider lost history mid-run: cover what was read, record the rest as a gap.
					gaps = append(gaps, model.Gap{Range: model.Range{From: expect, To: covered.To}, Reason: "provider reported history unavailable: " + err.Error()})
					final.Gaps = gaps
					covered.To = expect - 1
				} else if errors.Is(err, source.ErrHistoryUnavailable) {
					gaps = append(gaps, model.Gap{Range: model.Range{From: covered.From, To: covered.To}, Reason: "provider reported history unavailable: " + err.Error()})
					final.Gaps = gaps
					covered = nil
				} else {
					return nil, err
				}
			}
		}
		if covered != nil {
			final.Covered = []model.Range{*covered}
		}
	}
	sortGaps(final)
	final.Complete = len(final.Gaps) == 0
	if err := model.Encode(f, final); err != nil {
		return nil, err
	}
	cp.Done = true
	if err := commit(f, cfg, &cp); err != nil {
		return nil, err
	}
	return final, nil
}

func sortGaps(c *model.Coverage) {
	for i := 1; i < len(c.Gaps); i++ {
		for j := i; j > 0 && c.Gaps[j].From < c.Gaps[j-1].From; j-- {
			c.Gaps[j], c.Gaps[j-1] = c.Gaps[j-1], c.Gaps[j]
		}
	}
}

// openOutput opens the stream for a fresh run or a resume, enforcing that a resume matches the same job.
func openOutput(cfg Config, ident Checkpoint) (*os.File, Checkpoint, error) {
	raw, err := os.ReadFile(cfg.CheckpointPath)
	switch {
	case err == nil:
		var cp Checkpoint
		if err := json.Unmarshal(raw, &cp); err != nil {
			return nil, cp, fmt.Errorf("checkpoint %s is corrupt: %w", cfg.CheckpointPath, err)
		}
		if cp.Adapter != ident.Adapter || cp.Provider != ident.Provider || cp.Requested != ident.Requested || cp.Network != ident.Network {
			return nil, cp, fmt.Errorf("checkpoint %s belongs to a different job (adapter/provider/network/range differ); refusing to resume", cfg.CheckpointPath)
		}
		f, err := os.OpenFile(cfg.OutPath, os.O_RDWR, 0o644)
		if err != nil {
			return nil, cp, fmt.Errorf("checkpoint exists but stream %s cannot be opened: %w", cfg.OutPath, err)
		}
		st, err := f.Stat()
		if err != nil || st.Size() < cp.OutputBytes {
			f.Close()
			return nil, cp, fmt.Errorf("stream %s is shorter than its checkpoint says (%d bytes); it was modified or truncated", cfg.OutPath, cp.OutputBytes)
		}
		if err := f.Truncate(cp.OutputBytes); err != nil {
			f.Close()
			return nil, cp, err
		}
		if _, err := f.Seek(cp.OutputBytes, 0); err != nil {
			f.Close()
			return nil, cp, err
		}
		cp.Covered = ident.Covered
		return f, cp, nil
	case errors.Is(err, os.ErrNotExist):
		if _, serr := os.Stat(cfg.OutPath); serr == nil {
			return nil, ident, fmt.Errorf("%s already exists without a checkpoint; refusing to overwrite it", cfg.OutPath)
		}
		f, err := os.OpenFile(cfg.OutPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
		return f, ident, err
	default:
		return nil, ident, err
	}
}

// commit fsyncs the stream and atomically replaces the checkpoint.
func commit(f *os.File, cfg Config, cp *Checkpoint) error {
	if err := f.Sync(); err != nil {
		return err
	}
	off, err := f.Seek(0, 1)
	if err != nil {
		return err
	}
	cp.OutputBytes = off
	b, _ := json.MarshalIndent(cp, "", " ")
	tmp := cfg.CheckpointPath + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, cfg.CheckpointPath)
}
