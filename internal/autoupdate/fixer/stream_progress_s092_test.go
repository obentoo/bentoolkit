package fixer

// Authored for story 092 (autoupdate-apply-observable), sub-task 3.2: the three
// unattended agents run with `--output-format stream-json --verbose`, report
// each tool call and refusal through Progress, and tee their raw stream to
// LogPath through the secret scrub (R4.1-R4.3, R4.6, R4.7, R6.4, Q7). The
// registry fixer is not part of --apply and keeps `--output-format json`.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/llm"
)

const s092Key = "redact-me-092-redact-me"

// s092Stream is a recorded-shape stream: init, model text, three tool calls, a
// tool_result echoing the API key value, and a result with two refusals.
func s092Stream(result string) string {
	return strings.Join([]string{
		`{"type":"system","subtype":"init","tools":["Read","Bash","WebFetch"],"model":"claude-sonnet-4-5"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"MODEL-PROSE-SENTINEL Read /etc/shadow"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/stage/foo-1.2.ebuild"}}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"t1","type":"tool_result","content":"key=` + s092Key + `"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"pkgdev manifest --distdir /d"}},{"type":"tool_use","id":"t3","name":"WebFetch","input":{"url":"https://github.com/o/r/releases","prompt":"p"}}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"result":` + result + `,"total_cost_usd":0.01,"permission_denials":[{"tool_name":"Bash","tool_input":{"command":"curl SENTINEL-INPUT-092"}}]}`,
	}, "\n") + "\n"
}

type s092Exec struct {
	mu   sync.Mutex
	args []string
}

// s092Seam prints body (from a file, so no shell quoting) and records argv.
func s092Seam(t *testing.T, body string) (func(ctx context.Context, name string, arg ...string) *exec.Cmd, *s092Exec) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "stream.jsonl")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := &s092Exec{}
	return func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		rec.mu.Lock()
		rec.args = append([]string(nil), arg...)
		rec.mu.Unlock()
		return exec.CommandContext(ctx, "/bin/sh", "-c", `cat "$0"`, p)
	}, rec
}

type s092Progress struct {
	mu    sync.Mutex
	lines []string
}

func (p *s092Progress) add(l string) { p.mu.Lock(); p.lines = append(p.lines, l); p.mu.Unlock() }

func s092AssertStreamArgv(t *testing.T, who string, args []string) {
	t.Helper()
	ok := false
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--output-format" && args[i+1] == "stream-json" {
			ok = true
		}
		if args[i] == "--output-format" && args[i+1] == "json" {
			t.Errorf("%s still asks for --output-format json: %q", who, args)
		}
	}
	if !ok {
		t.Errorf("%s argv lacks --output-format stream-json: %q", who, args)
	}
	verbose := false
	for _, a := range args {
		verbose = verbose || a == "--verbose"
	}
	if !verbose {
		t.Errorf("%s argv lacks --verbose (stream-json requires it under -p): %q", who, args)
	}
}

// s092AssertProgress checks one line per tool call and one per refusal, named
// by tool, never by input, and no model text.
func s092AssertProgress(t *testing.T, who string, lines []string) {
	t.Helper()
	all := strings.Join(lines, "\n")
	for _, want := range []string{"Read /stage/foo-1.2.ebuild", "Bash pkgdev", "WebFetch github.com", "Bash denied"} {
		n := 0
		for _, l := range lines {
			if strings.HasSuffix(l, want) {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%s: %d progress lines end with %q, want 1:\n%s", who, n, want, all)
		}
	}
	for _, leak := range []string{"MODEL-PROSE-SENTINEL", "/etc/shadow", "SENTINEL-INPUT-092", s092Key} {
		if strings.Contains(all, leak) {
			t.Errorf("%s: progress carries %q:\n%s", who, leak, all)
		}
	}
	for _, l := range lines {
		if strings.Contains(l, "\n") {
			t.Errorf("%s: a progress line spans rows: %q", who, l)
		}
	}
}

// s092AssertLog checks the raw stream reached LogPath whole, 0600, with the
// key value scrubbed.
func s092AssertLog(t *testing.T, who, path, stream string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: agent log: %v", who, err)
	}
	if strings.Contains(string(body), s092Key) {
		t.Errorf("%s: the agent log carries the API key value unscrubbed", who)
	}
	for _, l := range strings.Split(strings.TrimSpace(stream), "\n") {
		want := l
		if i := strings.Index(l, s092Key); i >= 0 {
			want = l[:i] // the scrubbed line keeps everything before the value
		}
		if !strings.Contains(string(body), want) {
			t.Errorf("%s: agent log misses stream line %q", who, l)
		}
	}
	if fi, err := os.Stat(path); err == nil && fi.Mode().Perm() != 0o600 {
		t.Errorf("%s: agent log mode %v, want 0600", who, fi.Mode().Perm())
	}
}

func s092KeyedConfig(t *testing.T) llm.LLMConfig {
	t.Helper()
	isolateSecretsHome(t)
	t.Setenv("BENTOO_S092_AGENT_KEY", s092Key)
	return llm.LLMConfig{Provider: "claude-code", Bare: "true", APIKeyEnv: "BENTOO_S092_AGENT_KEY"}
}

func TestManifestFixerStreamProgressAndLog(t *testing.T) {
	stream := s092Stream(`"rewrote SRC_URI"`)
	seam, rec := s092Seam(t, stream)
	f := newTestFixer(t, s092KeyedConfig(t), WithFixerExecCommand(seam))
	req := sampleFixRequest(t)
	prog := &s092Progress{}
	req.Progress = prog.add
	req.LogPath = filepath.Join(t.TempDir(), "logs", "llm-fix-agent.jsonl")
	res, err := f.FixManifest(context.Background(), req)
	if err != nil {
		t.Fatalf("FixManifest: %v", err)
	}
	if res.Summary != "rewrote SRC_URI" || res.CostUSD != 0.01 {
		t.Errorf("result fields not derived from the result event: %+v", res)
	}
	if len(res.DeniedTools) != 1 || res.DeniedTools[0] != "Bash" {
		t.Errorf("DeniedTools = %v, want [Bash] (named by label, never input)", res.DeniedTools)
	}
	s092AssertStreamArgv(t, "manifest fixer", rec.args)
	s092AssertProgress(t, "manifest fixer", prog.lines)
	s092AssertLog(t, "manifest fixer", req.LogPath, stream)
}

func TestBuildFixerStreamProgressAndLog(t *testing.T) {
	stream := s092Stream(`"added the missing header dependency"`)
	seam, rec := s092Seam(t, stream)
	cfg := s092KeyedConfig(t)
	stubLookPathFound(t)
	f, err := NewClaudeCodeBuildFixer(cfg, WithBuildFixerExecCommand(seam))
	if err != nil {
		t.Fatal(err)
	}
	req := sampleBuildFixRequest(t)
	prog := &s092Progress{}
	req.Progress = prog.add
	req.LogPath = filepath.Join(t.TempDir(), "llm-build-fix-agent.jsonl")
	if _, err := f.FixBuild(context.Background(), req); err != nil {
		t.Fatalf("FixBuild: %v", err)
	}
	s092AssertStreamArgv(t, "build fixer", rec.args)
	s092AssertProgress(t, "build fixer", prog.lines)
	s092AssertLog(t, "build fixer", req.LogPath, stream)
}

func TestBumpReviewerStreamProgressAndLog(t *testing.T) {
	stream := s092Stream(`"{\"risks\":[]}"`)
	seam, rec := s092Seam(t, stream)
	cfg := s092KeyedConfig(t)
	stubLookPathFound(t)
	r, err := NewClaudeCodeBumpReviewer(cfg, WithBumpReviewerExecCommand(seam))
	if err != nil {
		t.Fatal(err)
	}
	req := sampleBumpReviewRequest()
	prog := &s092Progress{}
	req.Progress = prog.add
	req.LogPath = filepath.Join(t.TempDir(), "review-agent.jsonl")
	if _, err := r.ReviewBump(context.Background(), req); err != nil {
		t.Fatalf("ReviewBump: %v", err)
	}
	s092AssertStreamArgv(t, "bump reviewer", rec.args)
	s092AssertProgress(t, "bump reviewer", prog.lines)
	s092AssertLog(t, "bump reviewer", req.LogPath, stream)
}

// The registry fixer is not an --apply row: it keeps the single JSON envelope.
func TestRegistryFixerStaysJSONUnchanged092(t *testing.T) {
	seam, rec := s092Seam(t, okEnvelope)
	stubLookPathFound(t)
	f, err := NewClaudeCodeRegistryFixer(llm.LLMConfig{Provider: "claude-code", Bare: "false"}, WithRegistryFixerExecCommand(seam))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.FixRegistry(context.Background(), sampleRegistryFixRequest(t)); err != nil {
		t.Fatalf("FixRegistry: %v", err)
	}
	found := false
	for i := 0; i+1 < len(rec.args); i++ {
		if rec.args[i] == "--output-format" {
			found = rec.args[i+1] == "json"
		}
	}
	if !found {
		t.Errorf("registry fixer argv changed its output format: %q", rec.args)
	}
}

// R4.6: a stream that ends without a result fails exactly like a missing
// envelope (empty stdout) — same first error line.
func TestManifestFixerStreamWithoutResultFailsLikeMissingEnvelope(t *testing.T) {
	first := func(err error) string { return strings.SplitN(err.Error(), "\n", 2)[0] }
	noResult := strings.Join(strings.Split(s092Stream(`"x"`), "\n")[:5], "\n") + "\n"
	seam, _ := s092Seam(t, noResult)
	f := newTestFixer(t, llm.LLMConfig{Provider: "claude-code", Bare: "false"}, WithFixerExecCommand(seam))
	_, errNoResult := f.FixManifest(context.Background(), sampleFixRequest(t))
	emptySeam, _ := s092Seam(t, "")
	g := newTestFixer(t, llm.LLMConfig{Provider: "claude-code", Bare: "false"}, WithFixerExecCommand(emptySeam))
	_, errEmpty := g.FixManifest(context.Background(), sampleFixRequest(t))
	if errNoResult == nil || errEmpty == nil {
		t.Fatalf("want both to fail: noResult=%v empty=%v", errNoResult, errEmpty)
	}
	if first(errNoResult) != first(errEmpty) {
		t.Errorf("no-result stream and missing envelope fail differently:\n%s\n%s", first(errNoResult), first(errEmpty))
	}
}
