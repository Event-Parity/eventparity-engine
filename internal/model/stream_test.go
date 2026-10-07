package model

import (
	"bytes"
	"strings"
	"testing"
)

const (
	gA = "GAUGPIMKP4ZQ22FEUGDSTBJWTGIS5LDB5ZYUSQI33G37DFWLTY34OFAZ"
	gB = "GBYFGKRQVWELTTR3425EQXZP4US5KSHAMJUQNH42AYUI54IJN6M4RQB4"
	h1 = "79cc1ba2a38e512f5046d43d4bfeea84b110eed7531460eb70d6efced16d647b"
)

func header(from, to uint32) string {
	return `{"type":"header","format":"eventparity-stream/v1","adapter":"candidate","provider":"test","requested":{"from":` + itoa(from) + `,"to":` + itoa(to) + `},"createdAt":"2026-10-07T00:00:00Z","scope":"classic-payments"}`
}

func itoa(n uint32) string {
	var b bytes.Buffer
	Encode(&b, n)
	return strings.TrimSpace(b.String())
}

func pay(ledger uint32, amount string) string {
	return `{"type":"payment","ledger":` + itoa(ledger) + `,"applicationOrder":1,"txHash":"` + h1 + `","opIndex":0,"from":"` + gA + `","to":"` + gB + `","asset":{"type":"native"},"amount":"` + amount + `"}`
}

const cov = `{"type":"coverage","covered":[{"from":10,"to":12}],"gaps":[],"complete":true}`

func read(lines ...string) (*Stream, error) {
	return ReadStream(strings.NewReader(strings.Join(lines, "\n") + "\n"))
}

func TestReadValidStream(t *testing.T) {
	s, err := read(header(10, 12), pay(11, "5.0000000"), `{"type":"ledger","ledger":11,"unsupported":{"manage_data":2}}`, cov)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Payments) != 1 || s.Unsupported["manage_data"] != 2 || !s.Coverage.Complete {
		t.Fatalf("unexpected stream: %+v", s)
	}
}

func TestReadStreamFailsClosed(t *testing.T) {
	cases := map[string][]string{
		"no coverage line":            {header(10, 12), pay(11, "5.0000000")},
		"no header":                   {pay(11, "5.0000000"), cov},
		"payment outside coverage":    {header(10, 12), pay(13, "5.0000000"), cov},
		"non-canonical amount":        {header(10, 12), pay(11, "5"), cov},
		"unknown field":               {header(10, 12), strings.Replace(pay(11, "5.0000000"), `"opIndex":0`, `"opIndex":0,"score":9`, 1), cov},
		"unknown line type":           {header(10, 12), `{"type":"weird"}`, cov},
		"data after coverage":         {header(10, 12), cov, pay(11, "5.0000000")},
		"second header":               {header(10, 12), header(10, 12), cov},
		"unaccounted ledgers":         {header(10, 12), `{"type":"coverage","covered":[{"from":10,"to":11}],"gaps":[],"complete":true}`},
		"overlapping coverage":        {header(10, 12), `{"type":"coverage","covered":[{"from":10,"to":12},{"from":12,"to":12}],"gaps":[],"complete":true}`},
		"claims beyond requested":     {header(10, 12), `{"type":"coverage","covered":[{"from":10,"to":13}],"gaps":[],"complete":true}`},
		"gap without a reason":        {header(10, 12), `{"type":"coverage","covered":[{"from":10,"to":11}],"gaps":[{"from":12,"to":12,"reason":""}],"complete":false}`},
		"complete flag contradicts":   {header(10, 12), `{"type":"coverage","covered":[{"from":10,"to":11}],"gaps":[{"from":12,"to":12,"reason":"x"}],"complete":true}`},
		"wrong format":                {strings.Replace(header(10, 12), "v1", "v9", 1), cov},
		"malformed address":           {header(10, 12), strings.Replace(pay(11, "5.0000000"), gB, "GNOPE", 1), cov},
		"credit asset missing issuer": {header(10, 12), strings.Replace(pay(11, "5.0000000"), `{"type":"native"}`, `{"type":"credit","code":"USD"}`, 1), cov},
		"not json":                    {header(10, 12), `{nope`, cov},
		"empty":                       {},
	}
	for name, lines := range cases {
		if _, err := read(lines...); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestReadStreamAcceptsGapsThatAccountForTheRange(t *testing.T) {
	s, err := read(header(10, 12), pay(12, "1.0000000"),
		`{"type":"coverage","covered":[{"from":12,"to":12}],"gaps":[{"from":10,"to":11,"reason":"before provider history"}],"complete":false}`)
	if err != nil || s.Coverage.Complete || len(s.Coverage.Gaps) != 1 {
		t.Fatalf("got %+v, %v", s, err)
	}
}

func TestOversizedLineIsRejected(t *testing.T) {
	big := `{"type":"ledger","ledger":1,"unsupported":{"` + strings.Repeat("x", MaxLineBytes) + `":1}}`
	if _, err := read(header(10, 12), big, cov); err == nil {
		t.Fatal("expected an error for an oversized line")
	}
}

func TestPaymentKeyAndAssetString(t *testing.T) {
	p := Payment{TxHash: h1, OpIndex: 3, Asset: Asset{Type: "credit", Code: "USDX", Issuer: gA}}
	if p.Key() != h1+":3" || p.Asset.String() != "USDX:"+gA || (Asset{Type: "native"}).String() != "native" {
		t.Fatal("unexpected key or asset string")
	}
}
