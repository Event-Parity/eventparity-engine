package report

import (
	"html/template"
	"io"
	"strings"

	"github.com/Anasabubakar/eventparity-engine/internal/model"
)

var funcs = template.FuncMap{
	"ranges": rangesText,
	"pay":    payText,
	"upper":  strings.ToUpper,
	"gaps": func(r *Report) []gapRow {
		var out []gapRow
		for _, s := range []Side{r.Reference, r.Candidate} {
			for _, g := range s.Gaps {
				out = append(out, gapRow{Side: s.Name, Range: g.Range, Reason: g.Reason})
			}
		}
		return out
	},
}

type gapRow struct {
	Side   string
	Range  model.Range
	Reason string
}

var page = template.Must(template.New("report").Funcs(funcs).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>EventParity report, ledgers {{.Requested.From}}-{{.Requested.To}}</title>
<style>
:root{color-scheme:light dark;--bg:#fbfaf7;--ink:#1d2433;--muted:#5b6475;--line:#d9d3c4;--ok:#17693f;--bad:#a3231b;--warn:#7a4f00}
@media (prefers-color-scheme:dark){:root{--bg:#14181f;--ink:#e8eaf0;--muted:#a2abbb;--line:#333c4b;--ok:#7fd6a3;--bad:#ff9d94;--warn:#f1c76b}}
body{margin:0 auto;max-width:60rem;padding:1rem;background:var(--bg);color:var(--ink);font:16px/1.5 system-ui,sans-serif}
code,pre{font-family:ui-monospace,monospace;font-size:.85em;overflow-wrap:anywhere}
.verdict{display:inline-block;padding:.2rem .7rem;border:2px solid currentColor;border-radius:4px;font-weight:700}
.parity{color:var(--ok)}.differences{color:var(--bad)}.inconclusive{color:var(--warn)}
table{border-collapse:collapse;width:100%}th,td{border-bottom:1px solid var(--line);padding:.4rem;text-align:left;vertical-align:top}
.muted{color:var(--muted)}.diff{border:1px solid var(--line);border-left:6px solid var(--bad);padding:.5rem 1rem;margin:1rem 0;border-radius:6px}
.gap{border-left-color:var(--warn)}.cols{display:grid;grid-template-columns:1fr 1fr;gap:1rem}
@media (max-width:40rem){.cols{grid-template-columns:1fr}}
</style></head><body>
<h1>EventParity report</h1>
<p>Classic payments, ledgers {{.Requested.From}}-{{.Requested.To}}. Generated {{.GeneratedAt}} by {{.Tool.Name}} {{.Tool.Version}}.</p>
<p>Verdict: <span class="verdict {{.Verdict}}">{{upper (printf "%s" .Verdict)}}</span></p>
{{if eq (printf "%s" .Verdict) "inconclusive"}}<p>The comparison is <strong>inconclusive</strong>: at least one source did not cover part of the range. This is not a pass.</p>{{end}}
<h2>Sources</h2>
<table><tr><th></th><th>Name</th><th>Adapter</th><th>Provider</th><th>Covered</th><th>Payments</th><th>Stream SHA-256</th></tr>
<tr><th>Reference</th><td>{{.Reference.Name}}</td><td>{{.Reference.Adapter}}</td><td>{{.Reference.Provider}}</td><td>{{ranges .Reference.Covered}}</td><td>{{.Reference.Payments}}</td><td><code>{{.Reference.StreamSHA256}}</code></td></tr>
<tr><th>Candidate</th><td>{{.Candidate.Name}}</td><td>{{.Candidate.Adapter}}</td><td>{{.Candidate.Provider}}</td><td>{{ranges .Candidate.Covered}}</td><td>{{.Candidate.Payments}}</td><td><code>{{.Candidate.StreamSHA256}}</code></td></tr></table>
<p>Compared ledgers (covered by both): {{ranges .Compared}}. Matched payments: <strong>{{.Matched}}</strong>. Differences: <strong>{{len .Differences}}</strong>. Not compared because of coverage gaps: <strong>{{.NotComparedInGaps}}</strong>.</p>
{{range gaps .}}<div class="diff gap"><strong>Coverage gap</strong> in {{.Side}}: ledgers {{.Range.From}}-{{.Range.To}}. {{.Reason}}. These ledgers were not compared; this is not evidence of missing payments.</div>{{end}}
<h2>Differences</h2>
{{if not .Differences}}<p class="muted">None in the compared ledgers.</p>{{end}}
{{range .Differences}}<div class="diff"><strong>{{upper (printf "%s" .Class)}}</strong> <code>{{.Key}}</code> (ledger {{.Ledger}})<p>{{.Note}}</p>
<div class="cols"><div><strong>Reference</strong>{{range .Reference}}<pre>{{pay .}}</pre>{{else}}<p class="muted">not present</p>{{end}}</div>
<div><strong>Candidate</strong>{{range .Candidate}}<pre>{{pay .}}</pre>{{else}}<p class="muted">not present</p>{{end}}</div></div></div>{{end}}
{{if .Unsupported}}<h2>Operation types outside version 1</h2><p>Counts differ for these types in compared ledgers. They are disclosed, not compared one by one, and do not change the verdict.</p>
<table><tr><th>Type</th><th>Reference</th><th>Candidate</th></tr>{{range .Unsupported}}<tr><td>{{.OpType}}</td><td>{{.Reference}}</td><td>{{.Candidate}}</td></tr>{{end}}</table>{{end}}
<h2>Limits of this report</h2><ul>{{range .Limitations}}<li>{{.}}</li>{{end}}</ul>
</body></html>
`))

// HTML writes a self-contained page (no scripts, no external requests). All values are escaped by html/template.
func (r *Report) HTML(w io.Writer) error { return page.Execute(w, r) }
