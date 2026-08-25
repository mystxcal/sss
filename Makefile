SHELL := /bin/bash

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 1.0.0)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

MODULE  := github.com/sss/sss
LDFLAGS := -s -w \
	-X $(MODULE)/internal/version.Version=$(VERSION) \
	-X $(MODULE)/internal/version.Commit=$(COMMIT) \
	-X $(MODULE)/internal/version.BuildDate=$(DATE)

# CGO is deliberately off: the release must cross-compile cleanly for Linux and
# Windows without a C toolchain (frozen decision D025).
export CGO_ENABLED := 0

BIN  := bin
DIST := dist

.PHONY: all build test race vet fmt check clean cross release smoke install-aliases

all: build

build: ## Build the sss binary for the host platform
	@mkdir -p $(BIN)
	go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN)/sss ./cmd/sss

test: ## Run the full test suite
	go test ./...

race: ## Run the full test suite under the race detector
	go test -race ./...

vet: ## Static analysis
	go vet ./...

fmt: ## Format the tree
	gofmt -l -w $(shell find . -name '*.go' -not -path './vendor/*')

check: vet test ## Everything a change must pass before review

clean:
	rm -rf $(BIN) $(DIST)

# Release targets. linux/amd64 is the server target (Debian 11 compatible);
# the rest are client targets.
PLATFORMS := linux/amd64 linux/arm64 windows/amd64 windows/arm64 darwin/arm64

cross: ## Cross-compile every release platform
	@mkdir -p $(DIST)
	@set -e; for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		ext=""; if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
		out=$(DIST)/sss-$(VERSION)-$$os-$$arch$$ext; \
		echo "building $$out"; \
		GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags '$(LDFLAGS)' -o $$out ./cmd/sss; \
	done

release: clean cross ## Build every platform plus checksums and the alias shims
	@scripts/release.sh $(VERSION)

smoke: ## Run the contract smoke script against $$SSS_URL
	@scripts/contract-smoke.sh

install-aliases: build ## Install sss with its sssend/ssrecv/sssd aliases into $$PREFIX/bin
	@PREFIX=$${PREFIX:-/usr/local}; \
	install -m 0755 $(BIN)/sss $$PREFIX/bin/sss; \
	for alias in sssend ssrecv sssd; do \
		ln -sf $$PREFIX/bin/sss $$PREFIX/bin/$$alias; \
	done; \
	echo "installed sss, sssend, ssrecv, sssd into $$PREFIX/bin"
