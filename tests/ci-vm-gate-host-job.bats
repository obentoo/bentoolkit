#!/usr/bin/env bats
# Tests for scripts/ci-vm-gate.sh's host jobs (story 087, sub-task 4.4: R7.1,
# R7.2, R10.8).
#
# The gate's top level boots a VM, so the script is never executed. Each test
# lifts two pieces out of its text and runs them in a fresh `bash` under the
# script's own `set -euo pipefail`, with LOG_DIR in the test's temporary
# directory:
#   - the host_job function, run backgrounded and waited for, as the gate does;
#   - the host half of the verdict loop, which turns host-<name>.rc into
#     "PASS/FAIL (<seconds>s)" or "FAIL (did not finish)".
#
# Run alone:
#   bats tests/ci-vm-gate-host-job.bats
#
# CI_VM_GATE_SCRIPT overrides the script under test (default: this checkout's
# scripts/ci-vm-gate.sh).

bats_require_minimum_version 1.5.0

setup() {
	GATE_SCRIPT="${CI_VM_GATE_SCRIPT:-$(cd "$BATS_TEST_DIRNAME/.." && pwd)/scripts/ci-vm-gate.sh}"
	LOG_DIR="$BATS_TEST_TMPDIR/logs"
	mkdir -p "$LOG_DIR"
	HOST_JOB_DEF=$(sed -n '/^host_job() {/,/^}/p' "$GATE_SCRIPT")
	VERDICT_LOOP=$(sed -n '/^  for name in secrets osv workflow-lint changelog; do$/,/^  done$/p' "$GATE_SCRIPT")
	if [[ -z $HOST_JOB_DEF ]]; then
		echo "no host_job() definition found in $GATE_SCRIPT" >&2
		return 1
	fi
	if [[ -z $VERDICT_LOOP ]]; then
		echo "no host verdict loop found in $GATE_SCRIPT" >&2
		return 1
	fi
}

# run_jobs <body>: runs body after host_job's definition, in a strict-mode bash
# with LOG_DIR set, and waits for every backgrounded job.
run_jobs() {
	local driver="$BATS_TEST_TMPDIR/driver.sh"
	{
		echo 'set -euo pipefail'
		printf 'LOG_DIR=%q\n' "$LOG_DIR"
		printf '%s\n' "$HOST_JOB_DEF"
		printf '%s\n' "$1"
		echo 'wait'
	} >"$driver"
	bash "$driver"
}

# verdict: prints the gate's host verdict lines for what LOG_DIR holds.
verdict() {
	local driver="$BATS_TEST_TMPDIR/verdict.sh"
	{
		echo 'set -euo pipefail'
		printf 'LOG_DIR=%q\n' "$LOG_DIR"
		printf '%s\n' "$VERDICT_LOOP"
	} >"$driver"
	bash "$driver"
}

# assert_rc <name> <code>: host-<name>.rc exists and reads "<code> <seconds>".
assert_rc() {
	local f="$LOG_DIR/host-$1.rc" line
	if [[ ! -f $f ]]; then
		echo "missing $f; LOG_DIR holds: $(ls -A "$LOG_DIR")" >&2
		return 1
	fi
	line=$(<"$f")
	if [[ ! $line =~ ^$2\ [0-9]+$ ]]; then
		echo "$f = '$line', want '$2 <seconds>'" >&2
		return 1
	fi
}

@test "host_job: a job killed before it records its result leaves no .rc, and the verdict says FAIL (did not finish)" {
	# The command kills the backgrounded host_job subshell (its parent) with
	# SIGKILL, so nothing after the command can run: no result may be invented.
	run_jobs "host_job changelog sh -c 'kill -KILL \$PPID' &"
	[[ ! -e $LOG_DIR/host-changelog.rc ]]

	run -0 verdict
	[[ $output == *"host:changelog  FAIL (did not finish)"* ]]
	[[ $output != *"host:changelog  FAIL ("[0-9]* ]]
	[[ $output != *"host:changelog  PASS"* ]]
}

@test "host_job: a failing command records its exit code and duration" {
	run_jobs 'host_job changelog false &'
	assert_rc changelog 1
}

@test "host_job: a non-1 exit code is recorded as it was returned" {
	run_jobs "host_job osv sh -c 'exit 3' &"
	assert_rc osv 3
}

@test "host_job: a passing command records 0 and keeps its output in host-<name>.log" {
	run_jobs "host_job secrets sh -c 'echo s087-out; echo s087-err >&2' &"
	assert_rc secrets 0
	grep -qx 's087-out' "$LOG_DIR/host-secrets.log"
	grep -qx 's087-err' "$LOG_DIR/host-secrets.log"
}

@test "verdict: four concurrent host jobs, two red, print each one's real result" {
	run_jobs "host_job secrets true &
host_job osv sh -c 'exit 3' &
host_job workflow-lint true &
host_job changelog false &"
	assert_rc secrets 0
	assert_rc osv 3
	assert_rc workflow-lint 0
	assert_rc changelog 1

	run -0 verdict
	[[ $output =~ host:secrets\ +PASS\ \([0-9]+s\) ]]
	[[ $output =~ host:osv\ +FAIL\ \([0-9]+s\) ]]
	[[ $output =~ host:workflow-lint\ +PASS\ \([0-9]+s\) ]]
	[[ $output =~ host:changelog\ +FAIL\ \([0-9]+s\) ]]
	[[ $output != *"did not finish"* ]]
}

@test "script: keeps set -euo pipefail at its top level" {
	grep -qx 'set -euo pipefail' "$GATE_SCRIPT"
}
