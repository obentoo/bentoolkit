#!/usr/bin/env bats
# Tests for scripts/ci-vm-gate.sh's one-gate-at-a-time lock.
#
# Two gates started together once shut the shared VM down under each other.
# The gate's top level boots a VM, so, as in ci-vm-gate-host-job.bats, the
# script is never executed: acquire_gate_lock is lifted out of its text and run
# in a fresh strict-mode bash, with the lock file and LOG_DIR in the test's
# temporary directory.
#
# Run alone:
#   bats tests/ci-vm-gate-lock.bats
#
# CI_VM_GATE_SCRIPT overrides the script under test (default: this checkout's
# scripts/ci-vm-gate.sh).

bats_require_minimum_version 1.5.0

setup() {
	GATE_SCRIPT="${CI_VM_GATE_SCRIPT:-$(cd "$BATS_TEST_DIRNAME/.." && pwd)/scripts/ci-vm-gate.sh}"
	LOG_DIR="$BATS_TEST_TMPDIR/logs"
	LOCK_FILE="$BATS_TEST_TMPDIR/state/gate.lock"
	mkdir -p "$LOG_DIR"
	LOCK_DEF=$(sed -n '/^acquire_gate_lock() {/,/^}/p' "$GATE_SCRIPT")
	if [[ -z $LOCK_DEF ]]; then
		echo "no acquire_gate_lock() definition found in $GATE_SCRIPT" >&2
		return 1
	fi
}

# driver <body>: writes a strict-mode bash script that defines log and
# acquire_gate_lock, acquires the lock, then runs body.
driver() {
	local path="$BATS_TEST_TMPDIR/driver-$RANDOM.sh"
	{
		echo 'set -euo pipefail'
		printf 'LOG_DIR=%q\n' "$LOG_DIR"
		printf 'LOCK_FILE=%q\n' "$LOCK_FILE"
		cat <<-'EOF'
			log() { printf '%s\n' "$*" >>"$LOG_DIR/gate.log"; }
		EOF
		printf '%s\n' "$LOCK_DEF"
		echo 'acquire_gate_lock'
		printf '%s\n' "$1"
	} >"$path"
	echo "$path"
}

@test "a free lock is taken at once, with no waiting line" {
	run -0 bash "$(driver 'echo got-it')"
	[[ $output == got-it ]]
	[[ -f $LOCK_FILE ]]
	[[ ! -s $LOG_DIR/gate.log ]]
}

@test "a second gate waits for the first to exit, then proceeds" {
	# The bodies are expanded by the driver script, not here.
	# shellcheck disable=SC2016
	first=$(driver 'echo first-in >>"$LOG_DIR/order"; sleep 2; echo first-out >>"$LOG_DIR/order"')
	# shellcheck disable=SC2016
	second=$(driver 'echo second-in >>"$LOG_DIR/order"')

	bash "$first" &
	# Let the first gate take the lock before the second starts.
	for _ in $(seq 50); do
		[[ -s $LOG_DIR/order ]] && break
		sleep 0.1
	done
	run -0 bash "$second"
	wait

	run cat "$LOG_DIR/order"
	[[ $output == $'first-in\nfirst-out\nsecond-in' ]]
	grep -q 'waiting for it to finish' "$LOG_DIR/gate.log"
	grep -q 'lock acquired' "$LOG_DIR/gate.log"
}

@test "the shutdown trap is installed only after the lock is taken" {
	lock_line=$(grep -n '^acquire_gate_lock$' "$GATE_SCRIPT" | cut -d: -f1)
	trap_line=$(grep -n '^trap shutdown_later EXIT$' "$GATE_SCRIPT" | cut -d: -f1)
	[[ -n $lock_line && -n $trap_line ]]
	((lock_line < trap_line))
}
