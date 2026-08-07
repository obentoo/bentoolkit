package autoupdate

// fixer_model_test.go — story 030, sub-task 5.3 (S030-R4.1/R4.2/R4.3).
//
// What it pins: when an LLM fixer edits an ebuild or a registry entry, the result
// it returns must record WHICH model made the edit (R4.1) and must say when that
// model was an ALIAS — a bare word like "opus" — rather than a pinned identifier
// like "claude-opus-4-8" (R4.2). The distinction is not cosmetic: an alias
// resolves to a different model over time, so a record that omits it invites the
// wrong conclusion when someone audits a bad edit months later. And no path — no
// result field, no rendered log line, no error — may carry the API key (R4.3).
//
// The tests drive both fixers through the package-private scripted exec seam
// (fixerSeam / newTestFixer / newTestRegistryFixer), so no real `claude` binary
// is ever invoked and no network call is made.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// successEnvelope is a minimal well-formed CLI result envelope, so both fixers
// take their success path and return a populated result.
const successEnvelope = `{"type":"result","is_error":false,"result":"rewrote SRC_URI","total_cost_usd":0.01}`

// TestManifestFixResultCarriesResolvedModel pins R4.1 for the manifest fixer: the
// result reports the model string the invocation actually passed to --model, both
// when the model comes from config and when it falls back to the default.
func TestManifestFixResultCarriesResolvedModel(t *testing.T) {
	cases := []struct {
		name      string
		configure string // LLMConfig.Model
		want      string
	}{
		{name: "falls back to the default", configure: "", want: DefaultClaudeCodeModel},
		{name: "honours the configured model", configure: "claude-opus-4-8", want: "claude-opus-4-8"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			factory, cap, _ := fixerSeam("printf '%s' '" + successEnvelope + "'")
			f := newTestFixer(t, LLMConfig{Provider: "claude-code", Model: tc.configure},
				WithFixerExecCommand(factory))

			res, err := f.FixManifest(context.Background(), sampleFixRequest(t))
			if err != nil {
				t.Fatalf("FixManifest: %v", err)
			}

			if res.Model != tc.want {
				t.Errorf("res.Model = %q, want %q", res.Model, tc.want)
			}
			// R4.1 is about the string that reached the CLI, not the one that sat
			// in config, so the record is checked against the real argv.
			argvModel, ok := flagValue(cap.args, "--model")
			if !ok {
				t.Fatal("expected --model in argv")
			}
			if res.Model != argvModel {
				t.Errorf("res.Model = %q but argv --model = %q; the record must be what was passed", res.Model, argvModel)
			}
		})
	}
}

// TestRegistryFixResultCarriesResolvedModel is the same contract for the registry
// fixer, which resolves its model independently of the manifest fixer.
func TestRegistryFixResultCarriesResolvedModel(t *testing.T) {
	cases := []struct {
		name      string
		configure string
		want      string
	}{
		{name: "falls back to the default", configure: "", want: DefaultClaudeCodeModel},
		{name: "honours the configured model", configure: "claude-opus-4-8", want: "claude-opus-4-8"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			factory, cap, _ := fixerSeam("printf '%s' '" + successEnvelope + "'")
			f := newTestRegistryFixer(t, LLMConfig{Provider: "claude-code", Model: tc.configure},
				WithRegistryFixerExecCommand(factory))

			res, err := f.FixRegistry(context.Background(), sampleRegistryFixRequest(t))
			if err != nil {
				t.Fatalf("FixRegistry: %v", err)
			}

			if res.Model != tc.want {
				t.Errorf("res.Model = %q, want %q", res.Model, tc.want)
			}
			argvModel, ok := flagValue(cap.args, "--model")
			if !ok {
				t.Fatal("expected --model in argv")
			}
			if res.Model != argvModel {
				t.Errorf("res.Model = %q but argv --model = %q; the record must be what was passed", res.Model, argvModel)
			}
		})
	}
}

// TestAliasModelIsFlaggedAsAlias pins R4.2's core case: a bare-word model is
// flagged as an alias on BOTH result types, and the operator-facing phrase says
// the word "alias" rather than printing the bare word as if it were an identity.
func TestAliasModelIsFlaggedAsAlias(t *testing.T) {
	// The predicate itself. Anything that is not confidently a pinned id is an
	// alias — including strings the CLI would reject — because over-reporting an
	// alias is harmless while under-reporting one overstates the record.
	aliases := []string{
		"sonnet",
		"opus",
		"haiku",
		"opusplan",
		"sonnet[1m]",
		"claude-sonnet", // "claude-" shaped but carries no version component
		"",              // nothing to be confident about
	}
	for _, m := range aliases {
		if !isModelAlias(m) {
			t.Errorf("isModelAlias(%q) = false, want true", m)
		}
	}

	// The rendered phrase must STATE the alias (R4.2), not merely print it.
	phrase := FormatModelUsed("opus")
	if !strings.Contains(phrase, "alias") {
		t.Errorf("FormatModelUsed(%q) = %q; it must say that an alias was used", "opus", phrase)
	}
	if !strings.Contains(phrase, "opus") {
		t.Errorf("FormatModelUsed(%q) = %q; it must still name the model", "opus", phrase)
	}
	// An absent record must read as unknown, never as an alias named "".
	if got := FormatModelUsed(""); strings.Contains(got, "alias") {
		t.Errorf("FormatModelUsed(\"\") = %q; an absent model is unknown, not an alias", got)
	}

	// End to end, on both fixers: an aliased config yields ModelIsAlias == true.
	factory, _, _ := fixerSeam("printf '%s' '" + successEnvelope + "'")
	mf := newTestFixer(t, LLMConfig{Provider: "claude-code", Model: "opus"}, WithFixerExecCommand(factory))
	mres, err := mf.FixManifest(context.Background(), sampleFixRequest(t))
	if err != nil {
		t.Fatalf("FixManifest: %v", err)
	}
	if !mres.ModelIsAlias {
		t.Errorf("ManifestFixResult.ModelIsAlias = false for model %q, want true", mres.Model)
	}

	rfactory, _, _ := fixerSeam("printf '%s' '" + successEnvelope + "'")
	rf := newTestRegistryFixer(t, LLMConfig{Provider: "claude-code", Model: "opus"},
		WithRegistryFixerExecCommand(rfactory))
	rres, err := rf.FixRegistry(context.Background(), sampleRegistryFixRequest(t))
	if err != nil {
		t.Fatalf("FixRegistry: %v", err)
	}
	if !rres.ModelIsAlias {
		t.Errorf("RegistryFixResult.ModelIsAlias = false for model %q, want true", rres.Model)
	}
}

// TestPinnedModelIdIsNotFlaggedAsAlias is the other half of R4.2: a pinned
// identifier must NOT be labelled an alias, or the flag would be noise that
// operators learn to ignore.
func TestPinnedModelIdIsNotFlaggedAsAlias(t *testing.T) {
	pinned := []string{
		"claude-opus-4-8",
		"claude-sonnet-4-6",
		"claude-3-5-sonnet-20241022",
	}
	for _, m := range pinned {
		if isModelAlias(m) {
			t.Errorf("isModelAlias(%q) = true, want false", m)
		}
		if got := FormatModelUsed(m); strings.Contains(got, "alias") {
			t.Errorf("FormatModelUsed(%q) = %q; a pinned id must not be called an alias", m, got)
		}
	}

	factory, _, _ := fixerSeam("printf '%s' '" + successEnvelope + "'")
	f := newTestFixer(t, LLMConfig{Provider: "claude-code", Model: "claude-opus-4-8"},
		WithFixerExecCommand(factory))
	res, err := f.FixManifest(context.Background(), sampleFixRequest(t))
	if err != nil {
		t.Fatalf("FixManifest: %v", err)
	}
	if res.ModelIsAlias {
		t.Errorf("ManifestFixResult.ModelIsAlias = true for pinned model %q, want false", res.Model)
	}
}

// TestFixResultNeverContainsTheAPIKey pins R4.3 across every surface the new
// model record touches: the result fields, the phrase the operator log line is
// built from, and the error returned on failure.
//
// The test is deliberately NOT vacuous. This host runs the fixer on a
// subscription, so a naive "the result lacks the key" assertion would pass simply
// because there is no key. Here a key is configured, bare mode is forced on, and
// the scripted child dumps its own environment to disk — the test FAILS FIRST if
// the key never actually reached the invocation, and only then asserts absence
// everywhere else.
func TestFixResultNeverContainsTheAPIKey(t *testing.T) {
	const keyEnv = "TEST_FIXER_MODEL_KEY"
	const secret = "sk-ant-do-not-leak-me-030"
	t.Setenv(keyEnv, secret)

	cfg := LLMConfig{Provider: "claude-code", Model: "opus", APIKeyEnv: keyEnv, Bare: "true"}

	// --- Non-vacuity guard 1: the fixer really holds the key. ---
	envFile := filepath.Join(t.TempDir(), "child.env")
	factory, cap, _ := fixerSeam("env > '" + envFile + "'; printf '%s' '" + successEnvelope + "'")
	f := newTestFixer(t, cfg, WithFixerExecCommand(factory))
	if f.apiKey != secret {
		t.Fatalf("vacuous test: fixer.apiKey = %q, want the configured secret", f.apiKey)
	}
	if !f.bareMode {
		t.Fatal("vacuous test: expected bare mode so the key is injected into the child env")
	}

	res, err := f.FixManifest(context.Background(), sampleFixRequest(t))
	if err != nil {
		t.Fatalf("FixManifest: %v", err)
	}

	// --- Non-vacuity guard 2: the key was live in the invocation. ---
	childEnvDump, readErr := os.ReadFile(envFile)
	if readErr != nil {
		t.Fatalf("vacuous test: could not read the child environment dump: %v", readErr)
	}
	if !strings.Contains(string(childEnvDump), secret) {
		t.Fatal("vacuous test: the API key never reached the spawned CLI, so asserting its absence proves nothing")
	}

	// --- The assertions that matter. ---
	// argv: the key must never be an argument.
	for _, a := range cap.args {
		if strings.Contains(a, secret) {
			t.Errorf("API key leaked into argv: %q", a)
		}
	}
	// Result fields, including the new model record.
	for name, field := range map[string]string{"Summary": res.Summary, "Model": res.Model} {
		if strings.Contains(field, secret) {
			t.Errorf("API key leaked into ManifestFixResult.%s: %q", name, field)
		}
	}
	// The operator-facing phrase the applier's log line is composed from.
	if got := FormatModelUsed(res.Model); strings.Contains(got, secret) {
		t.Errorf("API key leaked into the log phrase: %q", got)
	}

	// The failure path: a non-zero exit funnels through formatFixerError, which
	// must not append the credential it holds.
	failFactory, _, _ := fixerSeam("echo 'upstream refused' 1>&2; exit 3")
	ff := newTestFixer(t, cfg, WithFixerExecCommand(failFactory))
	failRes, failErr := ff.FixManifest(context.Background(), sampleFixRequest(t))
	if failErr == nil {
		t.Fatal("expected an error from a non-zero CLI exit")
	}
	if strings.Contains(failErr.Error(), secret) {
		t.Errorf("API key leaked into the returned error: %v", failErr)
	}
	if strings.Contains(failRes.Model, secret) {
		t.Errorf("API key leaked into the failure-path model record: %q", failRes.Model)
	}

	// The registry fixer shares the same auth mechanics; assert its surfaces too.
	regFactory, regCap, _ := fixerSeam("printf '%s' '" + successEnvelope + "'")
	rf := newTestRegistryFixer(t, cfg, WithRegistryFixerExecCommand(regFactory))
	regRes, regErr := rf.FixRegistry(context.Background(), sampleRegistryFixRequest(t))
	if regErr != nil {
		t.Fatalf("FixRegistry: %v", regErr)
	}
	for _, a := range regCap.args {
		if strings.Contains(a, secret) {
			t.Errorf("API key leaked into registry argv: %q", a)
		}
	}
	if strings.Contains(regRes.Summary, secret) || strings.Contains(regRes.Model, secret) {
		t.Errorf("API key leaked into RegistryFixResult: %+v", regRes)
	}
}
