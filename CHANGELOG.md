# Changelog

## 0.1.0 (unreleased)
- Canonical payment observation, strict JSON Lines streams with exact coverage accounting.
- Horizon adapter (operations per ledger, paging, bounded retries) and RPC adapter (getTransactions with XDR decoding, fee-bump and muxed support).
- Checkpointed ingest with crash-safe restart.
- Comparator: missing, duplicated and reinterpreted payments; coverage-aware gaps; parity, differences or inconclusive.
- JSON (schema v1), text and self-contained HTML reports.
- CLI: fetch, compare, mutate, version.
- Recorded real testnet corpus with independent ground truth and golden reports.
