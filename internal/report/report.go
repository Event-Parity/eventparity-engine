// Package report builds the versioned comparison report and renders it as JSON, text and self-contained HTML.
package report

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Anasabubakar/eventparity-engine/internal/compare"
	"github.com/Anasabubakar/eventparity-engine/internal/model"
)

// Version is the report schema version.
const Version = "1"

// ToolName and ToolVersion identify the producer.
const ToolName = "eventparity-engine"

// ToolVersion is set at build time or defaults to the development version.
var ToolVersion = "0.1.0"

// Limitations are fixed statements included in every report.
var Limitations = []string{
	"Version 1 compares classic `payment` operations only. Path payments, account creation, offers, trustlines and Soroban operations are counted by type and not compared one by one.",
	"RPC events do not reproduce every Horizon effect, balance or history record, and this tool does not claim they do.",
	"Parity means: in the ledgers both sources covered, every classic payment matched on all compared fields. It says nothing about ledgers in coverage gaps.",
	"Provider history windows are finite. A ledger a source could not serve is a coverage gap, never evidence of a missing payment.",
	"A finite comparison over a ledger range is evidence about that range. It is not a proof about other ledgers or other operation types.",
}

// Side is one input stream in the report.
type Side struct {
	compare.SideInfo
	StreamSHA256 string `json:"streamSha256"`
}

// Report is the published JSON document.
type Report struct {
	ReportVersion string `json:"reportVersion"`
	Tool          struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"tool"`
	GeneratedAt       string                    `json:"generatedAt"`
	Scope             string                    `json:"scope"`
	Requested         model.Range               `json:"requested"`
	Compared          []model.Range             `json:"compared"`
	Reference         Side                      `json:"reference"`
	Candidate         Side                      `json:"candidate"`
	Verdict           compare.Verdict           `json:"verdict"`
	Matched           int                       `json:"matched"`
	NotComparedInGaps int                       `json:"notComparedInGaps"`
	Differences       []compare.Difference      `json:"differences"`
	Unsupported       []compare.UnsupportedDiff `json:"unsupportedNotCompared"`
	Limitations       []string                  `json:"limitations"`
}

// Inputs carries the raw bytes of each stream so the report can pin exactly what was compared.
type Inputs struct {
	ReferenceBytes, CandidateBytes []byte
	Now                            time.Time
}

// SHA256 hex-encodes a digest.
func SHA256(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Build assembles the report from a comparison result.
func Build(res *compare.Result, in Inputs) *Report {
	r := &Report{ReportVersion: Version, GeneratedAt: in.Now.UTC().Format(time.RFC3339), Scope: model.Scope,
		Requested: res.Requested, Compared: nonNilRanges(res.Compared), Verdict: res.Verdict, Matched: res.Matched,
		NotComparedInGaps: res.NotComparedInGaps, Differences: res.Differences, Unsupported: res.Unsupported,
		Limitations: append([]string(nil), Limitations...)}
	r.Tool.Name, r.Tool.Version = ToolName, ToolVersion
	r.Reference = Side{SideInfo: nonNilSide(res.Reference), StreamSHA256: SHA256(in.ReferenceBytes)}
	r.Candidate = Side{SideInfo: nonNilSide(res.Candidate), StreamSHA256: SHA256(in.CandidateBytes)}
	if r.Differences == nil {
		r.Differences = []compare.Difference{}
	}
	if r.Unsupported == nil {
		r.Unsupported = []compare.UnsupportedDiff{}
	}
	return r
}

func nonNilRanges(r []model.Range) []model.Range {
	if r == nil {
		return []model.Range{}
	}
	return r
}

func nonNilSide(s compare.SideInfo) compare.SideInfo {
	s.Covered = nonNilRanges(s.Covered)
	if s.Gaps == nil {
		s.Gaps = []model.Gap{}
	}
	return s
}

// JSON writes the report as indented JSON.
func (r *Report) JSON(w io.Writer) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// ExitCode maps the verdict to the CLI exit code: 0 parity, 1 differences, 3 inconclusive.
func (r *Report) ExitCode() int {
	switch r.Verdict {
	case compare.Parity:
		return 0
	case compare.Differences:
		return 1
	default:
		return 3
	}
}

func rangesText(rs []model.Range) string {
	if len(rs) == 0 {
		return "none"
	}
	parts := make([]string, len(rs))
	for i, r := range rs {
		parts[i] = fmt.Sprintf("%d-%d", r.From, r.To)
	}
	return strings.Join(parts, ", ")
}

func payText(p model.Payment) string {
	to := p.To
	if p.ToMuxedID != "" {
		to += " (muxed " + p.ToMuxedID + ")"
	}
	return fmt.Sprintf("ledger %d #%d  %s -> %s  %s %s", p.Ledger, p.ApplicationOrder, p.From, to, p.Amount, p.Asset)
}

// Text writes a plain-text rendering of the same report.
func (r *Report) Text(w io.Writer) error {
	var b strings.Builder
	fmt.Fprintf(&b, "EventParity report: ledgers %d-%d (classic payments)\n", r.Requested.From, r.Requested.To)
	fmt.Fprintf(&b, "Verdict: %s\n", strings.ToUpper(string(r.Verdict)))
	fmt.Fprintf(&b, "Reference: %s [%s %s]  covered %s\n", r.Reference.Name, r.Reference.Adapter, r.Reference.Provider, rangesText(r.Reference.Covered))
	fmt.Fprintf(&b, "Candidate: %s [%s %s]  covered %s\n", r.Candidate.Name, r.Candidate.Adapter, r.Candidate.Provider, rangesText(r.Candidate.Covered))
	fmt.Fprintf(&b, "Compared ledgers: %s\n", rangesText(r.Compared))
	fmt.Fprintf(&b, "Matched payments: %d   Differences: %d   Not compared (coverage gaps): %d\n", r.Matched, len(r.Differences), r.NotComparedInGaps)
	for _, side := range []Side{r.Reference, r.Candidate} {
		for _, g := range side.Gaps {
			fmt.Fprintf(&b, "COVERAGE GAP (%s): ledgers %d-%d: %s\n", side.Name, g.From, g.To, g.Reason)
		}
	}
	for _, d := range r.Differences {
		fmt.Fprintf(&b, "\n%s  %s\n  %s\n", strings.ToUpper(string(d.Class)), d.Key, d.Note)
		for _, p := range d.Reference {
			fmt.Fprintf(&b, "  reference: %s\n", payText(p))
		}
		for _, p := range d.Candidate {
			fmt.Fprintf(&b, "  candidate: %s\n", payText(p))
		}
	}
	if len(r.Unsupported) > 0 {
		fmt.Fprintf(&b, "\nOperation types outside version 1 whose counts differ (not compared one by one):\n")
		for _, u := range r.Unsupported {
			fmt.Fprintf(&b, "  %s: reference %d, candidate %d\n", u.OpType, u.Reference, u.Candidate)
		}
	}
	fmt.Fprintf(&b, "\nLimits of this report:\n")
	for _, l := range r.Limitations {
		fmt.Fprintf(&b, "  - %s\n", l)
	}
	_, err := io.WriteString(w, b.String())
	return err
}
