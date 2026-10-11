package autoupdate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/fixer"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// The validation record names two different byte strings. ebuild_digest is the
// source the candidate was staged from; validated_digest is the candidate as
// the gates read it, fixer and operator edits included. Reuse is a licence
// over the second, so a staged ebuild edited after the proof is validated
// again.
//
// The field is read and written through the record's JSON here, by its on-disk
// name, so these tests reach their assertions before the field exists in Go.

const validatedDigestKey = "validated_digest"

// recordFields reads the record beside root as raw JSON fields.
func recordFields(t *testing.T, root string) map[string]json.RawMessage {
	t.Helper()
	body, err := os.ReadFile(validate.StageRecordPath(root))
	if err != nil {
		t.Fatalf("no validation record beside %s: %v", root, err)
	}
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatalf("the validation record beside %s is not a JSON object: %v", root, err)
	}
	return fields
}

// recordedValidatedDigest returns the validated digest the record beside root
// carries, "" when it carries none.
func recordedValidatedDigest(t *testing.T, root string) string {
	t.Helper()
	raw, ok := recordFields(t, root)[validatedDigestKey]
	if !ok {
		return ""
	}
	var digest string
	if err := json.Unmarshal(raw, &digest); err != nil {
		t.Fatalf("%s in the record beside %s is not a string: %s", validatedDigestKey, root, raw)
	}
	return digest
}

// writeRecordWithValidatedDigest writes rec beside root and sets its validated
// digest to validated; present=false leaves the key out entirely.
func writeRecordWithValidatedDigest(t *testing.T, root string, rec validate.StageRecord, validated string, present bool) {
	t.Helper()
	if err := validate.WriteStageRecord(root, rec); err != nil {
		t.Fatalf("writing the stage record: %v", err)
	}
	fields := recordFields(t, root)
	delete(fields, validatedDigestKey)
	if present {
		raw, err := json.Marshal(validated)
		if err != nil {
			t.Fatal(err)
		}
		fields[validatedDigestKey] = raw
	}
	body, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(validate.StageRecordPath(root), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// stagedCandidatePath is the candidate ebuild inside a staged tree of the
// promoteFixture package.
func stagedCandidatePath(staged string) string {
	return filepath.Join(staged, "media-plugins", "gst-plugins-qt6", "gst-plugins-qt6-1.29.2.ebuild")
}

// editingFixSpy is a build fixer that appends edit to the staged ebuild it is
// handed, as a real repair would.
type editingFixSpy struct {
	edit  string
	calls []fixer.BuildFixRequest
	err   error
}

func (s *editingFixSpy) FixBuild(_ context.Context, req fixer.BuildFixRequest) (fixer.BuildFixResult, error) {
	s.calls = append(s.calls, req)
	f, err := os.OpenFile(req.EbuildPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		s.err = err
		return fixer.BuildFixResult{}, err
	}
	_, werr := f.WriteString(s.edit)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		s.err = werr
		return fixer.BuildFixResult{}, werr
	}
	return fixer.BuildFixResult{Summary: "edited the staged ebuild"}, nil
}

// editedBuildApplier wires an applier whose first build fails at configure and
// whose re-run is scripted by rerun; the build fixer edits the staged ebuild in
// between.
func editedBuildApplier(t *testing.T, spy *editingFixSpy, rerun buildRun) (a *Applier, overlayDir, pkg, stagingRoot string) {
	t.Helper()
	tmp := t.TempDir()
	overlayDir = filepath.Join(tmp, "overlay")
	configDir := filepath.Join(tmp, "config")
	stagingRoot = filepath.Join(tmp, "staging")
	pkg = "media-plugins/gst-plugins-qt6"

	createTestEbuildFile(t, overlayDir, pkg, "1.28.6")
	writePublishedManifest(t, overlayDir, publishedManifestBody)

	pending, err := NewPendingList(configDir)
	if err != nil {
		t.Fatalf("creating pending list: %v", err)
	}
	pending.Add(PendingUpdate{Package: pkg, CurrentVersion: "1.28.6", NewVersion: "1.29.2", Status: StatusPending})

	script := []buildRun{{output: preparedThenConfigureFails, err: errors.New("exit status 1")}, rerun}
	runs := 0
	attached := func(*exec.Cmd) ([]byte, error) {
		if runs >= len(script) {
			t.Fatalf("the build child ran %d times; the script has %d entries", runs+1, len(script))
		}
		r := script[runs]
		runs++
		return []byte(r.output), r.err
	}

	a, err = NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "true")
		}),
		WithApplierRunAttached(attached),
		WithConfirmFunc(func(string) bool { return true }),
		WithApplierStagingRoot(stagingRoot),
		WithApplierDistdir(t.TempDir(), ""),
		WithApplierBuildFixer(spy),
		WithApplierIsolationProbe(func() (bool, string) { return true, "" }),
	)
	if err != nil {
		t.Fatalf("creating applier: %v", err)
	}
	return a, overlayDir, pkg, stagingRoot
}

// TestStageRecordValidatedDigestIsTheBytesTheGatesRead: the build fixer edits
// the staged ebuild and the re-run passes, so the bytes the gates read are the
// edited ones. The record must carry their digest, and keep the source digest
// as its own, different value.
func TestStageRecordValidatedDigestIsTheBytesTheGatesRead(t *testing.T) {
	const edit = fixerEdit
	spy := &editingFixSpy{edit: edit}
	a, overlayDir, pkg, stagingRoot := editedBuildApplier(t, spy, buildRun{output: cleanCompile})
	source := candidateBody(t, overlayDir)

	result, err := a.Apply(t.Context(), pkg, true)
	if err != nil || !result.Success {
		t.Fatalf("fixture: the repaired bump did not pass: %v", err)
	}
	if len(spy.calls) != 1 || spy.err != nil {
		t.Fatalf("fixture: the build fixer ran %d time(s), edit error %v; want one edit", len(spy.calls), spy.err)
	}

	staged, err := validate.StagedTreePath(stagingRoot, pkg, "1.29.2")
	if err != nil {
		t.Fatal(err)
	}
	gatesRead, err := os.ReadFile(stagedCandidatePath(staged))
	if err != nil {
		t.Fatalf("reading the staged candidate: %v", err)
	}
	if string(gatesRead) != source+edit {
		t.Fatalf("fixture: the staged candidate is not the source plus the fixer's edit")
	}

	got := recordedValidatedDigest(t, staged)
	// Hostile half first: the digest of the source bytes is the value a
	// writer would reach for, and it is wrong here.
	if got == digestOf(source) {
		t.Fatalf("validated_digest is the digest of the source ebuild; the gates read the fixer-edited bytes")
	}
	if got != digestOf(source+edit) {
		t.Fatalf("validated_digest = %q, want %q (sha256 of the staged candidate as the gates read it)", got, digestOf(source+edit))
	}

	record, err := validate.ReadStageRecord(staged)
	if err != nil {
		t.Fatalf("reading the record: %v", err)
	}
	if record.EbuildDigest != digestOf(source) {
		t.Fatalf("ebuild_digest = %q, want the source digest %q: it keeps its meaning", record.EbuildDigest, digestOf(source))
	}
}

// TestStageRecordFailedRunCarriesNoValidatedDigest: a run whose gates did not
// pass proved no bytes, whether or not a fixer edited them.
func TestStageRecordFailedRunCarriesNoValidatedDigest(t *testing.T) {
	t.Run("a repaired build whose re-run still fails", func(t *testing.T) {
		spy := &editingFixSpy{edit: "\n# an edit that did not help\n"}
		a, _, pkg, stagingRoot := editedBuildApplier(t, spy,
			buildRun{output: preparedThenConfigureFails, err: errors.New("exit status 1")})

		result, _ := a.Apply(t.Context(), pkg, true)
		if result.Success {
			t.Fatalf("fixture: the bump passed although its re-run failed")
		}
		staged, err := validate.StagedTreePath(stagingRoot, pkg, "1.29.2")
		if err != nil {
			t.Fatal(err)
		}
		if got := recordedValidatedDigest(t, staged); got != "" {
			t.Fatalf("a failed run recorded validated_digest %q", got)
		}
	})

	t.Run("a gate that fails with no fixer", func(t *testing.T) {
		a, _, pkg, _, _ := promoteFixture(t, WithApplierDistdir(t.TempDir(), ""))
		result, _ := a.Apply(t.Context(), pkg, false)
		if result.Success || result.StagedPath == "" {
			t.Fatalf("fixture: want a failed run that kept its staged tree, got success=%v path=%q", result.Success, result.StagedPath)
		}
		if got := recordedValidatedDigest(t, result.StagedPath); got != "" {
			t.Fatalf("a failed run recorded validated_digest %q", got)
		}
	})
}

// reuseFixture stages the source ebuild plus suffix as the candidate and
// records a clean applier proof over the source bytes. The record's validated
// digest is validated(staged bytes); present=false omits the key.
func reuseFixture(t *testing.T, suffix string, validated func(staged string) string, present bool) (a *Applier, overlayDir, pkg, staged, body string, watch *promoteWatch) {
	t.Helper()
	a, overlayDir, pkg, watch, _ = promoteFixture(t,
		WithExecCommand(mockExecCommandSuccess),
		WithApplierDistdir(t.TempDir(), ""),
	)
	source := candidateBody(t, overlayDir)
	body = source + suffix
	staged = stageCandidateFor(t, a.StagingRoot(), overlayDir, body)
	rec := provedRecord(validate.DepthConfigure, digestOf(source), stagedDistfileDigest)
	rec.ProducedBy = validate.ProducedByApplier
	writeRecordWithValidatedDigest(t, staged, rec, validated(body), present)
	return a, overlayDir, pkg, staged, body, watch
}

// assertRevalidated checks that Apply ran the gates instead of promoting the
// retained tree on its record.
func assertRevalidated(t *testing.T, a *Applier, pkg string, watch *promoteWatch, why string) {
	t.Helper()
	before := watch.spawns
	result, err := a.Apply(t.Context(), pkg, false)
	if result == nil {
		t.Fatalf("Apply returned no result: %v", err)
	}
	if result.ValidationSource != ValidationSourceThisRun {
		t.Fatalf("ValidationSource = %q, want %q: %s (reason: %s)", result.ValidationSource, ValidationSourceThisRun, why, result.DepthReason)
	}
	if watch.spawns == before {
		t.Fatalf("no gate ran: %s", why)
	}
}

const fixerEdit = "\n# the build fixer dropped -Daalib=\n"

// TestReuseAcceptsUnchangedValidatedBytes: a tree whose staged bytes are still
// the ones its record proved is promoted without a gate, including when those
// bytes differ from the source because a fixer edited them.
func TestReuseAcceptsUnchangedValidatedBytes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		suffix string
	}{
		// Hostile half first: the staged bytes are not the source, and must
		// still match the proof.
		{name: "fixer-edited bytes", suffix: fixerEdit},
		{name: "bytes equal to the source", suffix: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, overlayDir, pkg, _, body, watch := reuseFixture(t, tc.suffix, digestOf, true)
			before := watch.spawns
			result, err := a.Apply(t.Context(), pkg, false)
			if err != nil || !result.Success {
				t.Fatalf("Apply: %v", err)
			}
			if result.ValidationSource != ValidationSourceStaged {
				t.Fatalf("ValidationSource = %q, want %q: the staged bytes are the ones the record proved (reason: %s)",
					result.ValidationSource, ValidationSourceStaged, result.DepthReason)
			}
			if watch.spawns != before {
				t.Fatalf("%d child process(es) ran for a tree whose proof still holds", watch.spawns-before)
			}
			published, err := os.ReadFile(filepath.Join(overlayDir, "media-plugins", "gst-plugins-qt6", "gst-plugins-qt6-1.29.2.ebuild"))
			if err != nil || string(published) != body {
				t.Fatalf("the published ebuild is not the proved bytes (err %v)", err)
			}
		})
	}
}

// TestReuseRefusesStagedBytesEditedAfterTheProof: once the staged candidate's
// bytes stop matching the proof, the tree is validated again.
func TestReuseRefusesStagedBytesEditedAfterTheProof(t *testing.T) {
	// Hostile third value: the proof is over fixer-edited bytes, and the staged
	// file is put back to the SOURCE bytes. Those match ebuild_digest, so a
	// check that accepts either digest would promote bytes no gate read.
	t.Run("reverted to the source bytes", func(t *testing.T) {
		a, overlayDir, pkg, staged, _, watch := reuseFixture(t, fixerEdit, digestOf, true)
		if err := os.WriteFile(stagedCandidatePath(staged), []byte(candidateBody(t, overlayDir)), 0o644); err != nil {
			t.Fatal(err)
		}
		assertRevalidated(t, a, pkg, watch, "the staged bytes are the source, not the bytes the record proved")
	})

	t.Run("edited by the operator after the proof", func(t *testing.T) {
		a, _, pkg, staged, body, watch := reuseFixture(t, "", digestOf, true)
		if err := os.WriteFile(stagedCandidatePath(staged), []byte(body+"\n# edited after the proof\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		assertRevalidated(t, a, pkg, watch, "the staged ebuild was edited after its proof")
	})
}

// TestReuseRefusesARecordWithoutValidatedDigest: a record that names no
// validated bytes, absent or empty, proves none.
func TestReuseRefusesARecordWithoutValidatedDigest(t *testing.T) {
	none := func(string) string { return "" }
	for _, tc := range []struct {
		name    string
		present bool
	}{
		{name: "key absent", present: false},
		{name: "key present and empty", present: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _, pkg, _, _, watch := reuseFixture(t, "", none, tc.present)
			assertRevalidated(t, a, pkg, watch, "the record carries no validated digest")
		})
	}
}
