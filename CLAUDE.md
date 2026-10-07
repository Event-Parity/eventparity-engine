# eventparity-engine: working notes

Commands: `go build ./...`, `go vet ./...`, `gofmt -l .`, `go test ./...`, `go test -tags live ./test/live`, `UPDATE_GOLDEN=1 go test ./test/corpus`.
Toolchain: Go 1.25 required by go-stellar-sdk v0.7.3. On a machine with older Go use `export GOTOOLCHAIN=auto` (the go.mod line is `go 1.25.0`, not `1.25`, so the download resolves).
Constraints: gaps are never differences; fail closed on bad streams; no floats; do not hand-edit corpus files; no AI co-author trailers in commits.
Unfinished: GitHub publishing and CI run, tagged release, independent maintainer review of the stream format.
