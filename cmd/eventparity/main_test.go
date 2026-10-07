package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	hz   = "../../corpus/testnet-2026-10-07/horizon.jsonl"
	rpc  = "../../corpus/testnet-2026-10-07/rpc.jsonl"
	bad  = "../../corpus/testnet-2026-10-07/candidate-defective.jsonl"
	gapH = "../../corpus/retention-gap-2026-10-07/horizon.jsonl"
	gapR = "../../corpus/retention-gap-2026-10-07/rpc.jsonl"
)

func exec(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestCompareExitCodesFollowTheVerdict(t *testing.T) {
	cases := []struct {
		name, ref, cand string
		code            int
		verdict         string
	}{
		{"parity", hz, rpc, 0, "PARITY"},
		{"differences", hz, bad, 1, "DIFFERENCES"},
		{"inconclusive", gapH, gapR, 3, "INCONCLUSIVE"},
	}
	for _, c := range cases {
		code, out, errs := exec("compare", "--reference", c.ref, "--candidate", c.cand)
		if code != c.code || !strings.Contains(out, "Verdict: "+c.verdict) {
			t.Errorf("%s: exit %d (want %d)\n%s\n%s", c.name, code, c.code, out, errs)
		}
	}
}

func TestDifferencesNameTheExactPayments(t *testing.T) {
	_, out, _ := exec("compare", "--reference", hz, "--candidate", bad)
	for _, want := range []string{"MISSING_IN_CANDIDATE", "DUPLICATED_IN_CANDIDATE", "5.0000000", "2.0000000", "USDX:"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestJSONHTMLAndTextAgreeOnTheVerdictAndCounts(t *testing.T) {
	_, js, _ := exec("compare", "--reference", hz, "--candidate", bad, "--format", "json")
	var rep struct {
		Verdict     string            `json:"verdict"`
		Matched     int               `json:"matched"`
		Differences []json.RawMessage `json:"differences"`
	}
	if err := json.Unmarshal([]byte(js), &rep); err != nil {
		t.Fatal(err)
	}
	_, html, _ := exec("compare", "--reference", hz, "--candidate", bad, "--format", "html")
	_, txt, _ := exec("compare", "--reference", hz, "--candidate", bad)
	if rep.Verdict != "differences" || rep.Matched != 9 || len(rep.Differences) != 2 {
		t.Fatalf("%+v", rep)
	}
	if !strings.Contains(html, "DIFFERENCES") || !strings.Contains(html, "<strong>9</strong>") || !strings.Contains(html, "<strong>2</strong>") {
		t.Error("HTML report disagrees with the JSON report")
	}
	if !strings.Contains(txt, "Matched payments: 9   Differences: 2") {
		t.Error("text report disagrees with the JSON report")
	}
	if strings.Contains(html, "<script") || strings.Contains(html, "http-equiv=\"refresh\"") {
		t.Error("the HTML report must contain no scripts")
	}
}

func TestHTMLEscapesHostileHeaderFields(t *testing.T) {
	b, _ := os.ReadFile(rpc)
	hostile := strings.Replace(string(b), `"provider":"https://soroban-testnet.stellar.org"`, `"provider":"<script>alert(1)</script>"`, 1)
	p := filepath.Join(t.TempDir(), "hostile.jsonl")
	os.WriteFile(p, []byte(hostile), 0o644)
	code, html, errs := exec("compare", "--reference", hz, "--candidate", p, "--format", "html")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	if strings.Contains(html, "<script>alert(1)") || !strings.Contains(html, "&lt;script&gt;alert(1)") {
		t.Error("a hostile provider string was not escaped")
	}
}

func TestCompareRefusesInvalidInputWithExitCodeTwo(t *testing.T) {
	dir := t.TempDir()
	noCoverage := filepath.Join(dir, "nocov.jsonl")
	b, _ := os.ReadFile(hz)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	os.WriteFile(noCoverage, []byte(strings.Join(lines[:len(lines)-1], "\n")+"\n"), 0o644)
	for name, args := range map[string][]string{
		"no coverage line": {"compare", "--reference", hz, "--candidate", noCoverage},
		"missing file":     {"compare", "--reference", hz, "--candidate", filepath.Join(dir, "none")},
		"different ranges": {"compare", "--reference", hz, "--candidate", gapR},
		"bad format":       {"compare", "--reference", hz, "--candidate", rpc, "--format", "xml"},
		"missing flags":    {"compare", "--reference", hz},
	} {
		if code, _, _ := exec(args...); code != 2 {
			t.Errorf("%s: exit %d, want 2", name, code)
		}
	}
}

func TestMutateBuildsADefectiveCandidateAndRefusesUnknownKeys(t *testing.T) {
	out := filepath.Join(t.TempDir(), "c.jsonl")
	key := "7024ffc7195b4b2295b075375c42419e39c37a902fd9b5141e487657fb4b2ae6:0"
	if code, _, e := exec("mutate", "--in", hz, "--out", out, "--drop", key); code != 0 {
		t.Fatalf("exit %d %s", code, e)
	}
	if code, o, _ := exec("compare", "--reference", hz, "--candidate", out); code != 1 || !strings.Contains(o, "MISSING_IN_CANDIDATE") {
		t.Fatalf("exit %d\n%s", code, o)
	}
	if code, _, _ := exec("mutate", "--in", hz, "--out", filepath.Join(t.TempDir(), "x"), "--drop", "nope:0"); code != 2 {
		t.Error("an unknown key must be refused")
	}
	if code, _, _ := exec("mutate", "--in", hz, "--out", out, "--drop", key); code != 2 {
		t.Error("mutate must not overwrite an existing file")
	}
}

func TestFetchEndToEndAgainstALocalHorizonAndResumeSafety(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			fmt.Fprint(w, `{"history_elder_ledger":1,"history_latest_ledger":100,"network_passphrase":"Test SDF Network ; September 2015"}`)
			return
		}
		fmt.Fprint(w, `{"_links":{"next":{"href":""}},"_embedded":{"records":[]}}`)
	}))
	defer srv.Close()
	out := filepath.Join(t.TempDir(), "s.jsonl")
	code, so, se := exec("fetch", "--source", "horizon", "--url", srv.URL, "--allow-http", "--from", "10", "--to", "12", "--out", out)
	if code != 0 || !strings.Contains(so, "complete=true") {
		t.Fatalf("exit %d\n%s\n%s", code, so, se)
	}
	if code, _, _ := exec("compare", "--reference", out, "--candidate", out); code != 0 {
		t.Errorf("an empty but fully covered stream compared with itself should be parity, exit %d", code)
	}
	if code, _, e := exec("fetch", "--source", "horizon", "--url", srv.URL, "--allow-http", "--from", "10", "--to", "12", "--out", out); code != 1 || !strings.Contains(e, "already complete") {
		t.Errorf("rerunning over a finished stream must fail: exit %d %s", code, e)
	}
	// A range beyond the provider's latest ledger is reported as a gap and exit code 3.
	out2 := filepath.Join(t.TempDir(), "g.jsonl")
	code, so, _ = exec("fetch", "--source", "horizon", "--url", srv.URL, "--allow-http", "--from", "90", "--to", "110", "--out", out2)
	if code != 3 || !strings.Contains(so, "after the provider's latest ledger (100)") {
		t.Errorf("exit %d\n%s", code, so)
	}
}

func TestFetchUsageErrors(t *testing.T) {
	out := filepath.Join(t.TempDir(), "s.jsonl")
	for name, args := range map[string][]string{
		"http without flag": {"fetch", "--source", "horizon", "--url", "http://example.com", "--from", "1", "--to", "2", "--out", out},
		"unknown source":    {"fetch", "--source", "grpc", "--url", "https://example.com", "--from", "1", "--to", "2", "--out", out},
		"inverted range":    {"fetch", "--source", "rpc", "--url", "https://example.com", "--from", "5", "--to", "2", "--out", out},
		"no args":           {},
		"unknown command":   {"frobnicate"},
	} {
		if code, _, _ := exec(args...); code != 2 {
			t.Errorf("%s: exit %d, want 2", name, code)
		}
	}
	if code, o, _ := exec("version"); code != 0 || !strings.HasPrefix(o, "eventparity-engine ") {
		t.Errorf("version: %d %q", code, o)
	}
}
