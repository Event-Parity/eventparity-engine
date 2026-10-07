package recorder

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRecordThenReplayServesTheSameBytesAndRefusesUnknownRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"echo":` + string(b) + `,"heavy":"xxxx","keep":{"heavy":[1,2]}}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	rec := &http.Client{Transport: &Recorder{Dir: dir, Next: http.DefaultTransport, TrimFields: []string{"heavy"}}}
	resp, err := rec.Post(srv.URL+"/rpc", "application/json", strings.NewReader(`{ "method": "getHealth" }`))
	if err != nil {
		t.Fatal(err)
	}
	live, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(live), `"heavy":"xxxx"`) {
		t.Fatalf("the caller must see the untrimmed live response, got %s", live)
	}

	rp, err := NewReplayer(dir)
	if err != nil || rp.Len() != 1 {
		t.Fatalf("replayer: %v len=%d", err, rp.Len())
	}
	play := &http.Client{Transport: rp}
	// A differently formatted but equivalent request body still matches.
	r2, err := play.Post(srv.URL+"/rpc", "application/json", strings.NewReader(`{"method":"getHealth"}`))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(r2.Body)
	if strings.Contains(string(got), "heavy") {
		t.Fatalf("trimmed fields must not be replayed: %s", got)
	}
	if !strings.Contains(string(got), `"echo"`) {
		t.Fatalf("unexpected replay: %s", got)
	}
	if _, err := play.Post(srv.URL+"/rpc", "application/json", strings.NewReader(`{"method":"other"}`)); err == nil {
		t.Fatal("an unrecorded request must fail, not return an empty answer")
	}
}

func TestReplayerRejectsAnEmptyDirectory(t *testing.T) {
	if _, err := NewReplayer(t.TempDir()); err == nil {
		t.Fatal("expected an error")
	}
}

func TestTrimNamesOnlyRemovedFields(t *testing.T) {
	out, names := trim([]byte(`{"a":1,"b":{"a":2,"c":[{"a":3}]}}`), []string{"a", "zzz"})
	if string(out) != `{"b":{"c":[{}]}}` || len(names) != 1 || names[0] != "a" {
		t.Fatalf("%s %v", out, names)
	}
}

func TestTrimPathsRemoveOnlyTheNamedLocation(t *testing.T) {
	in := `{"_links":{"next":"keep"},"_embedded":{"records":[{"id":1,"_links":{"self":"x"}},{"id":2,"_links":{"self":"y"}}]}}`
	out, names := trim([]byte(in), []string{"_embedded.records[]._links", "result.missing"})
	want := `{"_embedded":{"records":[{"id":1},{"id":2}]},"_links":{"next":"keep"}}`
	if string(out) != want || len(names) != 1 || names[0] != "_embedded.records[]._links" {
		t.Fatalf("%s %v", out, names)
	}
}
