# ADR 0001: Why a comparison harness next to the Ingest SDK and RPC

Status: accepted, 2026-10-07. What follows is what was read on that date; no existing tool was executed.

## What the existing sources say

- **Official Horizon to RPC migration guide** (page read). It maps Horizon endpoints to RPC methods and says some have no direct equivalent. For `GET /ledgers/{seq}/operations` it says to use `getTransactions` and parse transaction XDR for operations; for payments it points to `getEvents` (CAP-67 asset events, on nodes that emit them) together with `getTransactions` meta XDR. It tells applications to build or partner for an indexed equivalent where there is no mapping. It does not provide a way to check that a migrated application's records equal the old ones.
- **stellar-rpc issue 881** (read). A closed issue about RPC's own "v2 ingestion": one pass over each ledger feeding event payloads, tx-hash entries and fee statistics, with the SDK side tracked separately. It concerns RPC's internals and does not describe a client-side Horizon-versus-RPC comparison.
- **Stellar Ingest SDK** (documentation page and `ingest` package listing read). A library for building ledger-metadata export and consumer pipelines: ledger backends, change and transaction readers. It helps you build ingestion. It does not compare two ingestions.

None of the three, as read, offers a harness that takes two sources (or a user's pipeline) over a fixed range and says which payments differ, with coverage honesty. That gap is the whole claim. It is a narrow, checkable one: absence from what was read is not proof that no such tool exists anywhere.

## Decision
Build a small comparison engine that reuses the official Go SDK for XDR and addresses, reads Horizon directly over HTTP, and defines a documented stream format so a user's own pipeline can be the candidate. If an equivalent maintained harness is found, contribute the corpus or an adapter there.

## Consequences
- The RPC adapter follows the guide's `getTransactions` plus XDR route, not the `getEvents` route. A user whose pipeline is event-based can still compare by emitting candidate streams. An events-based adapter is a possible later addition and would need its own tests.
- The Go SDK pins Go 1.25. This is stated in the README.
- No independent ingestion maintainer has reviewed the stream format or the corpus. That validation is outstanding and tracked separately from engineering.
