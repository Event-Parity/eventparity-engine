// Command eventparity fetches classic payments from Horizon or Stellar RPC, compares two streams and writes a report.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"time"

	"github.com/Anasabubakar/eventparity-engine/internal/compare"
	"github.com/Anasabubakar/eventparity-engine/internal/horizon"
	"github.com/Anasabubakar/eventparity-engine/internal/ingest"
	"github.com/Anasabubakar/eventparity-engine/internal/model"
	"github.com/Anasabubakar/eventparity-engine/internal/report"
	"github.com/Anasabubakar/eventparity-engine/internal/rpcsrc"
	"github.com/Anasabubakar/eventparity-engine/internal/source"
)

const usage = `eventparity: payment-ingestion migration assurance for Stellar (classic payments, version 1)

Usage:
  eventparity fetch   --source horizon|rpc --url URL --from N --to M --out FILE
  eventparity compare --reference FILE --candidate FILE [--format text|json|html] [--out FILE]
  eventparity mutate  --in FILE --out FILE [--drop KEY]... [--duplicate KEY]...
  eventparity version

Exit codes:
  fetch    0 complete, 3 finished with coverage gaps, 1 error, 2 usage
  compare  0 parity, 1 differences, 3 inconclusive (coverage gaps), 2 usage or invalid input
`

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(s string) error { *l = append(*l, s); return nil }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "fetch":
		return fetch(args[1:], stdout, stderr)
	case "compare":
		return cmp(args[1:], stdout, stderr)
	case "mutate":
		return mutate(args[1:], stdout, stderr)
	case "version":
		fmt.Fprintf(stdout, "%s %s\n", report.ToolName, report.ToolVersion)
		return 0
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	}
	fmt.Fprintf(stderr, "unknown command %q\n\n%s", args[0], usage)
	return 2
}

func newFlags(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

func fetch(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("fetch", stderr)
	src := fs.String("source", "", "horizon or rpc")
	url := fs.String("url", "", "provider URL (https)")
	from := fs.Uint("from", 0, "first ledger")
	to := fs.Uint("to", 0, "last ledger")
	out := fs.String("out", "", "stream file to write (resumes from FILE.checkpoint if present)")
	allowHTTP := fs.Bool("allow-http", false, "permit http:// URLs (local test servers only)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *src == "" || *url == "" || *from == 0 || *to < *from || *out == "" {
		fmt.Fprintln(stderr, "fetch needs --source, --url, --from, --to and --out (with --to >= --from)")
		return 2
	}
	if !strings.HasPrefix(*url, "https://") && !(*allowHTTP && strings.HasPrefix(*url, "http://")) {
		fmt.Fprintln(stderr, "--url must be https:// (use --allow-http only for a local server)")
		return 2
	}
	var s source.Source
	switch *src {
	case "horizon":
		s = horizon.New(*url)
	case "rpc":
		s = rpcsrc.New(*url)
	default:
		fmt.Fprintln(stderr, "--source must be horizon or rpc")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	cov, err := ingest.Run(ctx, ingest.Config{Source: s, From: uint32(*from), To: uint32(*to), OutPath: *out, Tool: report.ToolName + " " + report.ToolVersion,
		Log: func(f string, a ...any) { fmt.Fprintf(stderr, f+"\n", a...) }})
	if err != nil {
		fmt.Fprintf(stderr, "fetch failed: %v\n(progress is saved; run the same command again to resume)\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "wrote %s: covered %v, %d gap(s), complete=%v\n", *out, cov.Covered, len(cov.Gaps), cov.Complete)
	for _, g := range cov.Gaps {
		fmt.Fprintf(stdout, "  gap %d-%d: %s\n", g.From, g.To, g.Reason)
	}
	if !cov.Complete {
		return 3
	}
	return 0
}

func readStream(path string) (*model.Stream, []byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	s, err := model.ReadStream(bytes.NewReader(b))
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	return s, b, nil
}

func cmp(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("compare", stderr)
	ref := fs.String("reference", "", "reference stream (e.g. Horizon)")
	cand := fs.String("candidate", "", "candidate stream (your pipeline, or RPC)")
	format := fs.String("format", "text", "text, json or html")
	out := fs.String("out", "", "write the report here instead of stdout")
	jsonOut := fs.String("json-out", "", "also write the JSON report to this file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *ref == "" || *cand == "" {
		fmt.Fprintln(stderr, "compare needs --reference and --candidate")
		return 2
	}
	if *format != "text" && *format != "json" && *format != "html" {
		fmt.Fprintln(stderr, "--format must be text, json or html")
		return 2
	}
	rs, rb, err := readStream(*ref)
	if err != nil {
		fmt.Fprintln(stderr, "invalid reference:", err)
		return 2
	}
	cs, cb, err := readStream(*cand)
	if err != nil {
		fmt.Fprintln(stderr, "invalid candidate:", err)
		return 2
	}
	res, err := compare.Compare(compare.Input{Reference: rs, Candidate: cs, ReferenceName: *ref, CandidateName: *cand})
	if err != nil {
		fmt.Fprintln(stderr, "cannot compare:", err)
		return 2
	}
	rep := report.Build(res, report.Inputs{ReferenceBytes: rb, CandidateBytes: cb, Now: time.Now()})
	w := stdout
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		defer f.Close()
		w = f
	}
	switch *format {
	case "json":
		err = rep.JSON(w)
	case "html":
		err = rep.HTML(w)
	default:
		err = rep.Text(w)
	}
	if err == nil && *jsonOut != "" {
		var f *os.File
		if f, err = os.Create(*jsonOut); err == nil {
			err = rep.JSON(f)
			f.Close()
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	return rep.ExitCode()
}

// mutate writes a copy of a stream with payments dropped or duplicated. It exists to build defective candidate
// streams for testing a pipeline's comparison, and for this repository's demo; it is not a data source.
func mutate(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("mutate", stderr)
	in := fs.String("in", "", "input stream")
	out := fs.String("out", "", "output stream")
	var drop, dup listFlag
	fs.Var(&drop, "drop", "payment key (txHash:opIndex) to remove; repeatable")
	fs.Var(&dup, "duplicate", "payment key to repeat once; repeatable")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *in == "" || *out == "" || len(drop)+len(dup) == 0 {
		fmt.Fprintln(stderr, "mutate needs --in, --out and at least one --drop or --duplicate")
		return 2
	}
	s, _, err := readStream(*in)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	have := map[string]bool{}
	for _, p := range s.Payments {
		have[p.Key()] = true
	}
	for _, k := range append(append([]string{}, drop...), dup...) {
		if !have[k] {
			fmt.Fprintf(stderr, "no payment %s in %s\n", k, *in)
			return 2
		}
	}
	dropSet, dupSet := map[string]bool{}, map[string]bool{}
	for _, k := range drop {
		dropSet[k] = true
	}
	for _, k := range dup {
		dupSet[k] = true
	}
	f, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	defer f.Close()
	h := s.Header
	h.Adapter, h.Provider, h.Tool = "candidate", "mutated copy of "+s.Header.Adapter, "eventparity-engine mutate"
	h.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	model.Encode(f, h)
	ledgers := make([]uint32, 0, len(s.UnsupportedByLedger))
	for l := range s.UnsupportedByLedger {
		ledgers = append(ledgers, l)
	}
	sort.Slice(ledgers, func(i, j int) bool { return ledgers[i] < ledgers[j] })
	for _, l := range ledgers {
		model.Encode(f, model.LedgerNote{Type: "ledger", Ledger: l, Unsupported: s.UnsupportedByLedger[l]})
	}
	for _, p := range s.Payments {
		if dropSet[p.Key()] {
			continue
		}
		n := 1
		if dupSet[p.Key()] {
			n = 2
		}
		for i := 0; i < n; i++ {
			model.Encode(f, struct {
				Type string `json:"type"`
				model.Payment
			}{"payment", p})
		}
	}
	model.Encode(f, s.Coverage)
	fmt.Fprintf(stdout, "wrote %s (dropped %d, duplicated %d)\n", *out, len(drop), len(dup))
	return 0
}
