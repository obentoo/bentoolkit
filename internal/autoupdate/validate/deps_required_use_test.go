package validate

import (
	"errors"
	"fmt"
	"os"
	"testing"
)

// The fixture is emerge's own stderr for a python-single-r1 ebuild whose
// PYTHON_COMPAT omits the only PYTHON_SINGLE_TARGET a host enables.
func TestRequiredUseConstraintsFromEmerge(t *testing.T) {
	out, err := os.ReadFile("testdata/emerge-required-use.txt")
	if err != nil {
		t.Fatal(err)
	}
	got := requiredUseConstraints(string(out))
	want := "python? ( exactly-one-of ( python_single_target_python3_12 python_single_target_python3_13 python_single_target_python3_14 ) )"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("constraints = %q, want [%q]", got, want)
	}
}

func TestRequiredUseConstraintsAbsent(t *testing.T) {
	if got := requiredUseConstraints("!!! All ebuilds that could satisfy \"x\" have been masked.\n"); got != nil {
		t.Fatalf("constraints = %q, want none", got)
	}
}

func TestRequiredUseErrorStaysUndetermined(t *testing.T) {
	cause := errors.New("exit status 1")
	err := fmt.Errorf("%w: %w", ErrDependenciesUndetermined, &RequiredUseError{Constraints: []string{"a? ( b )"}, cause: cause})
	var rue *RequiredUseError
	if !errors.As(err, &rue) || !errors.Is(err, ErrDependenciesUndetermined) || !errors.Is(err, cause) {
		t.Fatalf("err = %v does not carry both answers and the cause", err)
	}
	if rue.Error() != "REQUIRED_USE unsatisfied: a? ( b )" {
		t.Errorf("Error() = %q", rue.Error())
	}
}
