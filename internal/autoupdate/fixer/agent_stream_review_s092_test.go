package fixer

// Story 092, sub-task 3.2, tech review cycle 1: each test pins one fix to
// runAgentStream that the pre-authored stream tests did not reach.

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// s092rCmd returns a command printing body from a file, then running tail
// (a shell snippet, may be empty) in the same process.
func s092rCmd(ctx context.Context, t *testing.T, body, tail string) *exec.Cmd {
	t.Helper()
	p := filepath.Join(t.TempDir(), "stream.jsonl")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return exec.CommandContext(ctx, "/bin/sh", "-c", `cat "$0"`+tail, p)
}

const s092rResult = `{"type":"result","subtype":"success","is_error":false,"result":"done","total_cost_usd":0.01}`

// A secret echoed by a tool result reaches the stream as Node's JSON.stringify
// spells it. That spelling is masked in the log and in the failure stdout.
func TestAgentStreamReviewMasksJSONEscapedSecrets(t *testing.T) {
	cases := []struct {
		name, secret, encoded string
	}{
		{"quote and backslash", `pa"ss\word-s092`, `pa\"ss\\word-s092`},
		{"tab, newline and a bare control", "tab\there\nnl\x01-s092", `tab\there\nnl\u0001-s092`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// No result event: the run fails to parse, so stdout is kept for the diagnostic.
			stream := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"key=` + tc.encoded + ` end"}]}}` + "\n"
			logPath := filepath.Join(t.TempDir(), "llm-fix-agent.jsonl")
			run := runAgentStream(s092rCmd(context.Background(), t, stream, ""), agentStreamSpec{
				stage: StageManifestFix, logPath: logPath, apiKey: tc.secret, pkg: "app-misc/foo",
			})
			if run.parseErr == nil {
				t.Fatal("a stream without a result parsed")
			}
			body, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			for where, got := range map[string]string{"log": string(body), "failure stdout": run.stdout} {
				if strings.Contains(got, tc.encoded) {
					t.Errorf("%s carries the JSON-escaped secret: %q", where, got)
				}
				if !strings.Contains(got, "key=*** end") {
					t.Errorf("%s does not mask the secret in place: %q", where, got)
				}
			}
		})
	}
}

// The escaping matches Node's JSON.stringify, not encoding/json: <, >, &,
// U+2028/U+2029 and non-ASCII stay as they are, controls use lowercase hex.
func TestAgentStreamReviewNodeJSONEscape(t *testing.T) {
	cases := map[string]string{
		`a"b\c`:                    `a\"b\\c`,
		"\b\f\n\r\t":               `\b\f\n\r\t`,
		"\x00\x1f\x0b":             `\u0000\u001f\u000b`,
		"<a&b>":                    "<a&b>",
		"x\u2028y\u2029z é 世 \x7f": "x\u2028y\u2029z é 世 \x7f",
	}
	for in, want := range cases {
		if got := nodeJSONEscape(in); got != want {
			t.Errorf("nodeJSONEscape(%q) = %q, want %q", in, got, want)
		}
	}
}

// A target is cut to the terminal limit only after it is masked, so the cut
// never shows the start of a secret it split.
func TestAgentStreamReviewProgressMaskedBeforeCut(t *testing.T) {
	const secret = "sk-ant-SECRETVALUE123"
	path := "/" + strings.Repeat("a", 150) + secret
	stream := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"` + path + `"}}]}}` + "\n" + s092rResult + "\n"
	var lines []string
	run := runAgentStream(s092rCmd(context.Background(), t, stream, ""), agentStreamSpec{
		stage: StageManifestFix, apiKey: secret, pkg: "app-misc/foo",
		progress: func(l string) { lines = append(lines, l) },
	})
	if run.parseErr != nil || run.runErr != nil {
		t.Fatalf("run failed: parse=%v run=%v", run.parseErr, run.runErr)
	}
	if len(lines) != 1 {
		t.Fatalf("progress lines = %q, want one", lines)
	}
	if strings.Contains(lines[0], "sk-ant") || !strings.Contains(lines[0], "***") {
		t.Errorf("progress line shows part of the secret, or no mask: %q", lines[0])
	}
}

// A progress callback that blocks never holds the run past its timeout: the
// parser does not wait on it, and the return waits only progressDrainGrace.
func TestAgentStreamReviewBlockingProgressDoesNotOutliveTimeout(t *testing.T) {
	call := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t%d","name":"Read","input":{"file_path":"/f%d"}}]}}` + "\n"
	var stream strings.Builder
	for i := range 3 {
		fmt.Fprintf(&stream, call, i, i)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	cmd := s092rCmd(ctx, t, stream.String(), "; exec sleep 10")
	cmd.WaitDelay = 100 * time.Millisecond
	var calls atomic.Int32
	start := time.Now()
	run := runAgentStream(cmd, agentStreamSpec{
		stage: StageManifestFix, pkg: "app-misc/foo",
		progress: func(string) { calls.Add(1); time.Sleep(3 * time.Second) },
	})
	if took := time.Since(start); took >= 1500*time.Millisecond {
		t.Errorf("runAgentStream took %v with a 200ms timeout and a blocking callback, want < 1.5s", took)
	}
	if run.runErr == nil {
		t.Error("the killed run reports no error")
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("callback calls = %d, want 1 (the one still blocked)", n)
	}
}

// A full queue drops lines instead of blocking the offer; a callback that
// does not catch up within the grace is abandoned.
func TestAgentStreamReviewProgressQueueDropsAndAbandons(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	f := startProgressForwarder(func(string) { <-release })
	offered := make(chan struct{})
	go func() {
		defer close(offered)
		for range progressQueue + 50 {
			f.offer("line")
		}
	}()
	select {
	case <-offered:
	case <-time.After(2 * time.Second):
		t.Fatal("offer blocks on a callback that does not return")
	}
	dropped, abandoned := f.finish(50 * time.Millisecond)
	if dropped < 49 || dropped > 50 {
		t.Errorf("dropped = %d, want 49..50 (the queue plus at most the one taken)", dropped)
	}
	if !abandoned {
		t.Error("a blocked callback was not abandoned")
	}
}

// The log path is opened without following a symlink: a planted link fails
// the log with a WARN and leaves its target untouched, and the run succeeds.
func TestAgentStreamReviewLogRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("victim content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(victim, 0o644); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "llm-fix-agent.jsonl")
	if err := os.Symlink(victim, logPath); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	run := runAgentStream(s092rCmd(context.Background(), t, s092rResult+"\n", ""), agentStreamSpec{
		stage: StageManifestFix, logPath: logPath, pkg: "app-misc/foo",
		log: slog.New(slog.NewTextHandler(&logs, nil)),
	})
	if run.parseErr != nil || run.runErr != nil || run.env.Result != "done" {
		t.Errorf("the run itself failed: parse=%v run=%v result=%q", run.parseErr, run.runErr, run.env.Result)
	}
	body, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "victim content\n" {
		t.Errorf("the symlink target was written: %q", body)
	}
	if fi, err := os.Stat(victim); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("the symlink target's mode changed: %v %v", fi.Mode().Perm(), err)
	}
	if fi, err := os.Lstat(logPath); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the log path is no longer the planted symlink: %v", err)
	}
	if !strings.Contains(logs.String(), "agent log could not be written") {
		t.Errorf("no WARN for the refused log:\n%s", logs.String())
	}
}

// The agent log's directory gets the mode the child-run logs give theirs.
func TestAgentStreamReviewLogDirModeMatchesChildRunLogs(t *testing.T) {
	base := t.TempDir()
	ref := filepath.Join(base, "ref")
	if err := os.MkdirAll(ref, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(base, "logs", "llm-fix-agent.jsonl")
	runAgentStream(s092rCmd(context.Background(), t, s092rResult+"\n", ""), agentStreamSpec{
		stage: StageManifestFix, logPath: logPath, pkg: "app-misc/foo",
	})
	want, err := os.Stat(ref)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.Stat(filepath.Dir(logPath))
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode().Perm() != want.Mode().Perm() {
		t.Errorf("agent log dir mode %v, child-run log dir mode %v", got.Mode().Perm(), want.Mode().Perm())
	}
}

// A shorter secret inside a longer one is not masked on its own while the
// longer one is still split across the scrubFlushLimit boundary.
func TestAgentStreamReviewNestedSecretsAcrossChunkBoundary(t *testing.T) {
	var out bytes.Buffer
	w := &lineScrubWriter{next: &out, secrets: func() []string { return []string{"ABCDEFGH", "CD"} }}
	first := strings.Repeat("x", scrubFlushLimit-4) + "ABCD"
	for _, p := range []string{first, "EFGH\n"} {
		if _, err := w.Write([]byte(p)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.flush(); err != nil {
		t.Fatal(err)
	}
	want := strings.Repeat("x", scrubFlushLimit-4) + "***\n"
	if out.String() != want {
		got := out.String()
		t.Errorf("tail of the masked line = %q, want %q", got[max(0, len(got)-16):], want[len(want)-16:])
	}
}

// Values that overlap all the way back to the line's start do not hold the
// whole line: the cut moves forward past them instead.
func TestAgentStreamReviewHoldCutStaysBounded(t *testing.T) {
	if got := holdCut([]byte("aaaaaaaa"), 4, []string{"aa"}); got != 8 {
		t.Errorf("holdCut over a run of overlapping values = %d, want 8", got)
	}
	if got := holdCut([]byte("xxABCDyy"), 4, []string{"ABCD"}); got != 2 {
		t.Errorf("holdCut inside a value = %d, want its start 2", got)
	}
}

// Only the head and tail of a long stream are kept for the diagnostic, and
// they read as truncateDiagnostic would print them.
func TestAgentStreamReviewFailureStdoutBounded(t *testing.T) {
	line := `{"type":"user","message":{"content":"` + strings.Repeat("z", 100) + `"}}` + "\n"
	stream := "HEAD-MARK\n" + strings.Repeat(line, 1000) + "TAIL-MARK\n"
	run := runAgentStream(s092rCmd(context.Background(), t, stream, ""), agentStreamSpec{stage: StageManifestFix, pkg: "app-misc/foo"})
	if run.parseErr == nil {
		t.Fatal("a stream without a result parsed")
	}
	if len(run.stdout) > diagnosticsBudget {
		t.Errorf("kept stdout is %d bytes, want at most %d", len(run.stdout), diagnosticsBudget)
	}
	if truncateDiagnostic(run.stdout) != run.stdout {
		t.Error("truncateDiagnostic changes the kept stdout: it would be cut twice")
	}
	for _, want := range []string{"HEAD-MARK", "TAIL-MARK", "diagnostic truncated: "} {
		if !strings.Contains(run.stdout, want) {
			t.Errorf("kept stdout lacks %q", want)
		}
	}
	m := regexp.MustCompile(`\n\.\.\.\[diagnostic truncated: (\d+) bytes elided\]\.\.\.\n`).FindStringSubmatch(run.stdout)
	if m == nil {
		t.Fatalf("no elision marker in the kept stdout")
	}
	elided, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatal(err)
	}
	if kept := len(run.stdout) - len(m[0]); kept+elided != len(stream) {
		t.Errorf("kept %d + elided %d bytes != stream %d bytes", kept, elided, len(stream))
	}
}
