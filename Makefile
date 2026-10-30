.PHONY: build install test test-verbose lint lint-fix fmt vet quality clean setup-hooks run tui mcp

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  := $(shell git rev-parse HEAD 2>/dev/null || echo "unknown")
DATE    := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

GOCMD   := go
GOBUILD := $(GOCMD) build
GOTEST  := $(GOCMD) test
GOLINT  := $(shell go env GOPATH)/bin/golangci-lint run

# Build the cainban binary. CGO is required (SQLite via mattn/go-sqlite3).
build:
	CGO_ENABLED=1 $(GOBUILD) -ldflags="$(LDFLAGS)" -o bin/cainban ./cmd/cainban

# Install into $GOPATH/bin (or ~/.local/bin via ./install.sh).
install:
	CGO_ENABLED=1 $(GOCMD) install -ldflags="$(LDFLAGS)" ./cmd/cainban

test:
	CGO_ENABLED=1 $(GOTEST) -race -cover ./...

test-verbose:
	CGO_ENABLED=1 $(GOTEST) -race -cover -v ./...

lint:
	$(GOLINT)

lint-fix:
	$(GOLINT) --fix

fmt:
	gofmt -w .

vet:
	$(GOCMD) vet ./...

# Full local gate, mirroring CI: format check, vet, lint, race tests.
quality: vet lint test
	@test -z "$$(gofmt -l .)" || (echo "gofmt: files need formatting:"; gofmt -l .; exit 1)
	@echo "quality: OK"

clean:
	rm -rf bin/
	$(GOCMD) clean

# Install the pre-commit hooks (gofmt, go vet, go mod tidy, golangci-lint, go test).
setup-hooks:
	pre-commit install

# Convenience runners.
tui: build
	./bin/cainban tui

mcp: build
	./bin/cainban mcp
