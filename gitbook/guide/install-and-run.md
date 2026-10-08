# Install and run

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
