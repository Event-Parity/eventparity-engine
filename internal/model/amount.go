// Package model defines the canonical observation, stream and coverage types shared by every adapter and the comparator.
package model

import (
	"fmt"
	"math"
	"strings"
)

const stroopsPerUnit = 10_000_000

// FormatAmount renders stroops (int64) as the canonical 7-decimal string, exactly, with no floating point.
func FormatAmount(stroops int64) string {
	neg := stroops < 0
	u := uint64(stroops)
	if neg {
		u = -u
	}
	s := fmt.Sprintf("%d.%07d", u/stroopsPerUnit, u%stroopsPerUnit)
	if neg {
		return "-" + s
	}
	return s
}

// ParseAmount parses a decimal amount with at most 7 fractional digits into stroops. It accepts "5", "5.0",
// "5.0000000" and rejects signs, exponents, empty fractions, more than 7 decimals and overflow.
func ParseAmount(s string) (int64, error) {
	if s == "" {
		return 0, fmt.Errorf("empty amount")
	}
	whole, frac, hasDot := strings.Cut(s, ".")
	if whole == "" || (hasDot && frac == "") {
		return 0, fmt.Errorf("malformed amount %q", s)
	}
	if len(frac) > 7 {
		return 0, fmt.Errorf("amount %q has more than 7 decimal places", s)
	}
	var w uint64
	for _, c := range whole {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("malformed amount %q", s)
		}
		d := uint64(c - '0')
		if w > (math.MaxInt64-d)/10 {
			return 0, fmt.Errorf("amount %q overflows", s)
		}
		w = w*10 + d
	}
	frac += strings.Repeat("0", 7-len(frac))
	var f uint64
	for _, c := range frac {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("malformed amount %q", s)
		}
		f = f*10 + uint64(c-'0')
	}
	if w > (math.MaxInt64-f)/stroopsPerUnit {
		return 0, fmt.Errorf("amount %q overflows", s)
	}
	return int64(w*stroopsPerUnit + f), nil
}

// CanonicalAmount normalises a decimal string to the 7-decimal canonical form.
func CanonicalAmount(s string) (string, error) {
	v, err := ParseAmount(s)
	if err != nil {
		return "", err
	}
	return FormatAmount(v), nil
}
