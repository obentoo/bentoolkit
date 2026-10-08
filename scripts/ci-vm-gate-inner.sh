#!/usr/bin/env bash
# Runs INSIDE the gate VM (see scripts/ci-vm-gate.sh). Do not run it on your machine.
#
# Reproduces the Go jobs of .github/workflows/ci.yml on one clean checkout of
# the gated commit: Lint, Test, Audit and Build, plus CodeQL, which GitHub's
# default setup used to run. Every job writes <job>.log and
# <job>.rc ("<exit code> <seconds>") into the log directory; the host script
# reads the verdict from those files.
#
# Test runs alone first: the -race suite has timing-sensitive tests, and
# competing with golangci-lint for 8 vCPUs is a flake the real runner, which
# gives each job its own machine, never sees. Audit is light and shares that
# window; Lint, Build and CodeQL run side by side afterwards.
#
# Usage (from the host script):
#   ci-vm-gate-inner.sh <workspace> <log-dir> <go-toolchain> <codeql-dismissed.tsv>
set -uo pipefail

WS=${1:?workspace}
LOG_DIR=${2:?log dir}
export GOTOOLCHAIN=${3:?go toolchain, e.g. go1.26.8}
DISMISSED=${4:?codeql dismissed-alerts file}
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
    # A gated commit older than the audit-pkgdoc target has no such rule;
    # gating it must not fail on the script's own age.
    if make -n audit-pkgdoc >/dev/null 2>&1; then make audit-pkgdoc; fi
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

# CodeQL, pinned by version and by the SHA-256 GitHub publishes for each
# per-language bundle. A version is taken once it is at least 7 days old.
CODEQL_VERSION=2.27.1
CODEQL_GO_SHA256=c7fd9efd7bdd93b89a6d8daa6023611156be13608042c39ae6701722a1fef04e
CODEQL_ACTIONS_SHA256=71247327cf3afe4115acc546fe3ac77f2865928205d7688d6a0b7153e055603f

codeql_bin() { # <language> <sha256>: install the bundle once, print its CLI path
  local lang=$1 sha=$2 dir="$HOME/gate/tools/codeql-$1-$CODEQL_VERSION" file
  if [[ ! -x "$dir/codeql/codeql" ]]; then
    file=$(mktemp)
    curl -fsSL --retry 3 -o "$file" \
      "https://github.com/github/codeql-action/releases/download/codeql-bundle-v$CODEQL_VERSION/codeql-bundle-$lang-linux64.tar.zst" &&
      echo "$sha  $file" | sha256sum -c --quiet &&
      mkdir -p "$dir" && tar --zstd -xf "$file" -C "$dir"
    local rc=$?
    rm -f "$file"
    ((rc == 0)) || return 1
  fi
  echo "$dir/codeql/codeql"
}

# codeql_scan <language> <sha256> <suite> [create options...]: build the
# database and write <language>.sarif. The suite is the one GitHub's default
# setup runs, so local and hosted results are the same set.
codeql_scan() {
  local lang=$1 sha=$2 suite=$3 bin db="$TMPDIR/codeql-db-$1"
  shift 3
  bin=$(codeql_bin "$lang" "$sha") || { echo "codeql: installing the $lang bundle failed"; return 1; }
  "$bin" database create "$db" --language="$lang" --source-root=. --overwrite -q "$@" &&
    "$bin" database analyze "$db" "$suite" --format=sarif-latest -q \
      --output="$LOG_DIR/codeql-$lang.sarif"
}

# Every result must be listed in $DISMISSED (rule, path, CodeQL's line-shift
# stable fingerprint, reason), and every entry must still match a result, so
# the list only shrinks. That file is what GitHub's "dismiss" button was.
codeql() {
  codeql_scan go "$CODEQL_GO_SHA256" codeql/go-queries:codeql-suites/go-code-scanning.qls \
    --command="go build ./..." --command="go build -tags chromedp ./..." || return 1
  codeql_scan actions "$CODEQL_ACTIONS_SHA256" \
    codeql/actions-queries:codeql-suites/actions-code-scanning.qls || return 1

  local found expected new stale
  found=$(jq -r '.runs[].results[] | [.ruleId, .locations[0].physicalLocation.artifactLocation.uri,
      .partialFingerprints.primaryLocationLineHash] | @tsv' "$LOG_DIR"/codeql-*.sarif | sort)
  expected=$(grep -v '^#' "$DISMISSED" | grep -v '^[[:space:]]*$' | cut -f1-3 | sort)
  new=$(comm -23 <(printf '%s\n' "$found") <(printf '%s\n' "$expected") | grep -v '^$' || true)
  stale=$(comm -13 <(printf '%s\n' "$found") <(printf '%s\n' "$expected") | grep -v '^$' || true)
  echo "codeql: $(printf '%s\n' "$found" | grep -c . || true) result(s), all listed in codeql-dismissed.tsv unless named below"
  if [[ -n "$new" ]]; then
    echo "codeql: new result(s); fix them, or dismiss with a reason in scripts/codeql-dismissed.tsv:"
    jq -r '.runs[].results[] | "  \(.ruleId) \(.locations[0].physicalLocation.artifactLocation.uri):\(.locations[0].physicalLocation.region.startLine) \(.partialFingerprints.primaryLocationLineHash): \(.message.text | split("\n")[0])"' \
      "$LOG_DIR"/codeql-*.sarif | grep -F -f <(printf '%s\n' "$new" | cut -f3)
  fi
  if [[ -n "$stale" ]]; then
    echo "codeql: dismissed entries that no longer match a result; remove them:"
    printf '  %s\n' "$stale"
  fi
  [[ -z "$new" && -z "$stale" ]]
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
job codeql codeql &
wait
