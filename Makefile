# Bentoolkit Makefile
# Build, test, and install targets for the bentoo CLI and the bentoo-tray notifier

BINARY_NAME := bentoo
TRAY_BINARY := bentoo-tray
MODULE := github.com/obentoo/bentoolkit
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
# Build time stamped into version.BuildDate. It comes from SOURCE_DATE_EPOCH
# (the reproducible-builds.org convention), else from the last commit, so two
# builds of one commit are byte-identical; the wall clock is used only when
# neither exists (a source tarball built with no git and no SOURCE_DATE_EPOCH).
SOURCE_DATE_EPOCH ?= $(shell git log -1 --format=%ct 2>/dev/null)
BUILD_TIME := $(if $(SOURCE_DATE_EPOCH),$(shell date -u -d '@$(SOURCE_DATE_EPOCH)' '+%Y-%m-%d_%H:%M:%S'),$(shell date -u '+%Y-%m-%d_%H:%M:%S'))
VERSION_PKG := $(MODULE)/internal/common/version
LDFLAGS := -ldflags "-s -w -X $(VERSION_PKG).Version=$(VERSION) -X $(VERSION_PKG).Commit=$(COMMIT) -X $(VERSION_PKG).BuildDate=$(BUILD_TIME)"
LDFLAGS_DEBUG := -ldflags "-X $(VERSION_PKG).Version=$(VERSION) -X $(VERSION_PKG).Commit=$(COMMIT) -X $(VERSION_PKG).BuildDate=$(BUILD_TIME)"

# Directories
BUILD_DIR := build
CMD_DIR := cmd/bentoo
TRAY_CMD_DIR := cmd/bentoo-tray
PREFIX ?= /usr/local
INSTALL_DIR := $(PREFIX)/bin
# bentoo-tray's session files: desktop entry, user unit and scalable icons.
TRAY_MISC_DIR := misc/tray
APPLICATIONS_DIR := $(PREFIX)/share/applications
ICONS_DIR := $(PREFIX)/share/icons/hicolor/scalable/apps
SYSTEMD_USER_DIR := $(PREFIX)/lib/systemd/user

# User config install (honors XDG_CONFIG_HOME, matching internal/common/config)
CONFIG_EXAMPLE := config.example.yaml
CONFIG_DIR := $(if $(XDG_CONFIG_HOME),$(XDG_CONFIG_HOME),$(HOME)/.config)/bentoo
CONFIG_FILE := $(CONFIG_DIR)/config.yaml

# Go commands. Every target runs the toolchain go.mod names, as CI does
# (setup-go reads go.mod): a newer host Go formats and vets differently. Set
# GOTOOLCHAIN in the environment to override.
GO_TOOLCHAIN := $(shell awk '/^toolchain /{print $$2}' go.mod)
export GOTOOLCHAIN ?= $(GO_TOOLCHAIN)
GO := go
GOTEST := $(GO) test
# -trimpath keeps the checkout's absolute path out of the binaries, so where the
# tree was cloned does not change them.
GOBUILD := $(GO) build -trimpath
GOMOD := $(GO) mod

# golangci-lint at the version the CI Lint job installs, built with the toolchain
# exported above — the one CI uses; built with a newer host Go, its gofmt
# disagrees with CI's. lint-pin-check keeps this pin and the CI one equal.
GOLANGCI_LINT_VERSION := v2.13.2
GOLANGCI_LINT := $(GO) run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
# Extra golangci-lint arguments, e.g. LINT_ARGS="--enable-only misspell".
LINT_ARGS ?=

# Tray icons: each SVG variant in TRAY_ICON_SVG_DIR is rendered at every size
# in TRAY_ICON_SIZES into TRAY_ICON_PNG_DIR as <variant>-<size>.png, the names
# internal/tray/icons embeds. Keep TRAY_ICON_SIZES equal to that package's sizes.
TRAY_ICON_SVG_DIR := misc/tray/icons
TRAY_ICON_PNG_DIR := internal/tray/icons/png
TRAY_ICON_VARIANTS := bentoo-tray bentoo-tray-unread bentoo-tray-critical
TRAY_ICON_SIZES := 16 22 24 32 48
RSVG_CONVERT ?= rsvg-convert

# Test order: `on` draws a fresh seed per run; a failing run prints
# `-test.shuffle <seed>`, and `make test SHUFFLE=<seed>` replays that order.
SHUFFLE ?= on
# How long `make fuzz` runs each fuzz target.
FUZZTIME ?= 30s

# Default target
.PHONY: all
all: build

# Build the binaries. bentoo-tray is always pure Go (CGO_ENABLED=0): its D-Bus
# client needs no C library.
.PHONY: build
build:
	mkdir -p $(BUILD_DIR)
	$(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) ./$(CMD_DIR)
	CGO_ENABLED=0 $(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(TRAY_BINARY) ./$(TRAY_CMD_DIR)

# Build with debug symbols (no stripping)
.PHONY: build-debug
build-debug:
	mkdir -p $(BUILD_DIR)
	$(GOBUILD) $(LDFLAGS_DEBUG) -o $(BUILD_DIR)/$(BINARY_NAME) ./$(CMD_DIR)
	CGO_ENABLED=0 $(GOBUILD) $(LDFLAGS_DEBUG) -o $(BUILD_DIR)/$(TRAY_BINARY) ./$(TRAY_CMD_DIR)

# Install to system under DESTDIR/PREFIX: both binaries, plus bentoo-tray's
# desktop entry, user unit (ExecStart pointing at the installed binary) and its
# three scalable icons.
.PHONY: install
install: build
	install -Dm755 $(BUILD_DIR)/$(BINARY_NAME) $(DESTDIR)$(INSTALL_DIR)/$(BINARY_NAME)
	install -Dm755 $(BUILD_DIR)/$(TRAY_BINARY) $(DESTDIR)$(INSTALL_DIR)/$(TRAY_BINARY)
	install -Dm644 $(TRAY_MISC_DIR)/$(TRAY_BINARY).desktop $(DESTDIR)$(APPLICATIONS_DIR)/$(TRAY_BINARY).desktop
	install -d $(DESTDIR)$(SYSTEMD_USER_DIR)
	sed 's|@BINDIR@|$(INSTALL_DIR)|g' $(TRAY_MISC_DIR)/$(TRAY_BINARY).service.in > $(DESTDIR)$(SYSTEMD_USER_DIR)/$(TRAY_BINARY).service
	chmod 644 $(DESTDIR)$(SYSTEMD_USER_DIR)/$(TRAY_BINARY).service
	@set -eu; for variant in $(TRAY_ICON_VARIANTS); do \
		install -Dm644 "$(TRAY_ICON_SVG_DIR)/$$variant.svg" "$(DESTDIR)$(ICONS_DIR)/$$variant.svg"; \
	done

# Uninstall from system
.PHONY: uninstall
uninstall:
	rm -f $(DESTDIR)$(INSTALL_DIR)/$(BINARY_NAME) $(DESTDIR)$(INSTALL_DIR)/$(TRAY_BINARY)
	rm -f $(DESTDIR)$(APPLICATIONS_DIR)/$(TRAY_BINARY).desktop
	rm -f $(DESTDIR)$(SYSTEMD_USER_DIR)/$(TRAY_BINARY).service
	@set -eu; for variant in $(TRAY_ICON_VARIANTS); do \
		rm -f "$(DESTDIR)$(ICONS_DIR)/$$variant.svg"; \
	done

# Install the example config into the user's config dir.
# Never overwrites an existing config; writes 0600 as a defensive default (the
# config holds no secrets — those live in the separate ~/.config/bentoo/secrets
# file), matching what the app itself does when it creates a config.
.PHONY: install-config
install-config:
	@if [ -f "$(CONFIG_FILE)" ]; then \
		echo "install-config: $(CONFIG_FILE) already exists, leaving it untouched"; \
	else \
		install -Dm600 $(CONFIG_EXAMPLE) "$(CONFIG_FILE)"; \
		echo "install-config: wrote $(CONFIG_FILE) (edit it and set overlay.path)"; \
	fi

# Run tests with the race detector, in shuffled order
.PHONY: test
test:
	$(GOTEST) -race -shuffle=$(SHUFFLE) -v ./...

# Run tests with coverage (race detector and shuffled order, like `test`)
.PHONY: coverage
coverage:
	$(GOTEST) -race -shuffle=$(SHUFFLE) -v -coverprofile=coverage.out ./...
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

# Run every fuzz target for FUZZTIME each. `go test -list` prints a package's
# matching names and then its `ok <pkg>` line, so each name is paired with the
# package on the next `ok` line. -fuzz takes exactly one target and one package
# per run, hence the loop; the first failure stops it, naming the target.
.PHONY: fuzz
fuzz:
	@set -eu; \
	targets="$$($(GOTEST) -list '^Fuzz' ./... | awk '/^Fuzz/ { names = names " " $$1; next } /^ok / { n = split(names, a, " "); for (i = 1; i <= n; i++) print $$2 " " a[i]; names = "" }')"; \
	if [ -z "$$targets" ]; then echo "fuzz: no fuzz targets found"; exit 1; fi; \
	printf '%s\n' "$$targets" | while read -r pkg name; do \
		echo "fuzz: $$name ($$pkg) for $(FUZZTIME)"; \
		$(GOTEST) -run '^$$' -fuzz "^$$name$$" -fuzztime $(FUZZTIME) "$$pkg" || { echo "fuzz: $$name in $$pkg FAILED"; exit 1; }; \
	done

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

# Audit production comments and strings: comments must stand on their own for a
# reader who has no access to the gitignored planning notes, so a tracker ID
# (S058, S042-D7, R4.4, "(D9)", "sub-task 13.4", "story 060") in a non-test Go
# file fails, and so does a run of 20+ consecutive // lines unless it is the
# package doc comment directly above the package clause. A false positive is
# fixed by rewording the line: there is no allowlist. A missing directory is an
# error, never a clean result.
AUDIT_COMMENTS_DIRS ?= cmd internal
AUDIT_COMMENTS_MAX_BLOCK := 20
AUDIT_COMMENTS_ID_RE := -e '\bS[0-9]{3}\b' \
	-e '\bS[0-9]{3}-[A-Z]+[0-9]+' \
	-e '\b[A-Z][0-9]{1,2}(\.[0-9]+)+\b' \
	-e '\([^)]*\b[A-HQRT][0-9]{1,2}\b[^)]*\)' \
	-e '\b[Ss]ub-?tasks? [0-9]+(\.[0-9]+)?\b' \
	-e '\b[Ss]tor(y|ies) [0-9]{3}\b'

.PHONY: audit-comments
audit-comments:
	@set -eu; \
	for dir in $(AUDIT_COMMENTS_DIRS); do \
		if [ ! -d "$$dir" ]; then echo "audit-comments: $$dir: no such directory" >&2; exit 2; fi; \
	done; \
	files="$$(find $(AUDIT_COMMENTS_DIRS) -type f -name '*.go' ! -name '*_test.go' | LC_ALL=C sort)"; \
	ids=""; blocks=""; \
	if [ -n "$$files" ]; then \
		ids="$$(printf '%s\n' "$$files" | xargs -d '\n' grep -nHE $(AUDIT_COMMENTS_ID_RE) -- | sed -E 's/^([^:]+:[0-9]+):/\1: tracker ID: /' || true)"; \
		blocks="$$(printf '%s\n' "$$files" | xargs -d '\n' awk -v max=$(AUDIT_COMMENTS_MAX_BLOCK) ' \
			function flush(next_line) { \
				if (run >= max && next_line !~ /^package[ \t]/) printf "%s:%d: comment block of %d lines\n", bfile, start, run; \
				run = 0 \
			} \
			FNR == 1 && NR > 1 { flush("") } \
			/^[ \t]*\/\// { if (run == 0) { start = FNR; bfile = FILENAME }; run++; next } \
			{ flush($$0) } \
			END { flush("") }')"; \
	fi; \
	if [ -n "$$ids$$blocks" ]; then \
		[ -z "$$ids" ] || printf '%s\n' "$$ids"; \
		[ -z "$$blocks" ] || printf '%s\n' "$$blocks"; \
		exit 1; \
	fi; \
	echo "audit-comments: no tracker IDs and no comment block of $(AUDIT_COMMENTS_MAX_BLOCK)+ lines in $(AUDIT_COMMENTS_DIRS)"

# Security audit
.PHONY: audit
audit: audit-ctx
	$(GOMOD) verify
	@echo "Module verification passed"
	@# govulncheck is supplied by the `tool` directive in go.mod, so it lives in
	@# the module -- not on PATH. The old `command -v` probe therefore never found
	@# it and this target printed "skipping" and went green without ever scanning.
	go tool govulncheck ./...

# Clean build artifacts
.PHONY: clean
clean:
	rm -f coverage.out coverage.html cov.out coverage*.out $(BINARY_NAME) $(TRAY_BINARY)
	rm -rf $(BUILD_DIR)

# Write build/SHA256SUMS over every binary in build/, with bare file names so
# `cd build && sha256sum -c SHA256SUMS` verifies them. Run after a build target.
.PHONY: checksums
checksums:
	@set -eu; \
	files="$$(find $(BUILD_DIR) -maxdepth 1 -type f ! -name SHA256SUMS -printf '%f\n' 2>/dev/null | LC_ALL=C sort)"; \
	if [ -z "$$files" ]; then echo "checksums: no binaries in $(BUILD_DIR)/ (run make build or make build-all first)"; exit 1; fi; \
	cd $(BUILD_DIR) && printf '%s\n' "$$files" | xargs sha256sum > SHA256SUMS; \
	echo "checksums: wrote $(BUILD_DIR)/SHA256SUMS"

# Cross-compilation targets. CGO is disabled so these build on any host without a
# target C cross-toolchain (both binaries are pure Go); the result is a static
# binary, which is what we want to ship.
.PHONY: build-all
build-all: build-linux-amd64 build-linux-arm64

.PHONY: build-linux-amd64
build-linux-amd64:
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 ./$(CMD_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(TRAY_BINARY)-linux-amd64 ./$(TRAY_CMD_DIR)

.PHONY: build-linux-arm64
build-linux-arm64:
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-arm64 ./$(CMD_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(TRAY_BINARY)-linux-arm64 ./$(TRAY_CMD_DIR)

# Development helpers
.PHONY: fmt
fmt:
	$(GO) fmt ./...

.PHONY: vet
vet:
	$(GO) vet ./...

.PHONY: lint
lint: fmt vet lint-pin-check
	@set -e; \
	for tag in "" chromedp; do \
		echo "golangci-lint $(GOLANGCI_LINT_VERSION) (tags: $${tag:-none})"; \
		if [ -z "$$tag" ]; then \
			$(GOLANGCI_LINT) run $(LINT_ARGS) ./...; \
		else \
			$(GOLANGCI_LINT) run $(LINT_ARGS) --build-tags "$$tag" ./...; \
		fi; \
	done

# Fail when the golangci-lint version above differs from the one CI installs.
.PHONY: lint-pin-check
lint-pin-check:
	@ci="$$(grep -o 'golangci-lint@v[0-9][0-9.]*' .github/workflows/ci.yml | head -n 1 | sed 's/.*@//')"; \
	if [ -z "$$ci" ]; then \
		echo "cannot find the golangci-lint pin in .github/workflows/ci.yml"; exit 1; \
	fi; \
	if [ "$$ci" != "$(GOLANGCI_LINT_VERSION)" ]; then \
		echo "golangci-lint pin mismatch: Makefile $(GOLANGCI_LINT_VERSION), ci.yml $$ci"; exit 1; \
	fi

# Render the tray icon PNGs from their SVG sources. rsvg-convert (librsvg) is
# an authoring-time tool only: the PNGs are committed, so neither the Go build,
# CI nor the ebuild needs it. Old PNGs are removed first, so a dropped variant
# or size cannot linger in the embedded set. Re-run after editing an SVG.
.PHONY: tray-icons
tray-icons:
	@command -v $(RSVG_CONVERT) >/dev/null 2>&1 || { \
		echo "tray-icons: $(RSVG_CONVERT) not found (install librsvg)"; exit 1; }
	@set -eu; \
	mkdir -p $(TRAY_ICON_PNG_DIR); \
	rm -f $(TRAY_ICON_PNG_DIR)/*.png; \
	for v in $(TRAY_ICON_VARIANTS); do \
		for n in $(TRAY_ICON_SIZES); do \
			$(RSVG_CONVERT) -w "$$n" -h "$$n" -o "$(TRAY_ICON_PNG_DIR)/$$v-$$n.png" "$(TRAY_ICON_SVG_DIR)/$$v.svg"; \
		done; \
	done; \
	echo "tray-icons: rendered $(TRAY_ICON_VARIANTS) at $(TRAY_ICON_SIZES) px into $(TRAY_ICON_PNG_DIR)"

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
	@echo "  build           Build bentoo and bentoo-tray (default, stripped)"
	@echo "  build-debug     Build both binaries with debug symbols"
	@echo "  install         Install both binaries to $(INSTALL_DIR) and bentoo-tray's desktop entry, user unit and icons under $(PREFIX)"
	@echo "  uninstall       Remove everything install put under DESTDIR/PREFIX"
	@echo "  install-config  Copy config.example.yaml to the user's config dir (no overwrite)"
	@echo "  test            Run tests with -race in shuffled order (SHUFFLE=<seed> replays an order)"
	@echo "  coverage        Run tests with coverage report (-race, shuffled)"
	@echo "  fuzz            Run every fuzz target for FUZZTIME each (default 30s)"
	@echo "  audit-ctx       Verify no naked context.Background() in internal/autoupdate, internal/overlay"
	@echo "  audit           Run security audit (audit-ctx + go mod verify + govulncheck)"
	@echo "  clean           Remove build artifacts"
	@echo "  build-all       Cross-compile for linux amd64 and arm64"
	@echo "  checksums       Write build/SHA256SUMS over the binaries in build/"
	@echo "  build-linux-amd64  Build for Linux amd64"
	@echo "  build-linux-arm64  Build for Linux arm64"
	@echo "  fmt             Format code"
	@echo "  vet             Run go vet"
	@echo "  lint            Run fmt, vet, and golangci-lint $(GOLANGCI_LINT_VERSION) (the CI pin) for every build tag"
	@echo "  tray-icons      Render the tray icon PNGs from misc/tray/icons (needs rsvg-convert)"
	@echo "  tidy            Tidy dependencies"
	@echo "  check           Run lint, test, and audit"
	@echo "  help            Show this help"
