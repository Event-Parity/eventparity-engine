## What and why

## Verification
- [ ] `go vet ./... && gofmt -l . && go test ./...`
- [ ] No test derives its expected value from the code under test
- [ ] Gaps are still never reported as differences; no parity with gaps
