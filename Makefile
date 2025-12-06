# Bentoo Tools Makefile
# Build, test, and install targets for bentoo CLI

BINARY_NAME := bentoo
MODULE := github.com/lucascouts/bentoo-tools
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME := $(shell date -u '+%Y-%m-%d_%H:%M:%S')
LDFLAGS := -ldflags "-X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TIME)"

# Directories
BUILD_DIR := build
CMD_DIR := cmd/bentoo
INSTALL_DIR := /usr/local/bin

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
	$(GOBUILD) $(LDFLAGS) -o $(BINARY_NAME) ./$(CMD_DIR)

# Install to system
.PHONY: install
install: build
	install -Dm755 $(BINARY_NAME) $(DESTDIR)$(INSTALL_DIR)/$(BINARY_NAME)

# Uninstall from system
.PHONY: uninstall
uninstall:
	rm -f $(DESTDIR)$(INSTALL_DIR)/$(BINARY_NAME)

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

# Security audit
.PHONY: audit
audit:
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

# Cross-compilation targets
.PHONY: build-all
build-all: build-linux-amd64 build-linux-arm64

.PHONY: build-linux-amd64
build-linux-amd64:
	@mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=amd64 $(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 ./$(CMD_DIR)

.PHONY: build-linux-arm64
build-linux-arm64:
	@mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=arm64 $(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-arm64 ./$(CMD_DIR)

# Development helpers
.PHONY: fmt
fmt:
	$(GO) fmt ./...

.PHONY: vet
vet:
	$(GO) vet ./...

.PHONY: lint
lint: fmt vet

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
	@echo "Bentoo Tools Makefile"
	@echo ""
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@echo "  build           Build the binary (default)"
	@echo "  install         Install to $(INSTALL_DIR)"
	@echo "  uninstall       Remove from $(INSTALL_DIR)"
	@echo "  test            Run tests"
	@echo "  coverage        Run tests with coverage report"
	@echo "  audit           Run security audit (go mod verify + govulncheck)"
	@echo "  clean           Remove build artifacts"
	@echo "  build-all       Cross-compile for linux amd64 and arm64"
	@echo "  build-linux-amd64  Build for Linux amd64"
	@echo "  build-linux-arm64  Build for Linux arm64"
	@echo "  fmt             Format code"
	@echo "  vet             Run go vet"
	@echo "  lint            Run fmt and vet"
	@echo "  tidy            Tidy dependencies"
	@echo "  check           Run lint, test, and audit"
	@echo "  help            Show this help"
