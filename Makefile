VERSION ?= dev
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -X github.com/xlsft/openclick/internal/buildinfo.Version=$(VERSION) \
           -X github.com/xlsft/openclick/internal/buildinfo.Commit=$(COMMIT)

.PHONY: build test vet cross

# Native build with cgo: covers the Gio UI and cmd/companion, which the
# CGO_ENABLED=0 cross build below skips.
build:
	go build ./...
	go build -ldflags "$(LDFLAGS)" -o bin/ ./cmd/...

test:
	go test ./...

vet:
	go vet ./...

# Gio needs cgo on Linux (Wayland/X11/EGL) and macOS, so the UI and the
# binary that links it are built natively only (ADR-0001 amendment 1).
CGO_PKGS_RE := /cmd/companion$$|/internal/ui(/|$$)
PURE_PKGS   = $(shell go list ./... | grep -Ev '$(CGO_PKGS_RE)')

# Pure-Go packages must cross-compile for every target OS (ADR-0001 rule 6).
cross:
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build $(PURE_PKGS)
	GOOS=darwin  GOARCH=arm64 CGO_ENABLED=0 go build $(PURE_PKGS)
	GOOS=linux   GOARCH=amd64 CGO_ENABLED=0 go build $(PURE_PKGS)
