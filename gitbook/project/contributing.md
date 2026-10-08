# Contributing

```bash
go vet ./... && test -z "$(gofmt -l .)" && go test ./...
go test -tags live ./test/live      # optional, hits testnet
```

Rules that keep the tool honest:

- A ledger a source did not serve is a coverage gap, never a missing payment. Never produce `parity` when any gap exists.
- Fail closed on malformed input: a stream without coverage, an invalid observation or an unaccounted ledger is an error, not a warning.
- Amounts are integers or exact decimal strings. No floats anywhere in the data path.
- Expected values in tests must not come from the code under test. Use `corpus/*/ground-truth.json` or hand-reviewed counts, and say where a number comes from.
- Recorded corpora are real captures made with `tools/record`. Do not hand-edit them; re-record and say so in the commit.
- Changing a classification or the stream format means updating `SPEC.md`, the schema and the golden reports (`UPDATE_GOLDEN=1 go test ./test/corpus`).
- AI-assisted changes are welcome if you understand and have verified every line.

One logical change per commit with a conventional prefix (`feat`, `fix`, `test`, `docs`, `build`, `ci`).
