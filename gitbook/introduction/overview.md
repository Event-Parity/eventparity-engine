# Overview

Change your Stellar data source without silently changing your records.

EventParity compares the **classic payments** that two sources report for the same ledger range: Horizon, Stellar RPC, or a stream from your own ingestion pipeline. It pairs every operation by identity (transaction hash and operation index), checks issuer-aware assets and exact 7-decimal amounts, and reports what is missing, duplicated or reinterpreted, with the exact source evidence. A ledger a source could not serve is reported as a **coverage gap**, never as a missing payment, and a comparison with gaps can never be "parity".

Source: [eventparity-engine on GitHub](https://github.com/Event-Parity/eventparity-engine). Releases: [GitHub releases](https://github.com/Event-Parity/eventparity-engine/releases).
