// Command record fetches a ledger range from live Horizon and RPC endpoints through the recorder, saving every raw
// response (minus fields no adapter reads) and the two resulting streams. It is how the committed corpus was made.
//
//	go run ./tools/record -from 5071650 -to 5071658 -out corpus/testnet-2026-10-07
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Anasabubakar/eventparity-engine/internal/horizon"
	"github.com/Anasabubakar/eventparity-engine/internal/ingest"
	"github.com/Anasabubakar/eventparity-engine/internal/recorder"
	"github.com/Anasabubakar/eventparity-engine/internal/rpcsrc"
)

func main() {
	from := flag.Uint("from", 0, "first ledger")
	to := flag.Uint("to", 0, "last ledger")
	out := flag.String("out", "", "corpus directory")
	hz := flag.String("horizon", "https://horizon-testnet.stellar.org", "Horizon URL")
	rpcURL := flag.String("rpc", "https://soroban-testnet.stellar.org", "RPC URL")
	flag.Parse()
	if *from == 0 || *to < *from || *out == "" {
		fmt.Fprintln(os.Stderr, "usage: record -from N -to M -out DIR")
		os.Exit(2)
	}
	ctx := context.Background()
	now := func() time.Time { return time.Now() }

	h := horizon.New(*hz)
	h.HTTP = &http.Client{Timeout: 30 * time.Second, Transport: &recorder.Recorder{
		Dir: filepath.Join(*out, "recorded", "horizon"), Next: http.DefaultTransport,
		TrimFields: []string{"_embedded.records[]._links"}}}
	r := rpcsrc.New(*rpcURL)
	r.HTTP = &http.Client{Timeout: 60 * time.Second, Transport: &recorder.Recorder{
		Dir: filepath.Join(*out, "recorded", "rpc"), Next: http.DefaultTransport,
		TrimFields: []string{"resultMetaXdr", "events", "diagnosticEventsXdr", "resultMetaJson", "envelopeJson", "resultJson"}}}

	run := func(name string, cfg ingest.Config) {
		cov, err := ingest.Run(ctx, cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
			os.Exit(1)
		}
		fmt.Printf("%s: covered %v gaps %d complete=%v\n", name, cov.Covered, len(cov.Gaps), cov.Complete)
	}
	run("horizon", ingest.Config{Source: h, From: uint32(*from), To: uint32(*to), OutPath: filepath.Join(*out, "horizon.jsonl"), Now: now, Tool: "eventparity-engine record"})
	run("rpc", ingest.Config{Source: r, From: uint32(*from), To: uint32(*to), OutPath: filepath.Join(*out, "rpc.jsonl"), Now: now, Tool: "eventparity-engine record"})
}
