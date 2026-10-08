# Demo 1: a candidate that drops one payment and duplicates another

```bash
./eventparity compare --reference corpus/testnet-2026-10-07/horizon.jsonl \
                      --candidate corpus/testnet-2026-10-07/candidate-defective.jsonl
```

Verdict `DIFFERENCES`, exit 1: `MISSING_IN_CANDIDATE` for a 5.0000000 USDX payment and `DUPLICATED_IN_CANDIDATE` for a 2.0000000 USDX payment, each with the exact transaction hash, operation index and both sides' evidence. The candidate was produced with `eventparity mutate` from the real Horizon stream. Report: [report-defective.json](https://github.com/Event-Parity/eventparity-engine/blob/main/corpus/reports/report-defective.json).
