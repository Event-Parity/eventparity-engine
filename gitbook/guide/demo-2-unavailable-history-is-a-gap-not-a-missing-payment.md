# Demo 2: unavailable history is a gap, not a missing payment

```bash
./eventparity compare --reference corpus/retention-gap-2026-10-07/horizon.jsonl \
                      --candidate corpus/retention-gap-2026-10-07/rpc.jsonl
```

Testnet RPC did not retain ledgers 4950790-4950801 when this was recorded, while Horizon did (it holds history back to ledger 128). The report says `INCONCLUSIVE`, lists the RPC gap with its reason, compares only 4950802-4950810 (10 payments matched), and counts 11 Horizon payments in the gap as "not compared", not as differences. Report: [report-retention-gap.json](https://github.com/Event-Parity/eventparity-engine/blob/main/corpus/reports/report-retention-gap.json).
