# eventparity-engine: specification (v0.1)

## User
A maintainer of a payment-ingestion pipeline moving from Horizon to Stellar RPC (or changing providers) who needs evidence that the payments their records contain did not silently change. Secondary: an integrator validating a custom indexer.

## Supported scope
One declared ledger range per run. Successful classic `payment` operations. Issuer-aware assets, exact amounts, muxed account IDs. Sources: Horizon, Stellar RPC, or a candidate stream in the documented format.

## Non-goals
- Not an indexer, not a payment-history service, not an ingestion framework (use the Ingest SDK for that).
- No Soroban events, path payments, offers, balances or effects. These are counted by type, not compared.
- No claim that RPC reproduces Horizon beyond what a report shows for the compared ledgers.
- No custody, signing or submission (the corpus generator under `tools/` submits testnet transactions to build fixtures; the engine never does).

## Data model
**Payment** (canonical observation): `ledger`, `applicationOrder`, `txHash` (64 lowercase hex), `opIndex` (0-based position among all operations in the transaction, including unsupported ones), `from`, `fromMuxedId?`, `to`, `toMuxedId?`, `asset` (`native` or `credit` with code and issuer), `amount` (canonical 7-decimal string). Identity key: `txHash:opIndex`.

**Stream** (`eventparity-stream/v1`, JSON Lines): a `header` line (adapter, provider origin, network passphrase, requested range, creation time, scope); `ledger` lines with per-type counts of unsupported operations; `payment` lines; a final `coverage` line with `covered` ranges and `gaps` (each with a reason). Coverage and gaps must account for every requested ledger exactly once; `complete` is true exactly when there are no gaps. Readers fail closed: missing header or coverage, unknown fields or line types, non-canonical amounts, invalid addresses, payments outside covered ranges, oversize lines (1 MiB) are all errors.

**Report** (`report.v1.schema.json`): verdict, compared ranges, both sides' coverage, matched count, differences with evidence, unsupported-type count differences, fixed limitations, SHA-256 of each input stream.

## Classifications
`missing_in_candidate`, `missing_in_reference` (only when the other side covered that ledger), `duplicated_in_candidate`, `duplicated_in_reference`, `reinterpreted` (same identity, differing among ledger, applicationOrder, from, fromMuxedId, to, toMuxedId, asset, amount; every differing field is named). Observations in a ledger only one side covered are counted as not compared.

## Verdict
`differences` if any classification applies; else `inconclusive` if any gap exists on either side; else `parity`.

## Adapter behavior
- Both adapters emit every ledger in the requested range once, in order, including empty ledgers; an error is returned rather than a ledger skipped.
- Bounded retries with backoff on transport errors, 429 and 5xx; client errors are not retried; context cancellation stops retries.
- Provider history: `Bounds` reports the oldest and latest ledger served; ledgers outside become gaps before any fetch. If a provider later reports history unavailable mid-run, the remainder becomes a gap.
- Horizon identity comes from the operation's TOID (ledger, application order, operation index). RPC identity comes from `getTransactions` ledger, applicationOrder and the position in the decoded envelope.

## Checkpoint and restart
After each complete ledger the stream is fsynced and a checkpoint (`FILE.checkpoint`: adapter, provider, network, requested range, last ledger, stream byte length) is atomically replaced. On restart the stream is truncated to the checkpointed length, so a crash between writing a ledger and checkpointing re-reads that ledger. A checkpoint from a different job is refused; an existing stream without a checkpoint is never overwritten; a finished stream is not silently refetched.

## CLI and exit codes
`fetch`, `compare`, `mutate`, `version`. `fetch`: 0 complete, 3 gaps, 1 error, 2 usage. `compare`: 0 parity, 1 differences, 3 inconclusive, 2 invalid input. Provider URLs must be https (plain http only with `--allow-http`). Only the URL origin is recorded in streams.

## Acceptance criteria (each tested)
1. Both live adapters agree with each other and with independently built ground truth on real testnet ledgers.
2. A dropped and a duplicated payment are each identified with exact evidence.
3. An out-of-retention range is a gap and the verdict is never parity.
4. A crash mid-run followed by a restart reproduces the uninterrupted stream byte for byte.
5. Failed transactions never appear as payments; unsupported operations are counted, not dropped.
6. Candidate streams that omit coverage, claim more than requested or contain malformed observations are refused.
