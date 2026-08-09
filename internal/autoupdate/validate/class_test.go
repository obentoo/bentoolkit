package validate

// Authored for story 033, sub-task 1.1 — R1, R1.1, R1.2, R1.3, R1.4, R1.5, R1.6.
//
// HOW FAR A VERSION MOVED, FROM THE TWO STRINGS AND NOTHING ELSE. The
// classification decides how much validation a bump earns, so it has to be
// reproducible and free: no filesystem, no network, no registry lookup, no
// clock (R1.6). That is asserted below rather than assumed, because the cheapest
// way to "improve" a classifier is to let it peek at something.
//
// R1.5 IS THE INTERESTING ONE. An unparseable version returns ClassMajor — the
// class carrying the greatest depth — AND an error naming the string it could
// not parse. Both, not either. Returning the safe class silently is precisely
// the failure mode the requirement exists to prevent: the bump would be
// validated at maximum depth for a reason nobody could see, and the next reader
// would "optimise" the mystery away.
//
// This file pins `type Class` with `ClassRevision, ClassPatch, ClassSeries,
// ClassMajor` and `Classify(oldPV, newPV string) (Class, error)`.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestClassify_TheFourClasses is the table the requirement is written as. Each
// row is one boundary of the ladder, and the rows below the four canonical cases
// are the ones that decide whether the boundaries were drawn or guessed.
func TestClassify_TheFourClasses(t *testing.T) {
	tests := []struct {
		name     string
		old, new string
		want     Class
	}{
		// R1.1 — only the Portage revision suffix moved. Nothing upstream
		// changed at all, so this is the cheapest bump there is.
		{"revision suffix appears", "1.28.6", "1.28.6-r1", ClassRevision},
		{"revision suffix increments", "1.28.6-r1", "1.28.6-r2", ClassRevision},
		{"revision suffix disappears", "1.28.6-r1", "1.28.6", ClassRevision},

		// R1.2 — every component above the last matches.
		{"patch component", "1.28.5", "1.28.6", ClassPatch},
		{"patch component over ten", "1.28.9", "1.28.10", ClassPatch},
		{"patch and a new revision", "1.28.5", "1.28.6-r1", ClassPatch},

		// R1.3 — the series component moved and the one above it did not.
		{"series crossing", "1.28.6", "1.29.2", ClassSeries},
		{"series crossing downward in patch", "1.28.6", "1.29.0", ClassSeries},

		// R1.4 — the leading component moved.
		{"major bump", "1.28.6", "2.0.0", ClassMajor},
		{"major bump from a two-component version", "1.28", "2.0", ClassMajor},
		{"major bump by more than one", "1.28.6", "3.0.0", ClassMajor},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Classify(tt.old, tt.new)
			if err != nil {
				t.Fatalf("Classify(%q, %q): unexpected error %v — both versions parse", tt.old, tt.new, err)
			}
			if got != tt.want {
				t.Errorf("Classify(%q, %q) = %v, want %v", tt.old, tt.new, got, tt.want)
			}
		})
	}
}

// TestClassify_UnparseableReturnsTheDeepestClassAndSaysWhichString is R1.5, and
// the two halves are asserted separately because either one alone is a defect:
// the class without the error is a silent maximum, and the error without the
// class leaves the caller with nothing to act on.
func TestClassify_UnparseableReturnsTheDeepestClassAndSaysWhichString(t *testing.T) {
	tests := []struct {
		name      string
		old, new  string
		offending string
	}{
		{"garbage on the right", "1.28.6", "not-a-version", "not-a-version"},
		{"garbage on the left", "INKSCAPE_1_4_4", "1.29.2", "INKSCAPE_1_4_4"},
		{"empty new version", "1.28.6", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Classify(tt.old, tt.new)

			if got != ClassMajor {
				t.Errorf("Classify(%q, %q) = %v, want ClassMajor — an unreadable version earns the greatest depth",
					tt.old, tt.new, got)
			}
			if err == nil {
				t.Fatalf("Classify(%q, %q) returned no error; the caller cannot then say WHY the bump is being validated at maximum depth",
					tt.old, tt.new)
			}
			if tt.offending != "" && !strings.Contains(err.Error(), tt.offending) {
				t.Errorf("the error %q does not name the string it could not parse (%q)", err, tt.offending)
			}
		})
	}
}

// TestClassify_IsSymmetricAboutTheDirectionOfTheMove pins that the classifier
// answers about the SIZE of the move, not its sign. A downgrade is refused
// elsewhere (Apply's version guard); this function must not quietly disagree
// with itself about how far apart two versions are.
func TestClassify_IsSymmetricAboutTheDirectionOfTheMove(t *testing.T) {
	pairs := [][2]string{
		{"1.28.6", "1.29.2"},
		{"1.28.5", "1.28.6"},
		{"1.28.6", "2.0.0"},
	}

	for _, p := range pairs {
		forward, err := Classify(p[0], p[1])
		if err != nil {
			t.Fatalf("Classify(%q, %q): %v", p[0], p[1], err)
		}
		backward, err := Classify(p[1], p[0])
		if err != nil {
			t.Fatalf("Classify(%q, %q): %v", p[1], p[0], err)
		}
		if forward != backward {
			t.Errorf("Classify(%q, %q) = %v but Classify(%q, %q) = %v; the class describes the distance, not the direction",
				p[0], p[1], forward, p[1], p[0], backward)
		}
	}
}

// TestClassify_IdenticalVersionsAreTheCheapestClass pins the degenerate input
// rather than leaving it to whoever hits it first. Re-applying the same version
// is a real path (a revive, a retry), and it must not be classified as a major
// bump and charged a compile.
func TestClassify_IdenticalVersionsAreTheCheapestClass(t *testing.T) {
	got, err := Classify("1.28.6", "1.28.6")
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != ClassRevision {
		t.Errorf("Classify(%q, %q) = %v, want ClassRevision — nothing moved", "1.28.6", "1.28.6", got)
	}
}

// TestClassify_ReadsNothingButTheTwoStrings is R1.6, and it is asserted three
// ways because "derives from the two strings alone" is a negative and a negative
// needs boxing in: no child process, no file created, and no file read from the
// working directory.
//
// The package-wide import assertion (TestValidatePackage_ReachesNoNetwork in
// run_test.go) already covers the network; this covers the rest.
func TestClassify_ReadsNothingButTheTwoStrings(t *testing.T) {
	// Any child process at all is a failure: the classification is arithmetic
	// on two strings.
	var spawned []string
	origExec := execCommand
	execCommand = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		spawned = append(spawned, name+" "+strings.Join(arg, " "))
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { execCommand = origExec })

	origLook := lookPath
	lookPath = func(name string) (string, error) {
		spawned = append(spawned, "lookPath("+name+")")
		return "", os.ErrNotExist
	}
	t.Cleanup(func() { lookPath = origLook })

	// An empty working directory, watched for creations.
	work := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(work); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	for _, pair := range [][2]string{
		{"1.28.6", "1.29.2"},
		{"1.28.6", "not-a-version"},
	} {
		_, _ = Classify(pair[0], pair[1])
	}

	if len(spawned) != 0 {
		t.Errorf("Classify reached outside the process: %v", spawned)
	}
	entries, err := os.ReadDir(work)
	if err != nil {
		t.Fatalf("reading %q: %v", work, err)
	}
	if len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, filepath.Join(work, e.Name()))
		}
		t.Errorf("Classify wrote %v; it derives a classification from the two version strings alone (R1.6)", names)
	}
}
