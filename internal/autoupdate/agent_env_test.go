package autoupdate

// Authored for story 051 (llm-agent-least-privilege), sub-tasks 1.1 and 1.2 —
// the agent environment is a fixed allow-list (S051-R1.1..R1.8, S051-R9.2).
//
// Every assertion here reads the EXACT slice a spawner assigned to cmd.Env,
// captured through the spawner's own exec seam. Nothing reads the first or the
// last match of a name: a name that appears twice is itself a failure (R1.8), so
// which duplicate os/exec would pick is never what makes a test pass.
//
// The file also holds the helpers the other story-051 test files share
// (agentSeam, the argv readers, the hostile parent environment).

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	"github.com/obentoo/bentoolkit/internal/common/secrets"
)

// okEnvelope is a minimal successful `claude --output-format json` envelope.
const okEnvelope = `{"type":"result","subtype":"success","is_error":false,"result":"ok"}`

// agentSpawn records what a spawner handed to its exec seam: the argv, and the
// *exec.Cmd itself so the fields the spawner sets AFTER the factory returns
// (Env, Dir) can be read once the call is over.
type agentSpawn struct {
	mu    sync.Mutex
	count int
	name  string
	args  []string
	cmds  []*exec.Cmd
}

func (s *agentSpawn) last() *exec.Cmd {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.cmds) == 0 {
		return nil
	}
	return s.cmds[len(s.cmds)-1]
}

func (s *agentSpawn) spawns() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

// agentSeam returns an exec-seam factory whose child runs script under /bin/sh.
// The shell is named by absolute path so a test that empties the parent
// environment (PATH included) can still start the child.
func agentSeam(script string) (func(ctx context.Context, name string, arg ...string) *exec.Cmd, *agentSpawn) {
	spy := &agentSpawn{}
	factory := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script)
		spy.mu.Lock()
		spy.count++
		spy.name = name
		spy.args = append([]string(nil), arg...)
		spy.cmds = append(spy.cmds, cmd)
		spy.mu.Unlock()
		return cmd
	}
	return factory, spy
}

// printEnvelopeScript prints body verbatim on stdout and exits 0.
func printEnvelopeScript(body string) string {
	return "printf '%s' '" + body + "'"
}

// envName returns the NAME part of a KEY=VALUE entry.
func envName(kv string) string {
	name, _, _ := strings.Cut(kv, "=")
	return name
}

// envValuesOf returns every value the slice assigns to name, in order.
func envValuesOf(env []string, name string) []string {
	var out []string
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if k == name {
			out = append(out, v)
		}
	}
	return out
}

// assertNoDuplicateNames is R1.8: no variable name appears twice.
func assertNoDuplicateNames(t *testing.T, who string, env []string) {
	t.Helper()
	seen := map[string]int{}
	for _, kv := range env {
		seen[envName(kv)]++
	}
	for name, n := range seen {
		if n > 1 {
			t.Errorf("%s: %s appears %d times in the agent environment; which one os/exec picks must never decide a value (R1.8)", who, name, n)
		}
	}
}

// agentEnvAllowedExact is the R1.1 allow-list, byte for byte.
var agentEnvAllowedExact = map[string]bool{
	"PATH": true, "HOME": true, "TMPDIR": true, "LANG": true, "TERM": true,
	"CLAUDE_CONFIG_DIR": true, "NODE_EXTRA_CA_CERTS": true, "SSL_CERT_FILE": true,
	"HTTP_PROXY": true, "HTTPS_PROXY": true, "NO_PROXY": true,
	"http_proxy": true, "https_proxy": true, "no_proxy": true,
}

// agentEnvNameAllowed reports whether name may appear in an agent environment.
// portage adds the manifest fixer's extras (R1.3); ANTHROPIC_API_KEY is judged
// by the callers, because only bare mode may carry it (R1.5, R1.6).
func agentEnvNameAllowed(name string, portage bool) bool {
	if agentEnvAllowedExact[name] || strings.HasPrefix(name, "LC_") || strings.HasPrefix(name, "XDG_") {
		return true
	}
	if portage && (name == "DISTDIR" || strings.HasPrefix(name, "PORTAGE_")) {
		return true
	}
	return false
}

// setHostileParentEnv plants, in the PARENT environment, the secrets R1.2 names
// and a set of look-alike names that sit one character away from an allowed
// one, so a prefix or case slip in the allow-list lets them through. Every value
// carries "sentinel" so a leak is visible whatever name it travels under.
// HOME and XDG_CONFIG_HOME are pointed at a private directory so
// secrets.Paths() never reads the operator's real files.
func setHostileParentEnv(t *testing.T) (home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	for name, value := range map[string]string{
		"GITHUB_TOKEN":         "sentinel-github",
		"BENTOO_NTFY_TOKEN":    "sentinel-ntfy",
		"BENTOO_SMTP_PASSWORD": "sentinel-smtp",
		"ANTHROPIC_AUTH_TOKEN": "sentinel-auth-token",
		"ANTHROPIC_BASE_URL":   "sentinel-base-url",
		"PATH_EXTRA":           "sentinel-path-extra",
		"TERM_PROGRAM":         "sentinel-term-program",
		"LANGUAGE":             "sentinel-language",
		"HOMEBREW_PREFIX":      "sentinel-home-prefix",
		"LCX_LEAK":             "sentinel-lcx",
		"XDGX_LEAK":            "sentinel-xdgx",
		"HTTP_PROXY_PASSWORD":  "sentinel-proxy-password",
		"PORTAGE":              "sentinel-portage-bare",
		"PORTAGEQ_BIN":         "sentinel-portageq",
		"PORTAGE_TMPDIR":       "/var/tmp/portage051",
		"DISTDIR":              "/ambient/distdir051",
	} {
		t.Setenv(name, value)
	}
	return home
}

// assertCleanAgentEnv is the per-spawner half of R1.2/R1.4/R1.7/R1.8: the env is
// assigned (non-nil), carries only allowed names, no sentinel value, no
// duplicate, and — unless portage — no PORTAGE_*/DISTDIR entry.
func assertCleanAgentEnv(t *testing.T, who string, cmd *exec.Cmd, portage bool) {
	t.Helper()
	if cmd == nil {
		t.Fatalf("%s: no command was spawned", who)
	}
	if cmd.Env == nil {
		t.Fatalf("%s: cmd.Env is nil, so the agent inherits the whole parent environment (R1.7)", who)
	}
	assertNoDuplicateNames(t, who, cmd.Env)
	var outside, leakedUnder []string
	for _, kv := range cmd.Env {
		name := envName(kv)
		if name == "ANTHROPIC_API_KEY" {
			t.Errorf("%s: non-bare agent carries ANTHROPIC_API_KEY (R1.6)", who)
			continue
		}
		if !agentEnvNameAllowed(name, portage) {
			outside = append(outside, name)
		}
		if strings.Contains(kv, "sentinel") {
			leakedUnder = append(leakedUnder, name)
		}
	}
	sort.Strings(outside)
	sort.Strings(leakedUnder)
	if len(leakedUnder) > 0 {
		t.Errorf("%s: a sentinel secret value reached the agent under %v (R1.2)", who, leakedUnder)
	}
	if len(outside) > 0 {
		t.Errorf("%s: %d names outside the R1.1 allow-list reached the agent environment (R1.2, R1.4): %v", who, len(outside), outside)
	}
}

// ---------------------------------------------------------------------------
// 1.1 — the builder
// ---------------------------------------------------------------------------

// TestChildEnv_OnlyAllowListedNamesCross is R1.1/R1.2/R1.6, in both directions.
// Collapse direction: look-alike names (PATH_EXTRA, TERM_PROGRAM, LANGUAGE,
// LCX_*, XDGX_*, HTTP_PROXY_PASSWORD) and the named secrets stay out. Split
// direction: names spelled differently but allowed (lowercase proxies, any
// LC_*/XDG_*) come through, each with the parent's value. Third element: an
// api_key_env whose name happens to sit inside the XDG_ prefix is still an auth
// source and must not cross in non-bare mode.
func TestChildEnv_OnlyAllowListedNamesCross(t *testing.T) {
	home := setHostileParentEnv(t)
	allowed := map[string]string{
		"LANG":                "C.UTF-8",
		"TERM":                "xterm-051",
		"CLAUDE_CONFIG_DIR":   "/cfg/claude051",
		"NODE_EXTRA_CA_CERTS": "/etc/ssl/ca051.pem",
		"SSL_CERT_FILE":       "/etc/ssl/cert051.pem",
		"HTTP_PROXY":          "http://proxy051:3128",
		"HTTPS_PROXY":         "http://proxy051:3129",
		"NO_PROXY":            "localhost051",
		"http_proxy":          "http://lower051:3128",
		"https_proxy":         "http://lower051:3129",
		"no_proxy":            "lower-localhost051",
		"LC_ALL":              "C.UTF-8",
		"LC_TIME":             "en_DK.UTF-8",
		"XDG_RUNTIME_DIR":     "/run/user/051",
	}
	for k, v := range allowed {
		t.Setenv(k, v)
	}
	allowed["HOME"] = home
	allowed["XDG_CONFIG_HOME"] = filepath.Join(home, ".config")
	allowed["PATH"] = os.Getenv("PATH")

	const keyEnv = "XDG_BENTOO051_KEY"
	t.Setenv(keyEnv, "sentinel-key-under-xdg-prefix")
	t.Setenv("ANTHROPIC_API_KEY", "sentinel-ambient-key")

	seam, spy := agentSeam(printEnvelopeScript(okEnvelope))
	c := newTestClient(t, LLMConfig{Bare: "false", APIKeyEnv: keyEnv}, WithClaudeCodeExecCommand(seam))
	if _, err := c.run(t.Context(), "instr", []byte("content"), ""); err != nil {
		t.Fatalf("run: %v", err)
	}

	env := spy.last().Env
	assertCleanAgentEnv(t, "text client", spy.last(), false)
	for name, want := range allowed {
		got := envValuesOf(env, name)
		if len(got) != 1 || got[0] != want {
			t.Errorf("%s = %q in the agent environment, want exactly [%q] (R1.1)", name, got, want)
		}
	}
	if v := envValuesOf(env, keyEnv); len(v) != 0 {
		t.Errorf("the api_key_env name %s crossed in non-bare mode although it is an auth source (R1.6)", keyEnv)
	}
	if v := envValuesOf(env, "ANTHROPIC_AUTH_TOKEN"); len(v) != 0 {
		t.Errorf("ANTHROPIC_AUTH_TOKEN crossed in non-bare mode (R1.6)")
	}
}

// TestChildEnv_AmbientKeyYieldsOneResolvedEntry is the G1 hermeticity defect,
// proved: an ANTHROPIC_API_KEY in the caller's environment must neither survive
// nor duplicate the resolved key. Exactly ONE entry, holding the resolved key.
// The converse: bare mode with nothing resolved carries NO key at all — the
// ambient one does not slip in to fill the gap.
func TestChildEnv_AmbientKeyYieldsOneResolvedEntry(t *testing.T) {
	setHostileParentEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "ambient-x")
	const keyEnv = "BENTOO051_RESOLVED_KEY"
	t.Setenv(keyEnv, "resolved-secret")

	seam, spy := agentSeam(printEnvelopeScript(okEnvelope))
	c := newTestClient(t, LLMConfig{Bare: "auto", APIKeyEnv: keyEnv}, WithClaudeCodeExecCommand(seam))
	if !c.bareMode {
		t.Fatal("precondition: a resolved key with api_key_env set must select bare mode")
	}
	if _, err := c.run(t.Context(), "instr", []byte("content"), ""); err != nil {
		t.Fatalf("run: %v", err)
	}
	env := spy.last().Env
	assertNoDuplicateNames(t, "bare text client", env)
	if got := envValuesOf(env, "ANTHROPIC_API_KEY"); len(got) != 1 || got[0] != "resolved-secret" {
		t.Errorf("ANTHROPIC_API_KEY entries = %q, want exactly [\"resolved-secret\"] (R1.5)", got)
	}
	if got := envValuesOf(env, keyEnv); len(got) != 0 {
		t.Errorf("the api_key_env variable %s crossed into the agent environment (R1.2)", keyEnv)
	}

	// Converse: bare forced on, no key resolved.
	seam2, spy2 := agentSeam(printEnvelopeScript(okEnvelope))
	c2 := newTestClient(t, LLMConfig{Bare: "true"}, WithClaudeCodeExecCommand(seam2))
	if _, err := c2.run(t.Context(), "instr", []byte("content"), ""); err != nil {
		t.Fatalf("run (bare, no key): %v", err)
	}
	if got := envValuesOf(spy2.last().Env, "ANTHROPIC_API_KEY"); len(got) != 0 {
		t.Errorf("bare mode with no resolved key carries ANTHROPIC_API_KEY=%q; the ambient key must never cross (R1.2)", got)
	}
}

// TestChildEnv_NeverNil is R1.7: a parent holding NO allow-listed variable —
// only secrets — still yields an assigned, empty environment, never nil (nil
// would make the child inherit everything).
func TestChildEnv_NeverNil(t *testing.T) {
	stubLookPathFound(t)
	for _, kv := range os.Environ() {
		name, value, _ := strings.Cut(kv, "=")
		if name == "" {
			continue
		}
		t.Setenv(name, value) // restores the original on cleanup
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("unsetting %s: %v", name, err)
		}
	}
	t.Setenv("GITHUB_TOKEN", "sentinel-github")
	t.Setenv("BENTOO_NTFY_TOKEN", "sentinel-ntfy")

	seam, spy := agentSeam(printEnvelopeScript(okEnvelope))
	c, err := NewClaudeCodeClient(LLMConfig{Bare: "false"}, WithClaudeCodeExecCommand(seam))
	if err != nil {
		t.Fatalf("NewClaudeCodeClient: %v", err)
	}
	if _, err := c.run(t.Context(), "instr", []byte("content"), ""); err != nil {
		t.Fatalf("run: %v", err)
	}
	cmd := spy.last()
	if cmd.Env == nil {
		t.Fatal("cmd.Env is nil; the child inherits the parent environment verbatim (R1.7)")
	}
	if len(cmd.Env) != 0 {
		t.Errorf("cmd.Env = %q, want empty: the parent held no allow-listed variable (R1.2, R1.7)", cmd.Env)
	}
}

// ---------------------------------------------------------------------------
// 1.2 — per-spawner wiring
// ---------------------------------------------------------------------------

// TestAgentEnv_ManifestFixerGetsPortageAndOneDistdir is R1.3 with its hostile
// halves: PORTAGE_* crosses, the look-alikes PORTAGE and PORTAGEQ_BIN do not,
// and an ambient DISTDIR is REPLACED — exactly one DISTDIR entry, whose value is
// the request's DistDir, never the parent's.
func TestAgentEnv_ManifestFixerGetsPortageAndOneDistdir(t *testing.T) {
	setHostileParentEnv(t)
	seam, spy := agentSeam(printEnvelopeScript(okEnvelope))
	f := newTestFixer(t, LLMConfig{Provider: "claude-code", Bare: "false"}, WithFixerExecCommand(seam))
	req := sampleFixRequest(t)
	if _, err := f.FixManifest(context.Background(), req); err != nil {
		t.Fatalf("FixManifest: %v", err)
	}
	cmd := spy.last()
	assertCleanAgentEnv(t, "manifest fixer", cmd, true)
	if got := envValuesOf(cmd.Env, "DISTDIR"); len(got) != 1 || got[0] != req.DistDir {
		t.Errorf("DISTDIR entries = %q, want exactly [%q] (R1.3)", got, req.DistDir)
	}
	if got := envValuesOf(cmd.Env, "PORTAGE_TMPDIR"); len(got) != 1 || got[0] != "/var/tmp/portage051" {
		t.Errorf("PORTAGE_TMPDIR entries = %q, want the parent's value once (R1.3)", got)
	}
}

// TestAgentEnv_RegistryFixerCarriesNoSecret is R1.2/R1.4 for the registry fixer.
func TestAgentEnv_RegistryFixerCarriesNoSecret(t *testing.T) {
	setHostileParentEnv(t)
	seam, spy := agentSeam(printEnvelopeScript(okEnvelope))
	stubLookPathFound(t)
	f, err := NewClaudeCodeRegistryFixer(LLMConfig{Provider: "claude-code", Bare: "false"}, WithRegistryFixerExecCommand(seam))
	if err != nil {
		t.Fatalf("NewClaudeCodeRegistryFixer: %v", err)
	}
	if _, err := f.FixRegistry(context.Background(), sampleRegistryFixRequest(t)); err != nil {
		t.Fatalf("FixRegistry: %v", err)
	}
	assertCleanAgentEnv(t, "registry fixer", spy.last(), false)
}

// sampleBuildFixRequest builds a request over a real staged tree, so the child
// (which starts in the staged package directory) can start.
func sampleBuildFixRequest(t *testing.T) BuildFixRequest {
	t.Helper()
	root := t.TempDir()
	pkgDir := filepath.Join(root, "media-libs", "foo")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("creating staged package dir: %v", err)
	}
	ebuild := filepath.Join(pkgDir, "foo-1.2.ebuild")
	if err := os.WriteFile(ebuild, []byte("EAPI=8\n"), 0o644); err != nil {
		t.Fatalf("writing staged ebuild: %v", err)
	}
	return BuildFixRequest{
		Package:    "media-libs/foo",
		Version:    "1.2",
		Gate:       validate.GateCompile,
		StagedDir:  root,
		EbuildPath: ebuild,
		BuildLog:   "error: foo.h: No such file or directory",
		Attempt:    1,
	}
}

// TestAgentEnv_BuildFixerCarriesNoSecret is R1.2/R1.4 for the build fixer.
func TestAgentEnv_BuildFixerCarriesNoSecret(t *testing.T) {
	setHostileParentEnv(t)
	seam, spy := agentSeam(printEnvelopeScript(okEnvelope))
	stubLookPathFound(t)
	f, err := NewClaudeCodeBuildFixer(LLMConfig{Provider: "claude-code", Bare: "false"}, WithBuildFixerExecCommand(seam))
	if err != nil {
		t.Fatalf("NewClaudeCodeBuildFixer: %v", err)
	}
	if _, err := f.FixBuild(context.Background(), sampleBuildFixRequest(t)); err != nil {
		t.Fatalf("FixBuild: %v", err)
	}
	assertCleanAgentEnv(t, "build fixer", spy.last(), false)
}

// reviewEnvelope is a successful reviewer envelope carrying an empty review.
const reviewEnvelope = `{"type":"result","subtype":"success","is_error":false,"result":"{\"risks\":[]}"}`

// sampleBumpReviewRequest carries a prepared diff, so no archive is read.
func sampleBumpReviewRequest() BumpReviewRequest {
	return BumpReviewRequest{
		Package:       "media-plugins/gst-plugins-qt6",
		OldVersion:    "1.28.6",
		NewVersion:    "1.29.2",
		BuildFileDiff: "-option('aalib')\n+option('vulkan')\n",
	}
}

// TestAgentEnv_BumpReviewerCarriesNoSecret is R1.2/R1.4 for the bump reviewer.
func TestAgentEnv_BumpReviewerCarriesNoSecret(t *testing.T) {
	setHostileParentEnv(t)
	seam, spy := agentSeam(printEnvelopeScript(reviewEnvelope))
	stubLookPathFound(t)
	r, err := NewClaudeCodeBumpReviewer(LLMConfig{Provider: "claude-code", Bare: "false"}, WithBumpReviewerExecCommand(seam))
	if err != nil {
		t.Fatalf("NewClaudeCodeBumpReviewer: %v", err)
	}
	if _, err := r.ReviewBump(context.Background(), sampleBumpReviewRequest()); err != nil {
		t.Fatalf("ReviewBump: %v", err)
	}
	assertCleanAgentEnv(t, "bump reviewer", spy.last(), false)
}

// TestAgentEnv_TextClientCarriesNoSecret is R1.2/R1.4 for the text-only client.
func TestAgentEnv_TextClientCarriesNoSecret(t *testing.T) {
	setHostileParentEnv(t)
	seam, spy := agentSeam(printEnvelopeScript(okEnvelope))
	c := newTestClient(t, LLMConfig{Bare: "false"}, WithClaudeCodeExecCommand(seam))
	if _, err := c.run(t.Context(), "instr", []byte("content"), ""); err != nil {
		t.Fatalf("run: %v", err)
	}
	assertCleanAgentEnv(t, "text client", spy.last(), false)
}

// ---------------------------------------------------------------------------
// argv readers shared by the story-051 permission tests
// ---------------------------------------------------------------------------

// flagValues returns every value given to flag: for each occurrence, the
// elements that follow it up to the next element starting with "-". It reads a
// variadic list (`--allowedTools A B C`) and a repeated flag
// (`--allowedTools A --allowedTools B`) the same way, so the tests do not pin
// which of the two forms the implementation chose.
func flagValues(args []string, flag string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		if args[i] != flag {
			continue
		}
		for j := i + 1; j < len(args) && !strings.HasPrefix(args[j], "-"); j++ {
			out = append(out, args[j])
		}
	}
	return out
}

// hasFlag reports whether flag occurs in args at all.
func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

// toolSet reads --tools as a set, splitting each value on commas and spaces
// (the CLI accepts both), so `Read,Edit` and `Read Edit` and two elements are
// one answer.
func toolSet(args []string) map[string]bool {
	set := map[string]bool{}
	for _, v := range flagValues(args, "--tools") {
		for _, f := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' }) {
			set[f] = true
		}
	}
	return set
}

// settingsPermissions parses the single inline --settings JSON and returns its
// "permissions" object (nil when absent or unparseable, after reporting).
func settingsPermissions(t *testing.T, who string, args []string) map[string]any {
	t.Helper()
	vals := flagValues(args, "--settings")
	if len(vals) != 1 {
		t.Errorf("%s: --settings values = %q, want exactly one inline JSON document (R4.2)", who, vals)
		return nil
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(vals[0]), &doc); err != nil {
		t.Errorf("%s: --settings is not inline JSON: %v (R4.2)", who, err)
		return nil
	}
	perms, _ := doc["permissions"].(map[string]any)
	if perms == nil {
		t.Errorf("%s: --settings carries no permissions object (R4.2)", who)
	}
	return perms
}

// ruleList returns the permission rules of one kind ("allow" or "deny"),
// wherever the implementation put them: the --allowedTools/--disallowedTools
// values, or the inline settings' permissions.allow/deny arrays. Empty values
// (the text client's `--allowedTools ""`) are dropped.
func ruleList(t *testing.T, who string, args []string, kind string) []string {
	t.Helper()
	flag := "--allowedTools"
	if kind == "deny" {
		flag = "--disallowedTools"
	}
	var out []string
	for _, v := range flagValues(args, flag) {
		if v != "" {
			out = append(out, v)
		}
	}
	if vals := flagValues(args, "--settings"); len(vals) == 1 {
		var doc struct {
			Permissions struct {
				Allow []string `json:"allow"`
				Deny  []string `json:"deny"`
			} `json:"permissions"`
		}
		if json.Unmarshal([]byte(vals[0]), &doc) == nil {
			if kind == "deny" {
				out = append(out, doc.Permissions.Deny...)
			} else {
				out = append(out, doc.Permissions.Allow...)
			}
		}
	}
	return out
}

// oneRule matches exactly ONE permission rule: a tool name, optionally followed
// by one parenthesised specifier. "Read Edit" or "Bash(pkgdev *) WebFetch"
// (two rules joined into one element) do not match (R2.7).
var oneRule = regexp.MustCompile(`^[A-Za-z]+(\([^()]*\))?$`)

// dirRule renders the R2.3 rule for tool over an absolute directory: `//` is
// the CLI's filesystem-root anchor, formed by the directory's own leading `/`
// plus one more — /tmp/x becomes Read(//tmp/x/**).
func dirRule(tool, dir string) string { return tool + "(/" + dir + "/**)" }

// fileRule renders the R2.5 `//`-anchored rule for one absolute file path.
func fileRule(tool, path string) string { return tool + "(/" + path + ")" }

func containsRule(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func countOf(list []string, want string) int {
	n := 0
	for _, v := range list {
		if v == want {
			n++
		}
	}
	return n
}

// assertPinnedPermissions checks the properties R2.5, R2.6, R2.7, R4.1 and R4.2
// require of EVERY agent: dontAsk, no user/project/local settings source, the
// two pinned settings, one argv element per rule, and a Read deny (plus an Edit
// deny when the agent holds Edit or Write) for every secrets.Paths() entry.
func assertPinnedPermissions(t *testing.T, who string, args []string, holdsEdit bool) {
	t.Helper()
	if got := flagValues(args, "--permission-mode"); len(got) != 1 || got[0] != "dontAsk" {
		t.Errorf("%s: --permission-mode = %q, want exactly [dontAsk] (R2.6)", who, got)
	}
	if !hasFlag(args, "--setting-sources") {
		t.Errorf("%s: no --setting-sources; operator and repository settings still load (R4.1)", who)
	}
	for _, v := range flagValues(args, "--setting-sources") {
		for _, src := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' }) {
			if src == "user" || src == "project" || src == "local" {
				t.Errorf("%s: --setting-sources loads %q (R4.1)", who, src)
			}
		}
	}
	if perms := settingsPermissions(t, who, args); perms != nil {
		if v, ok := perms["blockReadsOutsideWorkingDirectories"].(bool); !ok || !v {
			t.Errorf("%s: permissions.blockReadsOutsideWorkingDirectories = %v, want true (R4.2)", who, perms["blockReadsOutsideWorkingDirectories"])
		}
		if v, ok := perms["disableBypassPermissionsMode"].(string); !ok || v != "disable" {
			t.Errorf("%s: permissions.disableBypassPermissionsMode = %v, want \"disable\" (R4.2)", who, perms["disableBypassPermissionsMode"])
		}
	}
	for _, flag := range []string{"--allowedTools", "--disallowedTools"} {
		for _, v := range flagValues(args, flag) {
			if v != "" && !oneRule.MatchString(v) {
				t.Errorf("%s: %s element %q is not exactly one rule (R2.7)", who, flag, v)
			}
		}
	}
	deny := ruleList(t, who, args, "deny")
	for _, p := range secrets.Paths() {
		if !containsRule(deny, fileRule("Read", p)) {
			t.Errorf("%s: no deny rule %q for secrets path %s (R2.5); deny = %q", who, fileRule("Read", p), p, deny)
		}
		if holdsEdit && !containsRule(deny, fileRule("Edit", p)) {
			t.Errorf("%s: no deny rule %q for secrets path %s (R2.5)", who, fileRule("Edit", p), p)
		}
	}
	if hasFlag(args, "--dangerously-skip-permissions") || hasFlag(args, "--allow-dangerously-skip-permissions") {
		t.Errorf("%s: the invocation bypasses permissions", who)
	}
}

// shellWord matches a usable-command mention of curl, wget, cat or ls (R3.8).
var shellWord = regexp.MustCompile(`\b(curl|wget|cat|ls)\b`)

// isolateSecretsPaths points HOME and XDG_CONFIG_HOME at a private directory so
// secrets.Paths() is deterministic and never names the operator's files.
func isolateSecretsPaths(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	return home
}
