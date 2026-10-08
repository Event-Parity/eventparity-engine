# Verification

```bash
go vet ./... && gofmt -l . && go test ./...            # deterministic: 75 tests, no network
go test -tags live ./test/live                         # opt-in: real Horizon + RPC over the corpus range
```

Deterministic tests replay recorded real responses, so they run offline. Golden reports are pinned; `UPDATE_GOLDEN=1 go test ./test/corpus` regenerates them after an intended change.
