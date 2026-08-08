package validate

// Authored for story 031, sub-task 2.2 — R1, R1.1, R1.2.
//
// Written from the contract. design.md D3 fixes the Option shape; it does NOT
// name the parser, so this file pins `parseMesonOptions(data []byte) []Option`.
// That name is the one negotiable thing here — every assertion below is about
// behaviour and survives a rename.
//
// Red is DEFERRED to Run mode: the package does not exist yet.

import "testing"

// names reduces a parse result to the option names, in file order, so the
// assertions read as the file does.
func names(opts []Option) []string {
	out := make([]string, 0, len(opts))
	for _, o := range opts {
		out = append(out, o.Name)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestParseMesonOptions_QuoteStyles covers both quotings Meson accepts. A
// parser that handles only one silently under-reports the upstream side, and
// an under-reported upstream turns every missing name into a false error.
func TestParseMesonOptions_QuoteStyles(t *testing.T) {
	src := []byte(`
option('aalib', type : 'feature', value : 'auto')
option("libcaca", type : "feature", value : "auto")
`)

	got := names(parseMesonOptions(src))

	if want := []string{"aalib", "libcaca"}; !equalStrings(got, want) {
		t.Errorf("parseMesonOptions: got %v, want %v", got, want)
	}
}

// TestParseMesonOptions_MultiLineDeclaration covers a declaration spread over
// several lines, which is how most real option files are written.
func TestParseMesonOptions_MultiLineDeclaration(t *testing.T) {
	src := []byte(`
option(
    'qt6',
    type : 'feature',
    value : 'auto',
    description : 'Qt6 plugin',
)
`)

	got := names(parseMesonOptions(src))

	if want := []string{"qt6"}; !equalStrings(got, want) {
		t.Errorf("parseMesonOptions: got %v, want %v", got, want)
	}
}

// TestParseMesonOptions_CommentIsNotADeclaration guards the other direction: a
// name read out of a comment is an option the build never accepts, so it would
// mask a real finding by making an undeclared option look declared.
func TestParseMesonOptions_CommentIsNotADeclaration(t *testing.T) {
	src := []byte(`
# option('removed_upstream', type : 'feature')
option('kept', type : 'feature')
`)

	got := names(parseMesonOptions(src))

	if want := []string{"kept"}; !equalStrings(got, want) {
		t.Errorf("parseMesonOptions: got %v, want %v — a commented declaration is not a declaration", got, want)
	}
}

// TestParseMesonOptions_EmptyFileIsZeroOptions pins that "no options" is an
// answer rather than a failure: a Meson project may legitimately declare none.
func TestParseMesonOptions_EmptyFileIsZeroOptions(t *testing.T) {
	got := parseMesonOptions([]byte("\n# nothing here\n"))

	if len(got) != 0 {
		t.Errorf("parseMesonOptions: got %d options, want 0", len(got))
	}
}

// TestParseMesonOptions_PreservesFileOrder pins the ordering the report relies
// on to stay stable between runs.
func TestParseMesonOptions_PreservesFileOrder(t *testing.T) {
	src := []byte("option('zeta')\noption('alpha')\noption('mu')\n")

	got := names(parseMesonOptions(src))

	if want := []string{"zeta", "alpha", "mu"}; !equalStrings(got, want) {
		t.Errorf("parseMesonOptions: got %v, want file order %v", got, want)
	}
}
