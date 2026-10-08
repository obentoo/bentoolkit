#!/usr/bin/env bash
# Runs INSIDE the gate VM (see scripts/ci-vm-gate.sh). Do not run it on your machine.
#
# Reproduces the Go jobs of .github/workflows/ci.yml on one clean checkout of
# the gated commit: Lint, Test, Audit and Build. Every job writes <job>.log and
# <job>.rc ("<exit code> <seconds>") into the log directory; the host script
# reads the verdict from those files.
#
# Test runs alone first: the -race suite has timing-sensitive tests, and
# competing with golangci-lint for 8 vCPUs is a flake the real runner, which
# gives each job its own machine, never sees. Audit is light and shares that
# window; Lint and Build run side by side afterwards.
#
# Usage (from the host script): ci-vm-gate-inner.sh <workspace> <log-dir> <go-toolchain>
set -uo pipefail

WS=${1:?workspace}
LOG_DIR=${2:?log dir}
export GOTOOLCHAIN=${3:?go toolchain, e.g. go1.26.8}
# The D-Bus integration tests fail instead of skipping when CI=true and
# dbus-daemon is missing, exactly as on the runner.
export CI=true
# Test temp dirs on tmpfs. The guest disk is a qcow2 file on the host's btrfs,
# where one fsync costs ~27 ms (200 synced 4 KiB writes: 5.4 s). Tests that
# write state with crash-safe writes then crawl: TestBenchmarkSpeedup took
# 1.7-4.7 s against 0.1 s on the host, whose /tmp is tmpfs, and timed out at
# 5 s in 2 of 30 runs. With TMPDIR here it passed 30 of 30 at 0.1 s.
export TMPDIR=/dev/shm/bentoolkit-gate
rm -rf "$TMPDIR" && mkdir -p "$TMPDIR"
mkdir -p "$LOG_DIR"
cd "$WS" || exit 1

# The golangci-lint pin lives in the Makefile, and `lint-pin-check` keeps it
# equal to the one ci.yml installs.
LINT_VERSION=$(sed -n 's/^GOLANGCI_LINT_VERSION := //p' Makefile)
LINT=(go run "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$LINT_VERSION")

job() { # <name> <command...> — run one job, recording its exit code and duration
  local name=$1 start rc
  shift
  start=$(date +%s)
  "$@" >"$LOG_DIR/$name.log" 2>&1
  rc=$?
  echo "$rc $(($(date +%s) - start))" >"$LOG_DIR/$name.rc"
}

lint() {
  "${LINT[@]}" run ./... &&
    "${LINT[@]}" run --build-tags chromedp ./... &&
    make audit-ctx &&
    make audit-comments &&
    make audit-pkgdoc
}

test_job() {
  go test -race -shuffle=on -v -coverprofile=coverage.out ./... || return 1
  local coverage
  coverage=$(go tool cover -func=coverage.out | tail -1 | awk '{print $3}' | tr -d '%')
  echo "Total coverage: ${coverage}%"
  if (($(echo "$coverage < 80" | bc -l))); then
    echo "Coverage ${coverage}% is below the 80% minimum"
    return 1
  fi
}

audit() {
  go mod verify &&
    go tool govulncheck ./... &&
    go tool govulncheck -tags chromedp ./...
}

build() {
  make build-all &&
    go build -tags chromedp ./... &&
    go vet -tags chromedp ./...
}

# Warm the toolchain and module cache once, so parallel jobs do not race to
# download the same files.
go version >"$LOG_DIR/setup.log" 2>&1 && go mod download >>"$LOG_DIR/setup.log" 2>&1

job test test_job &
job audit audit &
wait
job lint lint &
job build build &
wait
