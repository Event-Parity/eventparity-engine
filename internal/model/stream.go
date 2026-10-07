package model

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// MaxLineBytes bounds a single JSON line so a hostile candidate file cannot exhaust memory.
const MaxLineBytes = 1 << 20

// Encode writes one JSON line.
func Encode(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}

// ReadStream parses and validates a stream. It fails closed: a missing header, a missing coverage line
// (an incomplete or truncated stream), an invalid observation or an unknown line type is an error, because a
// stream whose coverage is unknown cannot be compared honestly.
func ReadStream(r io.Reader) (*Stream, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), MaxLineBytes)
	s := &Stream{Unsupported: map[string]int{}}
	line := 0
	var haveHeader, haveCoverage bool
	for sc.Scan() {
		line++
		raw := bytes.TrimSpace(sc.Bytes())
		if len(raw) == 0 {
			continue
		}
		if haveCoverage {
			return nil, fmt.Errorf("line %d: data after the coverage line", line)
		}
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		if !haveHeader && probe.Type != "header" {
			return nil, fmt.Errorf("line %d: stream must start with a header line", line)
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		switch probe.Type {
		case "header":
			if haveHeader {
				return nil, fmt.Errorf("line %d: second header", line)
			}
			if err := dec.Decode(&s.Header); err != nil {
				return nil, fmt.Errorf("line %d: header: %w", line, err)
			}
			if s.Header.Format != StreamFormat {
				return nil, fmt.Errorf("unsupported stream format %q (want %q)", s.Header.Format, StreamFormat)
			}
			if s.Header.Scope != Scope {
				return nil, fmt.Errorf("unsupported scope %q (want %q)", s.Header.Scope, Scope)
			}
			haveHeader = true
		case "payment":
			var p struct {
				Type string `json:"type"`
				Payment
			}
			if err := dec.Decode(&p); err != nil {
				return nil, fmt.Errorf("line %d: payment: %w", line, err)
			}
			if err := p.Payment.Validate(); err != nil {
				return nil, fmt.Errorf("line %d: %w", line, err)
			}
			s.Payments = append(s.Payments, p.Payment)
		case "ledger":
			var n LedgerNote
			if err := dec.Decode(&n); err != nil {
				return nil, fmt.Errorf("line %d: ledger note: %w", line, err)
			}
			for k, v := range n.Unsupported {
				if v < 0 {
					return nil, fmt.Errorf("line %d: negative unsupported count", line)
				}
				s.Unsupported[k] += v
			}
		case "coverage":
			if err := dec.Decode(&s.Coverage); err != nil {
				return nil, fmt.Errorf("line %d: coverage: %w", line, err)
			}
			if err := validateCoverage(s.Header.Requested, s.Coverage); err != nil {
				return nil, fmt.Errorf("line %d: %w", line, err)
			}
			haveCoverage = true
		default:
			return nil, fmt.Errorf("line %d: unknown line type %q", line, probe.Type)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if !haveHeader {
		return nil, fmt.Errorf("empty stream")
	}
	if !haveCoverage {
		return nil, fmt.Errorf("stream has no coverage line: it is incomplete or does not declare which ledgers it covers")
	}
	for _, p := range s.Payments {
		if !inAny(s.Coverage.Covered, p.Ledger) {
			return nil, fmt.Errorf("payment %s is in ledger %d, outside the declared coverage", p.Key(), p.Ledger)
		}
	}
	return s, nil
}

func inAny(rs []Range, l uint32) bool {
	for _, r := range rs {
		if r.Contains(l) {
			return true
		}
	}
	return false
}

// validateCoverage checks that covered ranges and gaps are ordered, disjoint, inside the requested range, and
// together account for it exactly. A stream cannot claim more than it requested or leave ledgers unaccounted for.
func validateCoverage(req Range, c Coverage) error {
	var all []Range
	for _, r := range c.Covered {
		all = append(all, r)
	}
	for _, g := range c.Gaps {
		if g.Reason == "" {
			return fmt.Errorf("a gap needs a reason")
		}
		all = append(all, g.Range)
	}
	next := req.From
	// Merge in order of From without importing sort for a tiny slice.
	for len(all) > 0 {
		idx := -1
		for i, r := range all {
			if r.To < r.From {
				return fmt.Errorf("range %d-%d is inverted", r.From, r.To)
			}
			if idx == -1 || r.From < all[idx].From {
				idx = i
			}
		}
		r := all[idx]
		all = append(all[:idx], all[idx+1:]...)
		if r.From != next {
			return fmt.Errorf("coverage and gaps must account for every requested ledger exactly once (expected %d, found %d)", next, r.From)
		}
		next = r.To + 1
	}
	if next != req.To+1 {
		return fmt.Errorf("coverage and gaps end at %d but the requested range ends at %d", next-1, req.To)
	}
	if c.Complete != (len(c.Gaps) == 0) {
		return fmt.Errorf("complete must be true exactly when there are no gaps")
	}
	return nil
}
