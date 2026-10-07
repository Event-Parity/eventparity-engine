// Package compare pairs observations from a reference stream and a candidate stream and classifies every difference.
//
// It never reads the network. Its inputs are fully validated streams, each with declared coverage, so a ledger that a
// source did not cover is reported as a coverage gap and is never counted as a missing payment.
package compare

import (
	"fmt"
	"sort"

	"github.com/Anasabubakar/eventparity-engine/internal/model"
)

// Class is the kind of difference found.
type Class string

const (
	MissingInCandidate    Class = "missing_in_candidate"
	MissingInReference    Class = "missing_in_reference"
	DuplicatedInCandidate Class = "duplicated_in_candidate"
	DuplicatedInReference Class = "duplicated_in_reference"
	Reinterpreted         Class = "reinterpreted"
)

// Verdict is the overall outcome. "parity" is only possible when both sides covered the whole requested range.
type Verdict string

const (
	Parity       Verdict = "parity"
	Differences  Verdict = "differences"
	Inconclusive Verdict = "inconclusive"
)

// Difference is one finding with the exact source evidence on each side.
type Difference struct {
	Class     Class           `json:"class"`
	Key       string          `json:"key"`
	Ledger    uint32          `json:"ledger"`
	Reference []model.Payment `json:"reference,omitempty"`
	Candidate []model.Payment `json:"candidate,omitempty"`
	Fields    []string        `json:"fields,omitempty"` // for reinterpreted
	Note      string          `json:"note"`
}

// UnsupportedDiff notes an operation type whose successful-operation count differs between sources in compared ledgers.
// These operations are outside version 1 and are reported for disclosure; they do not change the verdict.
type UnsupportedDiff struct {
	OpType    string `json:"opType"`
	Reference int    `json:"reference"`
	Candidate int    `json:"candidate"`
}

// SideInfo describes one input stream.
type SideInfo struct {
	Name     string        `json:"name"`
	Adapter  string        `json:"adapter"`
	Provider string        `json:"provider"`
	Network  string        `json:"network,omitempty"`
	Covered  []model.Range `json:"covered"`
	Gaps     []model.Gap   `json:"gaps"`
	Payments int           `json:"payments"`
}

// Result is the full comparison.
type Result struct {
	Requested         model.Range       `json:"requested"`
	Compared          []model.Range     `json:"compared"` // ledgers both sides covered
	Reference         SideInfo          `json:"reference"`
	Candidate         SideInfo          `json:"candidate"`
	Verdict           Verdict           `json:"verdict"`
	Matched           int               `json:"matched"`
	NotComparedInGaps int               `json:"notComparedInGaps"` // observations in a ledger only one side covered
	Differences       []Difference      `json:"differences"`
	Unsupported       []UnsupportedDiff `json:"unsupportedNotCompared"`
}

// Input names the two sides.
type Input struct {
	Reference, Candidate         *model.Stream
	ReferenceName, CandidateName string
}

// Compare classifies every difference between the reference and the candidate.
func Compare(in Input) (*Result, error) {
	ref, cand := in.Reference, in.Candidate
	if ref == nil || cand == nil {
		return nil, fmt.Errorf("both streams are required")
	}
	if ref.Header.Requested != cand.Header.Requested {
		return nil, fmt.Errorf("streams were requested for different ledger ranges (%d-%d vs %d-%d); compare equal ranges",
			ref.Header.Requested.From, ref.Header.Requested.To, cand.Header.Requested.From, cand.Header.Requested.To)
	}
	if ref.Header.Network != "" && cand.Header.Network != "" && ref.Header.Network != cand.Header.Network {
		return nil, fmt.Errorf("streams are from different networks (%q vs %q)", ref.Header.Network, cand.Header.Network)
	}

	res := &Result{
		Requested: ref.Header.Requested,
		Reference: side(in.ReferenceName, ref),
		Candidate: side(in.CandidateName, cand),
		Compared:  intersect(ref.Coverage.Covered, cand.Coverage.Covered),
	}

	refBy, candBy := group(ref.Payments), group(cand.Payments)
	keys := map[string]struct{}{}
	for k := range refBy {
		keys[k] = struct{}{}
	}
	for k := range candBy {
		keys[k] = struct{}{}
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Slice(sorted, func(i, j int) bool { return less(first(refBy, candBy, sorted[i]), first(refBy, candBy, sorted[j])) })

	for _, k := range sorted {
		rs, cs := refBy[k], candBy[k]
		switch {
		case len(rs) > 0 && len(cs) == 0:
			if covers(cand.Coverage.Covered, rs[0].Ledger) {
				res.Differences = append(res.Differences, Difference{Class: MissingInCandidate, Key: k, Ledger: rs[0].Ledger, Reference: rs,
					Note: "The reference shows this payment and the candidate, which covered this ledger, does not."})
			} else {
				res.NotComparedInGaps++
			}
		case len(cs) > 0 && len(rs) == 0:
			if covers(ref.Coverage.Covered, cs[0].Ledger) {
				res.Differences = append(res.Differences, Difference{Class: MissingInReference, Key: k, Ledger: cs[0].Ledger, Candidate: cs,
					Note: "The candidate shows this payment and the reference, which covered this ledger, does not."})
			} else {
				res.NotComparedInGaps++
			}
		default:
			if len(rs) > 1 {
				res.Differences = append(res.Differences, Difference{Class: DuplicatedInReference, Key: k, Ledger: rs[0].Ledger, Reference: rs,
					Note: fmt.Sprintf("The reference contains this operation %d times; a source should list each operation once.", len(rs))})
			}
			if len(cs) > 1 {
				res.Differences = append(res.Differences, Difference{Class: DuplicatedInCandidate, Key: k, Ledger: cs[0].Ledger, Candidate: cs,
					Note: fmt.Sprintf("The candidate contains this operation %d times.", len(cs))})
			}
			if f := fieldDiffs(rs[0], cs[0]); len(f) > 0 {
				res.Differences = append(res.Differences, Difference{Class: Reinterpreted, Key: k, Ledger: rs[0].Ledger, Reference: rs[:1], Candidate: cs[:1], Fields: f,
					Note: "Both sources list this operation but disagree on: " + join(f) + "."})
			} else {
				res.Matched++
			}
		}
	}
	sort.SliceStable(res.Differences, func(i, j int) bool {
		a, b := res.Differences[i], res.Differences[j]
		if a.Ledger != b.Ledger {
			return a.Ledger < b.Ledger
		}
		if a.Key != b.Key {
			return a.Key < b.Key
		}
		return a.Class < b.Class
	})

	res.Unsupported = unsupportedDiffs(ref, cand, res.Compared)

	gaps := len(ref.Coverage.Gaps) + len(cand.Coverage.Gaps)
	switch {
	case len(res.Differences) > 0:
		res.Verdict = Differences
	case gaps > 0:
		res.Verdict = Inconclusive
	default:
		res.Verdict = Parity
	}
	return res, nil
}

func side(name string, s *model.Stream) SideInfo {
	return SideInfo{Name: name, Adapter: s.Header.Adapter, Provider: s.Header.Provider, Network: s.Header.Network,
		Covered: s.Coverage.Covered, Gaps: s.Coverage.Gaps, Payments: len(s.Payments)}
}

func group(ps []model.Payment) map[string][]model.Payment {
	m := map[string][]model.Payment{}
	for _, p := range ps {
		m[p.Key()] = append(m[p.Key()], p)
	}
	return m
}

func first(a, b map[string][]model.Payment, k string) model.Payment {
	if v := a[k]; len(v) > 0 {
		return v[0]
	}
	return b[k][0]
}

func less(a, b model.Payment) bool {
	if a.Ledger != b.Ledger {
		return a.Ledger < b.Ledger
	}
	if a.ApplicationOrder != b.ApplicationOrder {
		return a.ApplicationOrder < b.ApplicationOrder
	}
	if a.TxHash != b.TxHash {
		return a.TxHash < b.TxHash
	}
	return a.OpIndex < b.OpIndex
}

func covers(rs []model.Range, l uint32) bool {
	for _, r := range rs {
		if r.Contains(l) {
			return true
		}
	}
	return false
}

func intersect(a, b []model.Range) []model.Range {
	var out []model.Range
	for _, x := range a {
		for _, y := range b {
			lo, hi := max(x.From, y.From), min(x.To, y.To)
			if lo <= hi {
				out = append(out, model.Range{From: lo, To: hi})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].From < out[j].From })
	return out
}

func fieldDiffs(r, c model.Payment) []string {
	var f []string
	if r.Ledger != c.Ledger {
		f = append(f, "ledger")
	}
	if r.ApplicationOrder != c.ApplicationOrder {
		f = append(f, "applicationOrder")
	}
	if r.From != c.From {
		f = append(f, "from")
	}
	if r.FromMuxedID != c.FromMuxedID {
		f = append(f, "fromMuxedId")
	}
	if r.To != c.To {
		f = append(f, "to")
	}
	if r.ToMuxedID != c.ToMuxedID {
		f = append(f, "toMuxedId")
	}
	if r.Asset != c.Asset {
		f = append(f, "asset")
	}
	if r.Amount != c.Amount {
		f = append(f, "amount")
	}
	return f
}

func join(s []string) string {
	out := ""
	for i, v := range s {
		if i > 0 {
			out += ", "
		}
		out += v
	}
	return out
}

func unsupportedDiffs(ref, cand *model.Stream, compared []model.Range) []UnsupportedDiff {
	sum := func(s *model.Stream) map[string]int {
		m := map[string]int{}
		for l, counts := range s.UnsupportedByLedger {
			if covers(compared, l) {
				for k, v := range counts {
					m[k] += v
				}
			}
		}
		return m
	}
	r, c := sum(ref), sum(cand)
	types := map[string]struct{}{}
	for k := range r {
		types[k] = struct{}{}
	}
	for k := range c {
		types[k] = struct{}{}
	}
	var out []UnsupportedDiff
	for _, t := range model.SortedTypes(toIntMap(types)) {
		if r[t] != c[t] {
			out = append(out, UnsupportedDiff{OpType: t, Reference: r[t], Candidate: c[t]})
		}
	}
	return out
}

func toIntMap(s map[string]struct{}) map[string]int {
	m := map[string]int{}
	for k := range s {
		m[k] = 0
	}
	return m
}
