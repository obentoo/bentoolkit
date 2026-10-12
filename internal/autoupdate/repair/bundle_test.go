package repair

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/obentoo/bentoolkit/internal/common/secrets"
)

const failureBudgetBytes = 16 << 10

func sampleBundle(stagedRoot string) Bundle {
	return Bundle{
		Schema:         SchemaVersion,
		Package:        "dev-go/govulncheck",
		CurrentVersion: "1.8.0",
		Candidate:      "1.9.0",
		Stage:          "re-check",
		Failure:        "manifest command failed: 404 Not Found",
		RefusedTools:   []string{"Bash", "WebFetch(raw.githubusercontent.com)"},
		StagedRoot:     stagedRoot,
		EbuildPath:     filepath.Join(stagedRoot, "dev-go", "govulncheck", "govulncheck-1.9.0.ebuild"),
		UpstreamURLs:   []string{"https://api.github.com/repos/golang/vuln/tags?per_page=30"},
		Created:        time.Date(2026, 10, 11, 7, 47, 47, 0, time.UTC),
	}
}

// TestRedactScrubsEverySecretLikeTheLogger: the failure text is scrubbed with
// the logger's mask, every secret, and a blank secret changes nothing.
func TestRedactScrubsEverySecretLikeTheLogger(t *testing.T) {
	const tokA = "ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	const tokB = "glpat-BBBBBBBBBBBBBBBBBBBB"
	in := "GET https://x/?t=" + tokA + " failed; retry with " + tokB + " also " + tokA
	got := Redact(in, []string{tokA, tokB, ""})
	if strings.Contains(got, tokA) || strings.Contains(got, tokB) {
		t.Fatalf("Redact left a secret in the text: %q", got)
	}
	want := secrets.Scrub(secrets.Scrub(in, tokA), tokB)
	if got != want {
		t.Fatalf("Redact must apply the logger's redaction (secrets.Scrub):\n got %q\nwant %q", got, want)
	}
	if plain := "no secret here"; Redact(plain, []string{""}) != plain {
		t.Fatalf("a blank secret must not alter the text: %q", Redact(plain, []string{""}))
	}
}

// TestRedactLongerSecretContainingShorterIsScrubbedWhole: a secret that
// contains another must not leave its own remainder behind, whatever order the
// caller passes them in.
func TestRedactLongerSecretContainingShorterIsScrubbedWhole(t *testing.T) {
	const short = "s3cr3tvalue"
	const long = "s3cr3tvalue-with-suffix-XYZ"
	for _, order := range [][]string{{short, long}, {long, short}} {
		got := Redact("token="+long+" end", order)
		if strings.Contains(got, "with-suffix-XYZ") || strings.Contains(got, short) {
			t.Fatalf("Redact(order %q) leaked part of a secret: %q", order, got)
		}
	}
}

// TestRedactKeepsLastSixteenKiBOnRuneBoundary: the tail is kept, the head is
// dropped, the result never exceeds 16 KiB and is valid UTF-8.
func TestRedactKeepsLastSixteenKiBOnRuneBoundary(t *testing.T) {
	head := "HEAD-MARKER-" + strings.Repeat("a", 4096)
	// "é" is two bytes; the odd prefix length puts the byte cut mid-rune.
	body := strings.Repeat("é", failureBudgetBytes) + "x"
	tail := "-TAIL-MARKER"
	got := Redact(head+body+tail, nil)
	if len(got) > failureBudgetBytes {
		t.Fatalf("len(Redact) = %d, want <= %d", len(got), failureBudgetBytes)
	}
	if len(got) < failureBudgetBytes-utf8.UTFMax {
		t.Fatalf("len(Redact) = %d, the tail budget was not used (want >= %d)", len(got), failureBudgetBytes-utf8.UTFMax)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("Redact cut inside a rune: result is not valid UTF-8")
	}
	if !strings.HasSuffix(got, tail) {
		t.Fatalf("Redact must keep the END of the failure text; got suffix %q", got[max(0, len(got)-20):])
	}
	if strings.Contains(got, "HEAD-MARKER") {
		t.Fatalf("Redact kept the head instead of the tail")
	}
	short := "short failure"
	if Redact(short, nil) != short {
		t.Fatalf("a text under the budget must be kept whole: %q", Redact(short, nil))
	}
}

// TestRedactBeforeTruncationNeverLeaksAHalfSecret: a secret straddling the
// 16 KiB cut must be scrubbed whole before the cut, so no fragment of it
// survives as the first bytes of the kept tail.
func TestRedactBeforeTruncationNeverLeaksAHalfSecret(t *testing.T) {
	// Built at run time so the secret scanners do not flag a fixture.
	tok := "gh" + "p_FIRSTHALFfirsthalf0123SECONDHALFsecondhalf4567"
	secondHalf := tok[len(tok)/2:]
	pad := strings.Repeat("z", failureBudgetBytes-len(secondHalf))
	in := strings.Repeat("y", 100) + tok + pad
	// Truncating first would keep exactly secondHalf+pad.
	got := Redact(in, []string{tok})
	if strings.Contains(got, secondHalf) || strings.Contains(got, tok[:len(tok)/2]) {
		t.Fatalf("Redact truncated before scrubbing: a fragment of the secret survived: %q", got[:min(len(got), 80)])
	}
	if len(got) > failureBudgetBytes {
		t.Fatalf("len(Redact) = %d, want <= %d", len(got), failureBudgetBytes)
	}
}

// TestWriteBundleRoundTripsWithMode0600 covers R1.2 and R1.5: every field
// survives, the file is 0600 and named BundleFile, and no temp file is left.
func TestWriteBundleRoundTripsWithMode0600(t *testing.T) {
	root := t.TempDir()
	b := sampleBundle(root)
	if err := WriteBundle(b); err != nil {
		t.Fatalf("WriteBundle: %v", err)
	}
	path := filepath.Join(root, BundleFile)
	if BundleFile != ".bentoo-repair.json" {
		t.Fatalf("BundleFile = %q, want .bentoo-repair.json", BundleFile)
	}
	if SchemaVersion != 1 {
		t.Fatalf("SchemaVersion = %d, want 1", SchemaVersion)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("bundle not written at %s: %v", path, err)
	}
	if mode := st.Mode().Perm(); mode != 0o600 {
		t.Fatalf("bundle mode = %o, want 600", mode)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != BundleFile {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("staged root holds %v after WriteBundle, want only %s (no temp file left)", names, BundleFile)
	}
	got, err := ReadBundle(path)
	if err != nil {
		t.Fatalf("ReadBundle: %v", err)
	}
	if !got.Created.Equal(b.Created) {
		t.Fatalf("Created = %v, want %v", got.Created, b.Created)
	}
	got.Created = b.Created
	if !reflect.DeepEqual(got, b) {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, b)
	}
}

// TestWriteBundleJSONFieldNames pins the documented on-disk keys.
func TestWriteBundleJSONFieldNames(t *testing.T) {
	root := t.TempDir()
	if err := WriteBundle(sampleBundle(root)); err != nil {
		t.Fatalf("WriteBundle: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, BundleFile))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("bundle is not a JSON object: %v", err)
	}
	for _, k := range []string{"schema", "package", "current_version", "candidate_version", "failed_stage",
		"failure", "refused_tools", "staged_root", "ebuild_path", "upstream_urls", "created"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("bundle JSON lacks key %q (have %v)", k, reflect.ValueOf(raw).MapKeys())
		}
	}
	if v, _ := raw["schema"].(float64); v != 1 {
		t.Errorf("schema = %v, want 1", raw["schema"])
	}
}

// TestWriteBundleOverwritesAnEarlierBundle: a rewrite (re-validation failed
// again) replaces the old content atomically.
func TestWriteBundleOverwritesAnEarlierBundle(t *testing.T) {
	root := t.TempDir()
	first := sampleBundle(root)
	if err := WriteBundle(first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Stage = "gate:compile"
	second.Failure = "compile failed again"
	if err := WriteBundle(second); err != nil {
		t.Fatalf("second WriteBundle: %v", err)
	}
	got, err := ReadBundle(filepath.Join(root, BundleFile))
	if err != nil {
		t.Fatal(err)
	}
	if got.Stage != "gate:compile" || got.Failure != "compile failed again" {
		t.Fatalf("rewrite not applied: stage %q failure %q", got.Stage, got.Failure)
	}
}

// TestWriteBundleRenameFailureLeavesNoPartialFile: when the final rename
// fails, the error names the path and no temp file or bundle remains.
func TestWriteBundleRenameFailureLeavesNoPartialFile(t *testing.T) {
	root := t.TempDir()
	// A non-empty directory where the bundle goes makes the rename fail.
	blocker := filepath.Join(root, BundleFile)
	if err := os.MkdirAll(filepath.Join(blocker, "keep"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := WriteBundle(sampleBundle(root))
	if err == nil {
		t.Fatalf("WriteBundle succeeded although %s is a non-empty directory", blocker)
	}
	if !strings.Contains(err.Error(), root) {
		t.Fatalf("error does not name the path: %v", err)
	}
	entries, rerr := os.ReadDir(root)
	if rerr != nil {
		t.Fatal(rerr)
	}
	for _, e := range entries {
		if e.Name() != BundleFile {
			t.Fatalf("temp file %q left behind after a failed write", e.Name())
		}
	}
	if fi, _ := os.Stat(blocker); fi == nil || !fi.IsDir() {
		t.Fatalf("the blocking directory was replaced")
	}
}

// TestWriteBundleRefusesMissingStagedRoot: an empty or absent staged root is an
// error, never a bundle written somewhere else.
func TestWriteBundleRefusesMissingStagedRoot(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	if err := WriteBundle(sampleBundle("")); err == nil {
		t.Fatalf("WriteBundle with an empty StagedRoot succeeded")
	}
	if _, err := os.Stat(filepath.Join(cwd, BundleFile)); err == nil {
		t.Fatalf("WriteBundle with an empty StagedRoot wrote into the working directory")
	}
	gone := filepath.Join(t.TempDir(), "gone")
	err := WriteBundle(sampleBundle(gone))
	if err == nil {
		t.Fatalf("WriteBundle into a missing staged root succeeded")
	}
	if !strings.Contains(err.Error(), gone) {
		t.Fatalf("error does not name the missing root %s: %v", gone, err)
	}
}

// TestReadBundleRefusesUnknownSchemaAndGarbage covers R2.5: each error names
// the bundle path; an unknown schema is ErrUnknownSchema.
func TestReadBundleRefusesUnknownSchemaAndGarbage(t *testing.T) {
	cases := []struct {
		name       string
		content    string
		create     bool
		wantSchema bool
	}{
		{name: "schema 2", content: `{"schema":2,"package":"dev-go/x"}`, create: true, wantSchema: true},
		{name: "schema 0", content: `{"package":"dev-go/x"}`, create: true, wantSchema: true},
		{name: "garbage", content: "not json {", create: true},
		{name: "truncated", content: `{"schema":1,"package":"dev-`, create: true},
		{name: "missing file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), BundleFile)
			if tc.create {
				if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			_, err := ReadBundle(path)
			if err == nil {
				t.Fatalf("ReadBundle accepted %s", tc.name)
			}
			if !strings.Contains(err.Error(), path) {
				t.Fatalf("error does not name %s: %v", path, err)
			}
			if got := errors.Is(err, ErrUnknownSchema); got != tc.wantSchema {
				t.Fatalf("errors.Is(err, ErrUnknownSchema) = %v, want %v (err %v)", got, tc.wantSchema, err)
			}
		})
	}
}
