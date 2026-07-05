# Bentoolkit Makefile
# Build, test, and install targets for bentoo CLI

BINARY_NAME := bentoo
MODULE := github.com/obentoo/bentoolkit
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME := $(shell date -u '+%Y-%m-%d_%H:%M:%S')
VERSION_PKG := $(MODULE)/internal/common/version
LDFLAGS := -ldflags "-s -w -X $(VERSION_PKG).Version=$(VERSION) -X $(VERSION_PKG).Commit=$(COMMIT) -X $(VERSION_PKG).BuildDate=$(BUILD_TIME)"
LDFLAGS_DEBUG := -ldflags "-X $(VERSION_PKG).Version=$(VERSION) -X $(VERSION_PKG).Commit=$(COMMIT) -X $(VERSION_PKG).BuildDate=$(BUILD_TIME)"

# Directories
BUILD_DIR := build
CMD_DIR := cmd/bentoo
INSTALL_DIR := /usr/local/bin

# User config install (honors XDG_CONFIG_HOME, matching internal/common/config)
CONFIG_EXAMPLE := config.example.yaml
CONFIG_DIR := $(if $(XDG_CONFIG_HOME),$(XDG_CONFIG_HOME),$(HOME)/.config)/bentoo
CONFIG_FILE := $(CONFIG_DIR)/config.yaml

# Go commands
GO := go
GOTEST := $(GO) test
GOBUILD := $(GO) build
GOMOD := $(GO) mod

# Default target
.PHONY: all
all: build

# Build the binary
.PHONY: build
build:
	mkdir -p $(BUILD_DIR)
	$(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) ./$(CMD_DIR)

# Build with debug symbols (no stripping)
.PHONY: build-debug
build-debug:
	mkdir -p $(BUILD_DIR)
	$(GOBUILD) $(LDFLAGS_DEBUG) -o $(BUILD_DIR)/$(BINARY_NAME) ./$(CMD_DIR)

# Install to system
.PHONY: install
install: build
	install -Dm755 $(BUILD_DIR)/$(BINARY_NAME) $(DESTDIR)$(INSTALL_DIR)/$(BINARY_NAME)

# Uninstall from system
.PHONY: uninstall
uninstall:
	rm -f $(DESTDIR)$(INSTALL_DIR)/$(BINARY_NAME)

# Install the example config into the user's config dir.
# Never overwrites an existing config; writes 0600 (may hold tokens), matching
# what the app itself does when it creates a config.
.PHONY: install-config
install-config:
	@if [ -f "$(CONFIG_FILE)" ]; then \
		echo "install-config: $(CONFIG_FILE) already exists, leaving it untouched"; \
	else \
		install -Dm600 $(CONFIG_EXAMPLE) "$(CONFIG_FILE)"; \
		echo "install-config: wrote $(CONFIG_FILE) (edit it and set overlay.path)"; \
	fi

# Run tests
.PHONY: test
test:
	$(GOTEST) -v ./...

# Run tests with coverage
.PHONY: coverage
coverage:
	$(GOTEST) -v -coverprofile=coverage.out ./...
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

# Audit the context spine: no naked context.Background() may appear in
# internal/autoupdate or internal/overlay outside of test files. The naive
# grep yields false positives — doc-comment lines that merely mention the
# string context.Background() in prose, and the GetWithHeaders convenience
# wrapper which carries an explicit "// SAFE:" justification. The chained
# filters drop, in order: test files, "// SAFE:"-annotated lines, and any
# line whose match is inside a pure comment (the grep -vE ':<lineno>:<ws>//'
# filter). A surviving hit is a genuine naked context.Background().
.PHONY: audit-ctx
audit-ctx:
	@set -e; \
	raw_hits="$$(grep -rn "context\.Background()" internal/autoupdate internal/overlay --include='*.go' || true)"; \
	no_tests="$$(printf '%s\n' "$$raw_hits" | grep -v "_test.go" || true)"; \
	no_safe="$$(printf '%s\n' "$$no_tests" | grep -v "// SAFE:" || true)"; \
	real_hits="$$(printf '%s\n' "$$no_safe" | grep -vE '^[^:]+:[0-9]+:[[:space:]]*//' || true)"; \
	if [ -n "$$real_hits" ]; then \
		echo "audit-ctx: naked context.Background() found"; \
		exit 1; \
	else \
		echo "audit-ctx: no naked context.Background() in internal/autoupdate or internal/overlay"; \
	fi

# Security audit
.PHONY: audit
audit: audit-ctx
	$(GOMOD) verify
	@echo "Module verification passed"
	@if command -v govulncheck >/dev/null 2>&1; then \
		govulncheck ./...; \
	else \
		echo "govulncheck not installed, skipping vulnerability check"; \
		echo "Install with: go install golang.org/x/vuln/cmd/govulncheck@latest"; \
	fi

# Clean build artifacts
.PHONY: clean
clean:
	rm -f $(BINARY_NAME)
	rm -f coverage.out coverage.html
	rm -rf $(BUILD_DIR)

# Cross-compilation targets. CGO is disabled so these build on any host without a
# target C cross-toolchain (bentoo is pure Go); the result is a static binary,
# which is what we want to ship.
.PHONY: build-all
build-all: build-linux-amd64 build-linux-arm64

.PHONY: build-linux-amd64
build-linux-amd64:
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 ./$(CMD_DIR)

.PHONY: build-linux-arm64
build-linux-arm64:
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-arm64 ./$(CMD_DIR)

# Development helpers
.PHONY: fmt
fmt:
	$(GO) fmt ./...

.PHONY: vet
vet:
	$(GO) vet ./...

.PHONY: lint
lint: fmt vet
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not installed, running basic checks only"; \
		echo "Install with: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"; \
	fi

# Tidy dependencies
.PHONY: tidy
tidy:
	$(GOMOD) tidy

# Run all checks (lint, test, audit)
.PHONY: check
check: lint test audit

# Help target
.PHONY: help
help:
	@echo "Bentoolkit Makefile"
	@echo ""
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@echo "  build           Build the binary (default, stripped)"
	@echo "  build-debug     Build the binary with debug symbols"
	@echo "  install         Install to $(INSTALL_DIR)"
	@echo "  uninstall       Remove from $(INSTALL_DIR)"
	@echo "  install-config  Copy config.example.yaml to the user's config dir (no overwrite)"
	@echo "  test            Run tests"
	@echo "  coverage        Run tests with coverage report"
	@echo "  audit-ctx       Verify no naked context.Background() in internal/autoupdate, internal/overlay"
	@echo "  audit           Run security audit (audit-ctx + go mod verify + govulncheck)"
	@echo "  clean           Remove build artifacts"
	@echo "  build-all       Cross-compile for linux amd64 and arm64"
	@echo "  build-linux-amd64  Build for Linux amd64"
	@echo "  build-linux-arm64  Build for Linux arm64"
	@echo "  fmt             Format code"
	@echo "  vet             Run go vet"
	@echo "  lint            Run fmt, vet, and golangci-lint (if installed)"
	@echo "  tidy            Tidy dependencies"
	@echo "  check           Run lint, test, and audit"
	@echo "  help            Show this help"
