package autoupdate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// substituteGateGoodHash is a well-formed commit hash: 40 lowercase hex.
const substituteGateGoodHash = "bc99a094bd926ae7b5ab8643947ce1438c950720"

// substituteGateTarget returns an applier over a fresh untrusted fixture and the
// path of an ebuild on disk that carries both the aux assignment and the commit
// variable — the file applySubstitutions is pointed at.
func substituteGateTarget(t *testing.T) (*Applier, string, string) {
	t.Helper()
	f := newUntrustedFixture(t, PendingUpdate{})
	a := f.applier(t)
	path := filepath.Join(f.pkgDir, "betterbird-bin-"+f.oldVersion+".ebuild")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("fixture ebuild missing: %v", err)
	}
	return a, f.pkg, path
}

// TestApplySubstitutionsRefusesMalformedValues pins S050-R1.7: the one function
// every writer of a new ebuild calls refuses a malformed value itself, so a
// writer that skips the early gates in Apply and Validate still cannot write it.
//
// The two mixed cases are the hostile half. A check placed beside each
// substitution, rather than before both, would write the well-formed value first
// and only then refuse the malformed one — a refusal that still leaves the file
// changed. R1.7 asks for the file byte-identical, so the check has to come before
// any write.
func TestApplySubstitutionsRefusesMalformedValues(t *testing.T) {
	cases := []struct {
		name     string
		update   PendingUpdate
		sentinel error
	}{
		{"quote-escape aux", PendingUpdate{AuxValue: `x"; touch /tmp/pwned; "`}, ErrInvalidAuxValue},
		{"template reference aux", PendingUpdate{AuxValue: "a${1}b"}, ErrInvalidAuxValue},
		{"newline aux", PendingUpdate{AuxValue: "esr-bb24\nrm -rf /"}, ErrInvalidAuxValue},
		{"uppercase hash", PendingUpdate{CommitHash: strings.ToUpper(substituteGateGoodHash)}, ErrInvalidCommitHash},
		{"39-hex hash", PendingUpdate{CommitHash: substituteGateGoodHash[:39]}, ErrInvalidCommitHash},
		{"good hash then malformed aux", PendingUpdate{CommitHash: substituteGateGoodHash, AuxValue: `x"; id; "`}, ErrInvalidAuxValue},
		{"good aux then uppercase hash", PendingUpdate{AuxValue: "esr-bb24", CommitHash: strings.ToUpper(substituteGateGoodHash)}, ErrInvalidCommitHash},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, pkg, path := substituteGateTarget(t)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read before: %v", err)
			}

			update := tc.update
			err = a.applySubstitutions(path, pkg, &update)
			if err == nil {
				t.Fatalf("applySubstitutions accepted %+v", tc.update)
			}
			if !errors.Is(err, tc.sentinel) {
				t.Errorf("error = %v, want it to wrap %v", err, tc.sentinel)
			}

			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read after: %v", err)
			}
			if string(after) != string(before) {
				t.Errorf("a refused value changed the ebuild\nbefore:\n%s\nafter:\n%s", before, after)
			}
		})
	}
}

// TestApplySubstitutionsWritesWellFormedValues is the regression guard for R1.7:
// the gate at the seam must not refuse what the allow-lists admit. Green before
// the gate lands, and must stay green after.
func TestApplySubstitutionsWritesWellFormedValues(t *testing.T) {
	cases := []struct {
		name   string
		update PendingUpdate
		want   []string
	}{
		{"aux esr-bb24", PendingUpdate{AuxValue: "esr-bb24"}, []string{`MY_BUILD="esr-bb24"` + "\n"}},
		{"lowercase 40-hex hash", PendingUpdate{CommitHash: substituteGateGoodHash}, []string{`EGIT_COMMIT="` + substituteGateGoodHash + `"` + "\n"}},
		{"both at once", PendingUpdate{AuxValue: "esr-bb24", CommitHash: substituteGateGoodHash}, []string{
			`MY_BUILD="esr-bb24"` + "\n",
			`EGIT_COMMIT="` + substituteGateGoodHash + `"` + "\n",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, pkg, path := substituteGateTarget(t)
			update := tc.update
			if err := a.applySubstitutions(path, pkg, &update); err != nil {
				t.Fatalf("applySubstitutions refused a well-formed value: %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read after: %v", err)
			}
			for _, want := range tc.want {
				if !strings.Contains(string(got), want) {
					t.Errorf("ebuild lacks %q:\n%s", want, got)
				}
			}
		})
	}
}
