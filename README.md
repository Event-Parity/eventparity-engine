<p align="center"><img src="docs/assets/banner.svg" alt="eventparity-engine" width="100%"></p>

# eventparity-engine

[![CI](https://github.com/Event-Parity/eventparity-engine/actions/workflows/ci.yml/badge.svg)](https://github.com/Event-Parity/eventparity-engine/actions/workflows/ci.yml) [![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE) [![Release](https://img.shields.io/github/v/release/Event-Parity/eventparity-engine)](https://github.com/Event-Parity/eventparity-engine/releases)

[Documentation](https://stellar-developer-tools.gitbook.io/eventparity-engine/) · [App repository](https://github.com/Event-Parity/eventparity-studio) · [Issues](https://github.com/Event-Parity/eventparity-engine/issues) · [Discussions](https://github.com/Event-Parity/eventparity-engine/discussions)


Change your Stellar data source without silently changing your records.

EventParity compares the **classic payments** that two sources report for the same ledger range: Horizon, Stellar RPC, or a stream from your own ingestion pipeline. It pairs every operation by identity (transaction hash and operation index), checks issuer-aware assets and exact 7-decimal amounts, and reports what is missing, duplicated or reinterpreted, with the exact source evidence. A ledger a source could not serve is reported as a **coverage gap**, never as a missing payment, and a comparison with gaps can never be "parity".

## What it does

- **Two working live adapters.** `horizon` reads `/ledgers/{n}/operations` with paging and bounded retries. `rpc` reads `getTransactions` and decodes each transaction envelope's operations from XDR with the official Stellar Go SDK (including fee-bump and muxed-account payments), which is the route the official [Horizon to RPC migration guide](https://developers.stellar.org/docs/data/apis/migrate-from-horizon-to-rpc) gives for operations.
- **Exact identity and amounts.** Amounts are integer stroops internally and canonical 7-decimal strings in streams; no floating point. Assets are code plus issuer; the same code from a different issuer is a different asset.
- **Coverage and checkpoints.** Every stream ends with a coverage line accounting for each requested ledger exactly once (covered or gap, with the reason). Providers keep finite history (testnet RPC keeps about 7 days), so out-of-window ledgers become gaps. Fetches checkpoint after every ledger and resume after a crash without duplicating or skipping a ledger (tested by killing a real adapter run mid-ledger).
- **Honest verdicts.** `parity` (every covered ledger matched, no gaps), `differences`, or `inconclusive` (gaps exist and nothing differs in what was compared).
- **Reports** as JSON (published [schema](schema/report.v1.schema.json)), plain text and self-contained HTML. A viewer lives in [eventparity-studio](https://github.com/Event-Parity/eventparity-studio).
- **Bring your own candidate.** Your pipeline writes a JSON Lines stream in the documented format; `eventparity compare` tells you how it differs from Horizon or RPC. A candidate without a coverage line is refused: a stream that does not declare what it covers cannot be compared honestly.

## Install and run

Needs Go 1.25 or newer (the Stellar Go SDK requires it; with an older Go installed, `GOTOOLCHAIN=auto` fetches 1.25.0 on demand).

```bash
git clone https://github.com/Event-Parity/eventparity-engine.git
cd eventparity-engine
go build -o eventparity ./cmd/eventparity

# Compare Horizon with RPC over a real recorded testnet range (no network needed):
./eventparity compare --reference corpus/testnet-2026-10-07/horizon.jsonl \
                      --candidate corpus/testnet-2026-10-07/rpc.jsonl

# Fetch a range yourself (resumes from FILE.checkpoint if interrupted):
./eventparity fetch --source horizon --url https://horizon-testnet.stellar.org --from 5071650 --to 5071658 --out horizon.jsonl
./eventparity fetch --source rpc     --url https://soroban-testnet.stellar.org --from 5071650 --to 5071658 --out rpc.jsonl
```

Exit codes: `fetch` 0 complete, 3 finished with coverage gaps, 1 error, 2 usage. `compare` 0 parity, 1 differences, 3 inconclusive, 2 invalid input or usage.

## Demo 1: a candidate that drops one payment and duplicates another

```bash
./eventparity compare --reference corpus/testnet-2026-10-07/horizon.jsonl \
                      --candidate corpus/testnet-2026-10-07/candidate-defective.jsonl
```

Verdict `DIFFERENCES`, exit 1: `MISSING_IN_CANDIDATE` for a 5.0000000 USDX payment and `DUPLICATED_IN_CANDIDATE` for a 2.0000000 USDX payment, each with the exact transaction hash, operation index and both sides' evidence. The candidate was produced with `eventparity mutate` from the real Horizon stream. Report: [report-defective.json](corpus/reports/report-defective.json).

## Demo 2: unavailable history is a gap, not a missing payment

```bash
./eventparity compare --reference corpus/retention-gap-2026-10-07/horizon.jsonl \
                      --candidate corpus/retention-gap-2026-10-07/rpc.jsonl
```

Testnet RPC did not retain ledgers 4950790-4950801 when this was recorded, while Horizon did (it holds history back to ledger 128). The report says `INCONCLUSIVE`, lists the RPC gap with its reason, compares only 4950802-4950810 (10 payments matched), and counts 11 Horizon payments in the gap as "not compared", not as differences. Report: [report-retention-gap.json](corpus/reports/report-retention-gap.json).

## The recorded corpus

`corpus/testnet-2026-10-07` covers ledgers 5071650-5071658 on testnet. [`tools/mkcorpus`](tools/mkcorpus/main.go) submitted nine real transactions from throwaway accounts (keys in memory only): an issued-asset payment, a 7-decimal amount, a 1-stroop payment, a muxed destination, a fee-bumped payment, a four-operation transaction mixing payments with `manage_data`, a path payment and an underfunded (failed) payment. [`ground-truth.json`](corpus/testnet-2026-10-07/ground-truth.json) records what the tool built; it is independent of every adapter. Tests assert both streams contain exactly those eight payments with exact fields, that the failed transaction's payment is absent, and that the unsupported operations were counted. The same ledgers held two payments from other accounts; for those the only cross-check is Horizon against RPC, and they agree. See [corpus/README.md](corpus/README.md) for how it was recorded and what was trimmed.

## Supported scope and limits

- Classic `payment` operations in successful transactions. Path payments, `create_account`, offers, trustlines, claimable balances, Soroban operations and everything else are **counted by operation type per ledger and not compared one by one**. Count differences are disclosed and do not change the verdict.
- RPC events do not reproduce every Horizon effect, balance or history record, and this tool does not claim they do. The RPC adapter decodes transaction envelopes, not CAP-67 events.
- A finite comparison is evidence about that ledger range only.
- Tested live against testnet only. Mainnet endpoints use the same code but are not exercised here.

## Verification

```bash
go vet ./... && gofmt -l . && go test ./...            # deterministic: 75 tests, no network
go test -tags live ./test/live                         # opt-in: real Horizon + RPC over the corpus range
```

Deterministic tests replay recorded real responses, so they run offline. Golden reports are pinned; `UPDATE_GOLDEN=1 go test ./test/corpus` regenerates them after an intended change.

## Status

Engineering complete for the declared version-one scope. Pushed to GitHub with CI green and releases v0.1.0, v0.1.1 and v0.1.2 (see CHANGELOG.md). Not done: review by an independent ingestion maintainer. See [SPEC.md](SPEC.md) and [docs/adr](docs/adr).

MIT licensed. Contributing: [CONTRIBUTING.md](CONTRIBUTING.md). Security: [SECURITY.md](SECURITY.md).

## Repository layout

- `cmd/`: command-line entry point
- `corpus/`: recorded testnet corpus and golden reports
- `docs/`: decision records (ADRs), evidence and assets
- `gitbook/`: source of the GitBook documentation
- `internal/`: Go packages
- `schema/`: JSON Schemas, generated and checked in CI
- `test/`: tests
- `tools/`: corpus and recording tools

## Documentation

The full documentation is at https://stellar-developer-tools.gitbook.io/eventparity-engine/. It is built from the `gitbook/` folder of this repository and synced from `main`, so a fix to a page is a pull request here.

## Contributing

Open issues are scoped so one person can finish one in a single cycle, and each lists acceptance criteria. Read [CONTRIBUTING.md](CONTRIBUTING.md), pick an issue from the [issue list](https://github.com/Event-Parity/eventparity-engine/issues), and say you are taking it before you start. Security reports go through [SECURITY.md](SECURITY.md), not public issues.

## Maintainers

| Maintainer | Role | GitHub |
|---|---|---|
| Anas Abubakar | Lead maintainer | [@Anasabubakar](https://github.com/Anasabubakar) |
| Abdulbasit Fazazi | Co-maintainer | [@fazaziishola-coder](https://github.com/fazaziishola-coder) |

## Community

Questions and design discussion go in [GitHub Discussions](https://github.com/Event-Parity/eventparity-engine/discussions). Bugs and scoped work go in [Issues](https://github.com/Event-Parity/eventparity-engine/issues).

## License

MIT. See [LICENSE](LICENSE).

## Contributors

Thanks to all the contributors who have made this project possible.

<a href="https://github.com/Event-Parity/eventparity-engine/graphs/contributors">
  <img src="https://contrib.rocks/image?repo=Event-Parity/eventparity-engine" alt="Contributors to eventparity-engine" />
</a>
