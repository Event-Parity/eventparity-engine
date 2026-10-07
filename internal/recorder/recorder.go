// Package recorder records real provider responses to disk and replays them, so adapters can be tested
// deterministically against genuine data without network access.
package recorder

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Exchange is one recorded request/response pair. Response is kept as JSON so the files are readable.
type Exchange struct {
	Method   string          `json:"method"`
	URL      string          `json:"url"`
	Request  json.RawMessage `json:"request,omitempty"`
	Status   int             `json:"status"`
	Response json.RawMessage `json:"response"`
	// Trimmed lists response fields removed before saving because no adapter reads them (size only).
	Trimmed []string `json:"trimmed,omitempty"`
}

// Key fingerprints a request.
func Key(method, url string, body []byte) string {
	var c bytes.Buffer
	if len(body) > 0 && json.Compact(&c, body) == nil {
		body = c.Bytes()
	}
	h := sha256.Sum256([]byte(method + "\n" + url + "\n" + string(body)))
	return hex.EncodeToString(h[:8])
}

// Recorder wraps a RoundTripper and saves every exchange under Dir.
type Recorder struct {
	Dir  string
	Next http.RoundTripper
	// TrimFields lists fields to remove before saving; see trim().
	TrimFields []string
}

func (r *Recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	var reqBody []byte
	if req.Body != nil {
		reqBody, _ = io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(reqBody))
	}
	resp, err := r.Next.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	saved, trimmed := trim(body, r.TrimFields)
	ex := Exchange{Method: req.Method, URL: req.URL.String(), Status: resp.StatusCode, Trimmed: trimmed}
	if len(reqBody) > 0 {
		ex.Request = json.RawMessage(reqBody)
	}
	if json.Valid(saved) {
		ex.Response = saved
	} else {
		q, _ := json.Marshal(string(saved))
		ex.Response = q
	}
	out, _ := json.MarshalIndent(ex, "", " ")
	if err := os.MkdirAll(r.Dir, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(r.Dir, Key(req.Method, req.URL.String(), reqBody)+".json"), append(out, '\n'), 0o644); err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(body)) // the caller sees the untrimmed real response
	return resp, nil
}

// trim removes fields before saving. A name without a dot is removed at any depth. A dotted path such as
// "result.items[].links" removes that field from exactly that location, where "[]" iterates an array. It returns the
// names that were actually removed.
func trim(body []byte, fields []string) ([]byte, []string) {
	if len(fields) == 0 {
		return body, nil
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return body, nil
	}
	removed := map[string]bool{}
	anywhere := map[string]bool{}
	for _, f := range fields {
		if strings.Contains(f, ".") {
			if cutPath(v, strings.Split(f, ".")) {
				removed[f] = true
			}
		} else {
			anywhere[f] = true
		}
	}
	var walk func(any)
	walk = func(n any) {
		switch t := n.(type) {
		case map[string]any:
			for k := range t {
				if anywhere[k] {
					delete(t, k)
					removed[k] = true
				}
			}
			for _, c := range t {
				walk(c)
			}
		case []any:
			for _, c := range t {
				walk(c)
			}
		}
	}
	walk(v)
	out, _ := json.Marshal(v)
	names := make([]string, 0, len(removed))
	for k := range removed {
		names = append(names, k)
	}
	sort.Strings(names)
	return out, names
}

func cutPath(n any, parts []string) bool {
	if len(parts) == 0 {
		return false
	}
	head, rest := parts[0], parts[1:]
	iterate := strings.HasSuffix(head, "[]")
	head = strings.TrimSuffix(head, "[]")
	m, ok := n.(map[string]any)
	if !ok {
		return false
	}
	child, ok := m[head]
	if !ok {
		return false
	}
	if len(rest) == 0 {
		if iterate {
			return false
		}
		delete(m, head)
		return true
	}
	if iterate {
		arr, ok := child.([]any)
		if !ok {
			return false
		}
		any := false
		for _, e := range arr {
			if cutPath(e, rest) {
				any = true
			}
		}
		return any
	}
	return cutPath(child, rest)
}

// Replayer serves recorded exchanges. A request with no recording is an error, never a silent empty answer.
type Replayer struct {
	byKey map[string]Exchange
}

// NewReplayer loads every *.json exchange in dir.
func NewReplayer(dir string) (*Replayer, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	rp := &Replayer{byKey: map[string]Exchange{}}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var ex Exchange
		if err := json.Unmarshal(b, &ex); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		rp.byKey[Key(ex.Method, ex.URL, ex.Request)] = ex
	}
	if len(rp.byKey) == 0 {
		return nil, fmt.Errorf("no recorded exchanges in %s", dir)
	}
	return rp, nil
}

func (rp *Replayer) Len() int { return len(rp.byKey) }

func (rp *Replayer) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
	}
	ex, ok := rp.byKey[Key(req.Method, req.URL.String(), body)]
	if !ok {
		return nil, fmt.Errorf("replay: no recording for %s %s %s", req.Method, req.URL.String(), strings.TrimSpace(string(body)))
	}
	payload := []byte(ex.Response)
	var s string
	if json.Unmarshal(payload, &s) == nil && !json.Valid([]byte(s)) {
		payload = []byte(s)
	}
	return &http.Response{
		StatusCode: ex.Status,
		Status:     fmt.Sprintf("%d", ex.Status),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(payload)),
		Request:    req,
	}, nil
}
