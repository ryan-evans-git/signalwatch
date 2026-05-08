.DEFAULT_GOAL := build

GO ?= go
NPM ?= npm

# Local-dev parity with the CI pipeline. The CI workflow is the source of
# truth — when a target diverges from CI, fix CI and update this Makefile.

.PHONY: build web go test test-race coverage coverage-html lint vet \
        gosec govulncheck licenses verify clean tools

build: web go

web:
	cd web && $(NPM) install && $(NPM) run build

go:
	$(GO) build -o bin/signalwatch ./cmd/signalwatch
	$(GO) build -o bin/signalwatchctl ./cmd/signalwatchctl

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

# Coverage profile + summary. Matches the CI invocation exactly.
coverage:
	$(GO) test -race -covermode=atomic -coverprofile=cover.out -coverpkg=./... ./...
	$(GO) tool cover -func=cover.out | tail -1

coverage-html: coverage
	$(GO) tool cover -html=cover.out -o coverage.html
	@echo "open coverage.html"

# Coverage-gate enforcement. Same tool the CI uses (vladopajic/go-test-coverage).
# `make tools` installs it locally if not on PATH.
coverage-gate: coverage
	@command -v go-test-coverage >/dev/null 2>&1 || { \
	  echo "go-test-coverage not installed. run 'make tools' first."; exit 1; }
	go-test-coverage --config=./.testcoverage.yml --profile=cover.out

lint:
	@command -v golangci-lint >/dev/null 2>&1 || { \
	  echo "golangci-lint not installed. run 'make tools' first."; exit 1; }
	golangci-lint run ./...

vet:
	$(GO) vet ./...

gosec:
	@command -v gosec >/dev/null 2>&1 || { \
	  echo "gosec not installed. run 'make tools' first."; exit 1; }
	gosec -severity medium -exclude-dir=examples -exclude-dir=web -exclude=G104 ./...

govulncheck:
	@command -v govulncheck >/dev/null 2>&1 || { \
	  echo "govulncheck not installed. run 'make tools' first."; exit 1; }
	govulncheck ./...

licenses:
	@command -v go-licenses >/dev/null 2>&1 || { \
	  echo "go-licenses not installed. run 'make tools' first."; exit 1; }
	go-licenses check ./... \
	  --disallowed_types=forbidden,restricted \
	  --ignore=github.com/ryan-evans-git/signalwatch

# `make verify` runs every gate the CI runs (minus trivy / codeql / gitleaks
# which are CI-only). Run this before pushing.
verify: lint vet test-race coverage-gate gosec govulncheck licenses
	@echo "✓ verify complete"

# One-shot installer for every CLI tool the local-dev gates need.
tools:
	$(GO) install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.62.2
	$(GO) install github.com/securego/gosec/v2/cmd/gosec@v2.22.0
	$(GO) install golang.org/x/vuln/cmd/govulncheck@latest
	$(GO) install github.com/google/go-licenses@v1.6.0
	$(GO) install github.com/vladopajic/go-test-coverage/v2@v2.16.0

clean:
	rm -rf bin/ internal/ui/dist/* web/node_modules web/.vite cover.out coverage.html
