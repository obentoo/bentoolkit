package snapshot

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Story 016 C2 — merging config names into SNAPPER_CONFIGS (R1, R5).
//
// mergeSnapperConfigsLine is pure — a string in, a string out — so the whole
// /etc/conf.d/snapper merge is exercised here without touching a real file.
// Wants are compared byte for byte and printed with %q, because quoting style
// and the final newline are part of the contract (R5.1).
// ---------------------------------------------------------------------------

// TestMergeSnapperConfigsLine: the merge lists every managed name in the first
// active SNAPPER_CONFIGS assignment (R1.1) without duplicating one already
// there (R1.2), creates the assignment when the content has none (R1.3), and
// copies every other variable, comment, and line through untouched (R1.4).
func TestMergeSnapperConfigsLine(t *testing.T) {
	cases := []struct {
		name     string
		existing string
		names    []string
		want     string
	}{
		{
			name:  "empty input creates the line",
			names: []string{"root"},
			want:  `SNAPPER_CONFIGS="root"` + "\n",
		},
		{
			name:     "partial list appends only the missing names",
			existing: `SNAPPER_CONFIGS="home root"` + "\n",
			names:    []string{"root", "var_log"},
			want:     `SNAPPER_CONFIGS="home root var_log"` + "\n",
		},
		{
			name:     "already complete is returned byte-identical",
			existing: `SNAPPER_CONFIGS="root home"` + "\n",
			names:    []string{"home", "root"},
			want:     `SNAPPER_CONFIGS="root home"` + "\n",
		},
		{
			name: "other variables and comments are preserved",
			existing: `# /etc/conf.d/snapper
SNAPPER_FSTYPE="btrfs"
SNAPPER_CONFIGS="root"

# trailing note
`,
			names: []string{"root", "home"},
			want: `# /etc/conf.d/snapper
SNAPPER_FSTYPE="btrfs"
SNAPPER_CONFIGS="root home"

# trailing note
`,
		},
		{
			name:     "commented-out assignment counts as absent",
			existing: `#SNAPPER_CONFIGS="old"` + "\n",
			names:    []string{"root"},
			want:     `#SNAPPER_CONFIGS="old"` + "\n" + `SNAPPER_CONFIGS="root"` + "\n",
		},
		{
			name:     "single-quoted value is read and re-emitted in its own style",
			existing: `SNAPPER_CONFIGS='root'` + "\n",
			names:    []string{"root", "home"},
			want:     `SNAPPER_CONFIGS='root home'` + "\n",
		},
		{
			name:     "complete single-quoted value is never restyled",
			existing: `SNAPPER_CONFIGS='root home'` + "\n",
			names:    []string{"root", "home"},
			want:     `SNAPPER_CONFIGS='root home'` + "\n",
		},
		{
			name:     "unquoted value is read without corruption",
			existing: "SNAPPER_CONFIGS=root\n",
			names:    []string{"root", "home"},
			want:     `SNAPPER_CONFIGS="root home"` + "\n",
		},
		{
			name:     "complete unquoted value is left alone",
			existing: "SNAPPER_CONFIGS=root\n",
			names:    []string{"root"},
			want:     "SNAPPER_CONFIGS=root\n",
		},
		{
			name:     "empty value is filled in",
			existing: `SNAPPER_CONFIGS=""` + "\n",
			names:    []string{"root"},
			want:     `SNAPPER_CONFIGS="root"` + "\n",
		},
		{
			name: "only the first active assignment is updated",
			existing: `SNAPPER_CONFIGS="root"
SNAPPER_CONFIGS="stale"
`,
			names: []string{"home"},
			want: `SNAPPER_CONFIGS="root home"
SNAPPER_CONFIGS="stale"
`,
		},
		{
			name:  "repeated names are de-duplicated against themselves",
			names: []string{"root", "root", "home"},
			want:  `SNAPPER_CONFIGS="root home"` + "\n",
		},
		{
			name:     "no names leaves an existing assignment untouched",
			existing: `SNAPPER_CONFIGS="root"` + "\n",
			want:     `SNAPPER_CONFIGS="root"` + "\n",
		},
		{
			name:     "a missing final newline is not invented",
			existing: `SNAPPER_CONFIGS="root"`,
			names:    []string{"root"},
			want:     `SNAPPER_CONFIGS="root"`,
		},
		{
			name:     "appending closes an unterminated last line",
			existing: "# no trailing newline",
			names:    []string{"root"},
			want:     "# no trailing newline\n" + `SNAPPER_CONFIGS="root"` + "\n",
		},
		{
			name:     "a same-line comment survives the rewrite",
			existing: `SNAPPER_CONFIGS="root" # managed` + "\n",
			names:    []string{"home"},
			want:     `SNAPPER_CONFIGS="root home" # managed` + "\n",
		},
		{
			name:     "a name holding whitespace is refused",
			existing: "",
			names:    []string{"root", "with space"},
			want:     `SNAPPER_CONFIGS="root"` + "\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeSnapperConfigsLine(tc.existing, tc.names)
			if got != tc.want {
				t.Errorf("mergeSnapperConfigsLine(%q, %q)\n--- got ---\n%q\n--- want ---\n%q",
					tc.existing, tc.names, got, tc.want)
			}
		})
	}
}

// TestMergeSnapperConfigsLine_Idempotent: merging over the merge's own output
// changes nothing, whatever shape the operator's file started in — so applying
// twice over an unchanged config leaves identical on-disk state (R5.1, R1.2).
func TestMergeSnapperConfigsLine_Idempotent(t *testing.T) {
	names := []string{"root", "home", "var_log"}
	seeds := []string{
		"",
		`SNAPPER_CONFIGS=""` + "\n",
		`SNAPPER_CONFIGS='root'` + "\n",
		"SNAPPER_CONFIGS=root\n",
		"# only a comment, no assignment\n",
		`#SNAPPER_CONFIGS="old"` + "\n",
		`SNAPPER_FSTYPE="btrfs"` + "\n" + `SNAPPER_CONFIGS="home"` + "\n",
		`SNAPPER_CONFIGS="root" # managed` + "\n",
		`SNAPPER_CONFIGS="root"`, // no trailing newline
	}

	for _, seed := range seeds {
		first := mergeSnapperConfigsLine(seed, names)
		// Guard the idempotency check itself: a merge that returned its input
		// unchanged would trivially "repeat" without ever registering a config.
		for _, n := range names {
			if !strings.Contains(first, n) {
				t.Errorf("seed %q: merged content is missing %q:\n%q", seed, n, first)
			}
		}
		if second := mergeSnapperConfigsLine(first, names); second != first {
			t.Errorf("seed %q: second pass differs\n--- first ---\n%q\n--- second ---\n%q",
				seed, first, second)
		}
	}
}

// ---------------------------------------------------------------------------
// Story 016 C2 — persisting the merge to /etc/conf.d/snapper (R1).
//
// ensureSnapperRegistered is the I/O half of the fix. Every test here drives it
// over the snapperConfdPath seam pointed at a temp file, so the real
// /etc/conf.d/snapper is never read and never written.
// ---------------------------------------------------------------------------

// TestSnapperRegister_CreatesFileWhenAbsent: with no /etc/conf.d/snapper on
// disk, registration creates it holding a SNAPPER_CONFIGS line that lists every
// managed name (R1.3, R1.1). The mode is asserted because 0644 is deliberate —
// this is world-readable shell config, unlike the 0640 per-subvolume configs.
func TestSnapperRegister_CreatesFileWhenAbsent(t *testing.T) {
	path := stubSnapperConfdPath(t)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("precondition: %s must not exist yet (stat err = %v)", path, err)
	}

	if err := ensureSnapperRegistered([]string{"root", "home"}); err != nil {
		t.Fatalf("ensureSnapperRegistered: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("conf.d file was not created: %v", err)
	}
	want := `SNAPPER_CONFIGS="root home"` + "\n"
	if string(got) != want {
		t.Errorf("content\n--- got ---\n%q\n--- want ---\n%q", got, want)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after write: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Errorf("mode = %#o, want 0644", perm)
	}
}

// TestSnapperRegister_SecondCallIsNoOp: registering twice leaves byte-identical
// content, so a name is never duplicated and repeated `apply` runs converge
// (R1.2, R5.1).
func TestSnapperRegister_SecondCallIsNoOp(t *testing.T) {
	path := stubSnapperConfdPath(t)
	names := []string{"root", "home", "var_log"}

	if err := ensureSnapperRegistered(names); err != nil {
		t.Fatalf("first ensureSnapperRegistered: %v", err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after first call: %v", err)
	}
	// Guard the idempotency assertion itself: a call that registered nothing at
	// all would satisfy "identical twice" without ever fixing the bug.
	for _, n := range names {
		if !strings.Contains(string(first), n) {
			t.Fatalf("first call did not register %q:\n%s", n, first)
		}
	}

	if err := ensureSnapperRegistered(names); err != nil {
		t.Fatalf("second ensureSnapperRegistered: %v", err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after second call: %v", err)
	}
	if string(second) != string(first) {
		t.Errorf("second call rewrote the file\n--- first ---\n%q\n--- second ---\n%q",
			first, second)
	}
}

// TestSnapperRegister_PreservesExistingFile: registering over an operator's own
// /etc/conf.d/snapper appends only the missing name — the already-listed one is
// not duplicated (R1.2) and every other variable, comment, and blank line
// survives byte for byte (R1.4). The seed mirrors the file snapper actually
// ships, so the assertion is against a realistic layout.
func TestSnapperRegister_PreservesExistingFile(t *testing.T) {
	path := stubSnapperConfdPath(t)
	existing := `## Path: System/Snapper

## Type:        string
## Default:     ""
# List of snapper configurations.
SNAPPER_CONFIGS="root"
SNAPPER_FSTYPE="btrfs"
`
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := ensureSnapperRegistered([]string{"root", "home"}); err != nil {
		t.Fatalf("ensureSnapperRegistered: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	want := `## Path: System/Snapper

## Type:        string
## Default:     ""
# List of snapper configurations.
SNAPPER_CONFIGS="root home"
SNAPPER_FSTYPE="btrfs"
`
	if string(got) != want {
		t.Errorf("content\n--- got ---\n%q\n--- want ---\n%q", got, want)
	}
}

// TestSnapperRegister_EnsureSnapperConfigsRegisters is the wiring guard, and the
// regression test for the bug itself: ensureSnapperConfigs must not stop at
// writing /etc/snapper/configs/<name>: unless each name also lands in
// SNAPPER_CONFIGS, snapper cannot enumerate the config and every subvolume fails
// with "unknown config" (R1.1). The per-subvolume writes are re-asserted so the
// added registration cannot mask a regression in them (R2.1).
func TestSnapperRegister_EnsureSnapperConfigsRegisters(t *testing.T) {
	dir := stubSnapperConfigsDir(t)
	confd := stubSnapperConfdPath(t)
	cfg := &Config{Engine: EngineConfig{
		Driver:     "snapper",
		Subvolumes: []string{"/", "/home", "/var/log"},
	}}

	if err := ensureSnapperConfigs(cfg); err != nil {
		t.Fatalf("ensureSnapperConfigs: %v", err)
	}

	got, err := os.ReadFile(confd)
	if err != nil {
		t.Fatalf("ensureSnapperConfigs did not register anything: %v", err)
	}
	want := `SNAPPER_CONFIGS="root home var_log"` + "\n"
	if string(got) != want {
		t.Errorf("registration\n--- got ---\n%q\n--- want ---\n%q", got, want)
	}
	for _, name := range []string{"root", "home", "var_log"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("per-subvolume config %s not written: %v", name, err)
		}
	}
}
