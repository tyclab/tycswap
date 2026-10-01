MODULE  := github.com/tyclab/tycswap
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo v0.0.0-dev)
LDFLAGS := -X $(MODULE)/internal/version.Version=$(VERSION)

.PHONY: help build install test race vet fmt lint vuln clean

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-10s %s\n", $$1, $$2}'

build: ## Build ./tycswap with embedded version
	go build -ldflags "$(LDFLAGS)" -o tycswap ./cmd/tycswap

install: ## go install with embedded version
	go install -ldflags "$(LDFLAGS)" ./cmd/tycswap

test: ## Run all tests
	go test ./...

race: ## Run all tests with the race detector
	go test -race ./...

vet: ## go vet all packages
	go vet ./...

fmt: ## gofmt all source (fails if anything was unformatted)
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "unformatted:"; echo "$$out"; gofmt -w .; exit 1; fi

vuln: ## Scan for known vulnerabilities (govulncheck, fetched by go run)
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

clean: ## Remove built binary
	rm -f tycswap
