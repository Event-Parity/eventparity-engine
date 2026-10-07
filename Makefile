.PHONY: build test check live
build:
	go build -o bin/eventparity ./cmd/eventparity
test:
	go test ./...
check:
	go vet ./... && test -z "$$(gofmt -l .)" && go test ./...
live:
	go test -tags live ./test/live
