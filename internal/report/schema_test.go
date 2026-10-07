package report_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func compileSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	f, err := os.Open("../../schema/report.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AddResource("report.v1.schema.json", doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile("report.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func validate(t *testing.T, s *jsonschema.Schema, raw []byte) error {
	t.Helper()
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return s.Validate(v)
}

func TestEveryGoldenReportValidatesAgainstThePublishedSchema(t *testing.T) {
	s := compileSchema(t)
	files, _ := filepath.Glob("../../corpus/reports/*.json")
	if len(files) != 3 {
		t.Fatalf("expected 3 golden reports, found %d", len(files))
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := validate(t, s, b); err != nil {
			t.Errorf("%s: %v", filepath.Base(f), err)
		}
	}
}

func TestTheSchemaRejectsScoresUnknownFieldsAndBadVerdicts(t *testing.T) {
	s := compileSchema(t)
	b, _ := os.ReadFile("../../corpus/reports/report-parity.json")
	var m map[string]any
	json.Unmarshal(b, &m)
	mutate := func(f func(map[string]any)) []byte {
		c := map[string]any{}
		b, _ := json.Marshal(m)
		json.Unmarshal(b, &c)
		f(c)
		out, _ := json.Marshal(c)
		return out
	}
	bad := map[string][]byte{
		"safety score":     mutate(func(c map[string]any) { c["safetyScore"] = 99 }),
		"unknown verdict":  mutate(func(c map[string]any) { c["verdict"] = "all-clear" }),
		"wrong version":    mutate(func(c map[string]any) { c["reportVersion"] = "2" }),
		"short stream sha": mutate(func(c map[string]any) { c["reference"].(map[string]any)["streamSha256"] = "abc" }),
		"no limitations":   mutate(func(c map[string]any) { c["limitations"] = []any{} }),
	}
	for name, raw := range bad {
		if validate(t, s, raw) == nil {
			t.Errorf("%s: expected the schema to reject it", name)
		}
	}
}
