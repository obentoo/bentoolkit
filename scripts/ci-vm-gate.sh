#!/usr/bin/env bash
# The local CI gate. Everything .github/workflows/ci.yml checks runs here,
# before a push, and its result is the evidence the work is done; pushing
# publishes verified work, it is not how to find out whether it passes.
#
# The VM is created once with scripts/ci-vm-create.sh.
#
#   ./scripts/ci-vm-gate.sh                 # gate HEAD (committed work only)
#   ./scripts/ci-vm-gate.sh <ref>           # gate any branch or commit, e.g. a Dependabot PR
#   ./scripts/ci-vm-gate.sh --status <ref>  # also post the verdict as a commit status
#
# What runs where:
#   VM   — Lint, Test, Audit and Build, as ci.yml runs them
#          (scripts/ci-vm-gate-inner.sh), on the KVM guest `bentoolkit-ci`:
#          Ubuntu 24.04 like ubuntu-latest, a non-root user like the runner
#          (several tests chmod a directory to simulate a write failure, and
#          root ignores mode bits), 8 vCPU / 12 GB, and caches kept between runs;
#   host — Secret Scan (gitleaks), OSV Scan, Workflow Lint (zizmor) and the
#          Changelog gate, in a clean worktree of the same commit.
#
# Why a VM and not `act`: the image and the Go caches survive between runs, the
# guest cannot take more than its cap from the host, and it is the same
# operating system the hosted runner uses.
#
# Why no self-hosted GitHub runner on that VM: the repository is public, and a
# self-hosted runner on a public repository runs the code of any fork's pull
# request on this machine.
#
# The VM is shut down 30 s after the gate ends unless
# ~/.local/share/bentoolkit-ci/hold exists.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

VM="${GATE_VM:-bentoolkit-ci}"
VM_USER="${GATE_VM_USER:-runner}"
VM_KEY="${GATE_VM_KEY:-$HOME/.ssh/ci_runner}"
CONN="qemu:///system"
STATE_DIR="$HOME/.local/share/bentoolkit-ci"
LOG_ROOT="${GATE_LOG_ROOT:-$STATE_DIR/logs}"
BASE="${GATE_BASE:-origin/main}"
ZIZMOR_VERSION=1.25.2 # ci.yml's pin

POST_STATUS=false
if [[ "${1:-}" == --status ]]; then
  POST_STATUS=true
  shift
fi
REF="${1:-HEAD}"
SHA=$(git rev-parse --verify "$REF^{commit}")
SHORT=$(git rev-parse --short "$SHA")
TOOLCHAIN=$(git show "$SHA:go.mod" | sed -n 's/^toolchain //p')
LOG_DIR="$LOG_ROOT/$SHORT-$(date +%Y%m%d-%H%M%S)"
mkdir -p "$LOG_DIR"

log() { printf '%s %s\n' "$(date +%H:%M:%S)" "$*" | tee -a "$LOG_DIR/gate.log"; }
vm_state() { LC_ALL=C virsh --connect "$CONN" domstate "$VM" 2>/dev/null; }
vm_ip() {
  virsh --connect "$CONN" domifaddr "$VM" 2>/dev/null |
    awk '/ipv4/ { split($4, a, "/"); print a[1]; exit }'
}

if [[ "$REF" == HEAD ]] && { ! git diff --quiet || ! git diff --cached --quiet; }; then
  log "WARN uncommitted changes are NOT gated: the gate tests HEAD $SHORT only"
fi
[[ -n "$TOOLCHAIN" ]] || { log "FAIL: go.mod at $SHORT has no toolchain line"; exit 1; }
log "gating $REF ($SHORT) with $TOOLCHAIN"

shutdown_later() {
  sleep 30
  if [[ -f "$STATE_DIR/hold" ]]; then
    log "VM kept on ($STATE_DIR/hold exists)"
    return
  fi
  virsh --connect "$CONN" shutdown "$VM" >/dev/null 2>&1 && log "VM shutdown requested"
}
trap shutdown_later EXIT

# 1. VM up, and its address from the libvirt DHCP lease.
if [[ "$(vm_state)" != running ]]; then
  log "starting $VM"
  virsh --connect "$CONN" start "$VM" >/dev/null
fi
IP=""
for _ in $(seq 60); do
  IP=$(vm_ip)
  if [[ -n "$IP" ]] && ssh -i "$VM_KEY" -o BatchMode=yes -o ConnectTimeout=3 \
    "$VM_USER@$IP" true 2>/dev/null; then
    break
  fi
  sleep 2
done
VM_HOST="$VM_USER@$IP"
SSH=(ssh -i "$VM_KEY" -o BatchMode=yes -o ConnectTimeout=5 "$VM_HOST")
"${SSH[@]}" true || { log "FAIL: $VM not reachable over ssh"; exit 1; }

# 2. The commit, over ssh — never through GitHub — into a clean checkout.
# receive.shallowUpdate: a shallow local clone can only push what it has, and
# a bare repository refuses that by default ("shallow update not allowed").
"${SSH[@]}" 'mkdir -p ~/gate && { [ -d ~/gate/repo.git ] || git init -q --bare ~/gate/repo.git; } &&
  git -C ~/gate/repo.git config receive.shallowUpdate true &&
  git -C ~/gate/repo.git symbolic-ref HEAD refs/heads/gate'
GIT_SSH_COMMAND="ssh -i $VM_KEY -o BatchMode=yes" \
  git push -q --force "ssh://$VM_HOST/home/$VM_USER/gate/repo.git" "$SHA:refs/heads/gate" \
  2>>"$LOG_DIR/gate.log" || { log "FAIL: could not push $SHORT to the VM (see $LOG_DIR/gate.log)"; exit 1; }
"${SSH[@]}" bash -s -- "$SHA" <<'REMOTE' || { log "FAIL: could not check out $SHORT in the VM"; exit 1; }
set -euo pipefail
d=$HOME/gate/ws
[ -d "$d/.git" ] || git clone -q "$HOME/gate/repo.git" "$d"
git -C "$d" fetch -q origin gate
git -C "$d" checkout -q --force --detach "$1"
git -C "$d" clean -q -ffdx
REMOTE
log "pushed $SHORT to the VM"

# 3. VM jobs and host scanners, side by side. The inner script is THIS
#    checkout's copy, so a branch that predates the script can still be gated.
REMOTE_LOG="/home/$VM_USER/gate/logs/$SHORT"
scp -q -i "$VM_KEY" "$REPO_ROOT/scripts/ci-vm-gate-inner.sh" "$VM_HOST:gate/inner.sh" ||
  { log "FAIL: could not copy the inner script to the VM"; exit 1; }
log "gate running (VM: test, audit, lint, build; host: secrets, osv, workflow-lint, changelog)"
"${SSH[@]}" "rm -rf '$REMOTE_LOG' && bash ~/gate/inner.sh ~/gate/ws '$REMOTE_LOG' '$TOOLCHAIN'" \
  >"$LOG_DIR/vm.log" 2>&1 &
VM_PID=$!

host_job() { # <name> <command...>
  local name=$1 start rc
  shift
  start=$(date +%s)
  "$@" >"$LOG_DIR/host-$name.log" 2>&1
  rc=$?
  echo "$rc $(($(date +%s) - start))" >"$LOG_DIR/host-$name.rc"
}

changelog_gate() { # the logic of ci.yml's Changelog job, against $BASE
  local branch pr changed
  local -a relevant
  git fetch -q --no-tags origin "${BASE#origin/}" || true
  branch=$(git -C "$REPO_ROOT" rev-parse --abbrev-ref "$REF" 2>/dev/null || true)
  pr=$(gh pr list --head "${branch#origin/}" --json number,labels --jq '.[0]' 2>/dev/null || true)
  if [[ -n "$pr" ]] && jq -e '.labels[]? | select(.name == "no-changelog")' <<<"$pr" >/dev/null; then
    echo "Labelled no-changelog; skipping."
    return 0
  fi
  changed=$(git diff --name-only "$BASE...$SHA")
  mapfile -t relevant < <(grep -E '^(go\.mod|go\.sum)$|\.go$' <<<"$changed" | grep -vE '_test\.go$' || true)
  if ((${#relevant[@]} == 0)); then
    echo "No user-visible change; changelog not required."
    return 0
  fi
  if grep -qx 'CHANGELOG.md' <<<"$changed"; then
    echo "Changelog updated alongside:"
    printf '  %s\n' "${relevant[@]}"
    return 0
  fi
  echo "This change ships but does not touch CHANGELOG.md:"
  printf '  %s\n' "${relevant[@]}"
  return 1
}

SCAN_WT=$(mktemp -d -t bentoolkit-gate-XXXX)
git worktree add -q --detach "$SCAN_WT" "$SHA"
(
  cd "$SCAN_WT"
  # --log-opts scopes gitleaks to the history this commit reaches, as the
  # runner's full-depth checkout does; without it, it scans every local branch.
  host_job secrets gitleaks git --redact --log-opts="$SHA" . &
  host_job osv osv-scanner scan source --recursive ./ &
  host_job workflow-lint uvx "zizmor==$ZIZMOR_VERSION" --min-severity=medium .github/workflows/ &
  host_job changelog changelog_gate &
  wait
)

VM_RC=0
wait "$VM_PID" || VM_RC=$?
git worktree remove --force "$SCAN_WT"
mkdir -p "$LOG_DIR/vm-logs"
scp -q -r -i "$VM_KEY" "$VM_HOST:$REMOTE_LOG/." "$LOG_DIR/vm-logs/" 2>/dev/null || true

# 4. Verdict from each job's exit code AND, for Test, the failure count: a
#    missing .rc means a job never finished, which is red, never green.
{
  echo "== gate $SHORT ($(date +%H:%M)), $TOOLCHAIN"
  for name in test audit lint build; do
    f="$LOG_DIR/vm-logs/$name.rc"
    if [[ ! -f "$f" ]]; then
      printf '  %-15s FAIL (did not finish)\n' "vm:$name"
      continue
    fi
    read -r rc secs <"$f"
    printf '  %-15s %s (%ss)\n' "vm:$name" "$([[ $rc == 0 ]] && echo PASS || echo FAIL)" "$secs"
  done
  for name in secrets osv workflow-lint changelog; do
    f="$LOG_DIR/host-$name.rc"
    if [[ ! -f "$f" ]]; then
      printf '  %-15s FAIL (did not finish)\n' "host:$name"
      continue
    fi
    read -r rc secs <"$f"
    printf '  %-15s %s (%ss)\n' "host:$name" "$([[ $rc == 0 ]] && echo PASS || echo FAIL)" "$secs"
  done
  grep -h 'Total coverage' "$LOG_DIR/vm-logs/test.log" 2>/dev/null | sed 's/^/  /' || true
  echo "logs: $LOG_DIR"
} | tee "$LOG_DIR/summary.txt"

red=$(grep -c ' FAIL' "$LOG_DIR/summary.txt" || true)
failed_tests=$(grep -cE '^(--- FAIL|FAIL\s)' "$LOG_DIR/vm-logs/test.log" 2>/dev/null || true)
if ((VM_RC != 0 || red > 0 || failed_tests > 0)); then
  VERDICT=failure
  log "GATE RED"
else
  VERDICT=success
  log "GATE GREEN"
fi

# 5. Optional: record the verdict on the commit, so a pull request shows it.
if $POST_STATUS; then
  slug=$(gh repo view --json nameWithOwner --jq .nameWithOwner)
  gh api -X POST "repos/$slug/statuses/$SHA" \
    -f state="$VERDICT" -f context="local-gate" \
    -f description="KVM VM + host: test, lint, audit, build, secrets, osv, zizmor, changelog" \
    >/dev/null && log "posted local-gate=$VERDICT on $SHORT"
fi

[[ "$VERDICT" == success ]]
