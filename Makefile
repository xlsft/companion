VERSION ?= dev
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -X github.com/xlsft/openclick/internal/buildinfo.Version=$(VERSION) \
           -X github.com/xlsft/openclick/internal/buildinfo.Commit=$(COMMIT)

.PHONY: build test vet cross

build:
	go build -ldflags "$(LDFLAGS)" -o bin/ ./cmd/...

test:
	go test ./...

vet:
	go vet ./...

# Pure-Go packages must cross-compile for every target OS (ADR-0001 rule 6).
cross:
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./...
	GOOS=darwin  GOARCH=arm64 CGO_ENABLED=0 go build ./...
	GOOS=linux   GOARCH=amd64 CGO_ENABLED=0 go build ./...
