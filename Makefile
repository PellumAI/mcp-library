.PHONY: help check build test vet lint lint-install pin-check validate-all mcp-package index site fixture verify clean

MCPLIB  := ./bin/mcplib
BIN_DIR := $(CURDIR)/bin

# golangci-lint is pinned by the same mechanism MCPGW's Makefile uses: an
# exact version installed into ./bin, and a post-install assertion that the
# binary reports it, so `make lint` never runs whatever is on PATH.
GOLANGCI_LINT_VERSION ?= v2.13.2
GOLANGCI_LINT         := $(BIN_DIR)/golangci-lint
GOLANGCI_LINT_VERSION_RE := $(subst .,\.,$(patsubst v%,%,$(GOLANGCI_LINT_VERSION)))
GOLANGCI_LINT_VERSION_OK = { [ -x $(GOLANGCI_LINT) ] && $(GOLANGCI_LINT) --version 2>/dev/null | grep -qE '(^|[^0-9.])$(GOLANGCI_LINT_VERSION_RE)([^0-9.]|$$)'; }

## help: list targets
help:
	@grep -hE '^## ' $(MAKEFILE_LIST) | sed -e 's/^## //'

## check: everything CI runs on a pull request
check: pin-check vet lint test validate-all

## build: build the mcplib binary
build:
	@mkdir -p bin
	go build -trimpath -o $(MCPLIB) ./cmd/mcplib

## test: unit tests
test:
	go test ./...

## vet: go vet
vet:
	go vet ./...

## lint: golangci-lint at the pinned version
lint: lint-install
	$(GOLANGCI_LINT) run ./...

lint-install:
	@if $(GOLANGCI_LINT_VERSION_OK); then \
		echo "golangci-lint $(GOLANGCI_LINT_VERSION) already installed at $(GOLANGCI_LINT)"; \
	else \
		echo "installing golangci-lint $(GOLANGCI_LINT_VERSION) into $(BIN_DIR)"; \
		mkdir -p $(BIN_DIR); \
		script=$$(mktemp) || exit 1; \
		trap 'rm -f "$$script"' EXIT INT TERM; \
		curl -sSfL -o "$$script" https://raw.githubusercontent.com/golangci/golangci-lint/$(GOLANGCI_LINT_VERSION)/install.sh || exit 1; \
		sh "$$script" -b $(BIN_DIR) $(GOLANGCI_LINT_VERSION) || exit 1; \
		if ! $(GOLANGCI_LINT_VERSION_OK); then \
			echo "ERROR: $(GOLANGCI_LINT) does not report $(GOLANGCI_LINT_VERSION) after install"; \
			exit 1; \
		fi; \
	fi

## validate-all: validate every recipe against the pinned executor target
validate-all: build
	@$(MCPLIB) validate ./servers/...

## mcp-package: build one package. NAME=<server> ARCH=<amd64|arm64>
mcp-package: build
	@test -n "$(NAME)" || { echo "NAME is required, e.g. make mcp-package NAME=grafana ARCH=amd64" >&2; exit 2; }
	@$(MCPLIB) build --server "$(NAME)" --arch "$(or $(ARCH),amd64)" --out dist

## index: regenerate index.json from dist/
index: build
	@$(MCPLIB) index --dist dist --servers servers --out dist/index.json

## site: generate the static site from dist/index.json
site: build
	@$(MCPLIB) site --index dist/index.json --out site

## fixture: regenerate the miniature library MCPGW's contract test consumes, and the rotation rehearsal
fixture: build
	@$(MCPLIB) fixture --out dist/fixture
	@$(MCPLIB) fixture --rotation --out dist/rotation

## verify: verify dist/index.json against the committed public keys
verify: build
	@$(MCPLIB) verify --keys keys --index dist/index.json

## pin-check: fail if any dependency is resolved at build time rather than pinned
pin-check:
	@./scripts/pin-check.sh

## clean: remove build outputs
clean:
	rm -rf bin dist site
