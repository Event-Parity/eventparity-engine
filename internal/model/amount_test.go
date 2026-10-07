package model

import "testing"

func TestFormatAmount(t *testing.T) {
	cases := map[int64]string{
		0:                         "0.0000000",
		1:                         "0.0000001",
		10_000_000:                "1.0000000",
		123_456_789:               "12.3456789",
		9_223_372_036_854_775_807: "922337203685.4775807",
	}
	for in, want := range cases {
		if got := FormatAmount(in); got != want {
			t.Errorf("FormatAmount(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestParseAmount(t *testing.T) {
	good := map[string]int64{
		"0": 0, "1": 10_000_000, "5.0": 50_000_000, "12.3456789": 123_456_789,
		"0.0000001": 1, "922337203685.4775807": 9_223_372_036_854_775_807,
	}
	for in, want := range good {
		got, err := ParseAmount(in)
		if err != nil || got != want {
			t.Errorf("ParseAmount(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	bad := []string{"", ".5", "5.", "-1", "+1", "1e3", "1.00000001", "abc", "1,5", "922337203685.4775808", "99999999999999999999"}
	for _, in := range bad {
		if v, err := ParseAmount(in); err == nil {
			t.Errorf("ParseAmount(%q) = %d, want error", in, v)
		}
	}
}

func TestRoundTripIsExact(t *testing.T) {
	for _, v := range []int64{0, 1, 7, 9_999_999, 10_000_000, 123_456_789, 1<<62 + 12345} {
		s := FormatAmount(v)
		back, err := ParseAmount(s)
		if err != nil || back != v {
			t.Errorf("round trip %d -> %q -> %d (%v)", v, s, back, err)
		}
	}
}

func TestCanonicalAmount(t *testing.T) {
	got, err := CanonicalAmount("5")
	if err != nil || got != "5.0000000" {
		t.Fatalf("got %q, %v", got, err)
	}
}
