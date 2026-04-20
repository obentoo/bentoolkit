package output

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/fatih/color"
	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// captureStdout captures output written to os.Stdout and color.Output during fn execution.
// The color package writes to color.Output (defaults to os.Stdout), so both must be redirected.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	oldStdout := os.Stdout
	oldColorOut := color.Output
	os.Stdout = w
	color.Output = w
	fn()
	w.Close()
	os.Stdout = oldStdout
	color.Output = oldColorOut
	out, _ := io.ReadAll(r)
	return string(out)
}

// captureStderr captures output written to os.Stderr and color.Error during fn execution.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	oldStderr := os.Stderr
	oldColorErr := color.Error
	os.Stderr = w
	color.Error = w
	fn()
	w.Close()
	os.Stderr = oldStderr
	color.Error = oldColorErr
	out, _ := io.ReadAll(r)
	return string(out)
}

// TestPrintSuccess verifies PrintSuccess writes to stdout with "✓" prefix.
// Requirements: 6.1
func TestPrintSuccess(t *testing.T) {
	NoColor()
	defer ForceColor()

	cases := []struct {
		format string
		args   []interface{}
		want   string
	}{
		{"done", nil, "✓ done"},
		{"item %s", []interface{}{"foo"}, "✓ item foo"},
		{"count %d", []interface{}{42}, "✓ count 42"},
	}
	for _, tc := range cases {
		out := captureStdout(t, func() {
			PrintSuccess(tc.format, tc.args...)
		})
		if !strings.Contains(out, tc.want) {
			t.Errorf("PrintSuccess(%q) = %q, want contains %q", tc.format, out, tc.want)
		}
	}
}

// TestPrintError verifies PrintError writes to stderr with "✗" prefix.
// Requirements: 6.2
func TestPrintError(t *testing.T) {
	NoColor()
	defer ForceColor()

	cases := []struct {
		format string
		args   []interface{}
		want   string
	}{
		{"failed", nil, "✗ failed"},
		{"error %s", []interface{}{"oops"}, "✗ error oops"},
	}
	for _, tc := range cases {
		out := captureStderr(t, func() {
			PrintError(tc.format, tc.args...)
		})
		if !strings.Contains(out, tc.want) {
			t.Errorf("PrintError(%q) = %q, want contains %q", tc.format, out, tc.want)
		}
	}
}

// TestPrintWarning verifies PrintWarning writes to stdout with "⚠" prefix.
// Requirements: 6.3
func TestPrintWarning(t *testing.T) {
	NoColor()
	defer ForceColor()

	cases := []struct {
		format string
		args   []interface{}
		want   string
	}{
		{"caution", nil, "⚠ caution"},
		{"warn %s", []interface{}{"here"}, "⚠ warn here"},
	}
	for _, tc := range cases {
		out := captureStdout(t, func() {
			PrintWarning(tc.format, tc.args...)
		})
		if !strings.Contains(out, tc.want) {
			t.Errorf("PrintWarning(%q) = %q, want contains %q", tc.format, out, tc.want)
		}
	}
}

// TestPrintInfo verifies PrintInfo writes to stdout with "→" prefix.
// Requirements: 6.4
func TestPrintInfo(t *testing.T) {
	NoColor()
	defer ForceColor()

	cases := []struct {
		format string
		args   []interface{}
		want   string
	}{
		{"note", nil, "→ note"},
		{"info %s", []interface{}{"msg"}, "→ info msg"},
	}
	for _, tc := range cases {
		out := captureStdout(t, func() {
			PrintInfo(tc.format, tc.args...)
		})
		if !strings.Contains(out, tc.want) {
			t.Errorf("PrintInfo(%q) = %q, want contains %q", tc.format, out, tc.want)
		}
	}
}

// TestStatusColorUnknown verifies StatusColor returns reset color for unknown status.
// Requirements: 6.8
func TestStatusColorUnknown(t *testing.T) {
	c := StatusColor("Unknown")
	if c == nil {
		t.Fatal("StatusColor(unknown) returned nil")
	}
	// Reset color should produce no ANSI codes when NoColor is set
	NoColor()
	defer ForceColor()
	result := c.Sprint("x")
	if result != "x" {
		t.Errorf("reset color Sprint = %q, want %q", result, "x")
	}
}

// TestFormatPackage verifies FormatPackage output contains category and package name.
// Requirements: 6.5, 6.6
func TestFormatPackage(t *testing.T) {
	NoColor()
	defer ForceColor()

	cases := []struct {
		category string
		pkg      string
		wantCat  bool
	}{
		{"app-editors", "vim", true},
		{"sys-apps", "util-linux", true},
		{"", "standalone", false},
	}
	for _, tc := range cases {
		result := FormatPackage(tc.category, tc.pkg)
		if !strings.Contains(result, tc.pkg) {
			t.Errorf("FormatPackage(%q,%q) = %q, missing pkg", tc.category, tc.pkg, result)
		}
		if tc.wantCat && !strings.Contains(result, tc.category) {
			t.Errorf("FormatPackage(%q,%q) = %q, missing category", tc.category, tc.pkg, result)
		}
	}
}

// TestBox verifies Box output contains title, content, and box drawing characters.
// Requirements: 6.7
func TestBox(t *testing.T) {
	NoColor()
	defer ForceColor()

	title := "My Title"
	content := "Some content here"
	out := captureStdout(t, func() {
		Box(title, content)
	})

	for _, want := range []string{title, content, "┌", "└", "│"} {
		if !strings.Contains(out, want) {
			t.Errorf("Box() output missing %q\nGot: %q", want, out)
		}
	}
}

// TestSprint verifies Sprint returns a colored string without printing.
// Requirements: 6.9
func TestSprint(t *testing.T) {
	NoColor()
	defer ForceColor()

	result := Sprint(Success, "hello")
	if result != "hello" {
		t.Errorf("Sprint = %q, want %q", result, "hello")
	}
}

// TestPrintln verifies Println writes to stdout with color.
// Requirements: 6.10
func TestPrintln(t *testing.T) {
	NoColor()
	defer ForceColor()

	out := captureStdout(t, func() {
		Println(Info, "test line")
	})
	if !strings.Contains(out, "test line") {
		t.Errorf("Println output = %q, want contains %q", out, "test line")
	}
}

// TestPrintf verifies Printf writes to stdout with color.
// Requirements: 6.10
func TestPrintf(t *testing.T) {
	NoColor()
	defer ForceColor()

	out := captureStdout(t, func() {
		Printf(Warning, "val=%d", 7)
	})
	if !strings.Contains(out, "val=7") {
		t.Errorf("Printf output = %q, want contains %q", out, "val=7")
	}
}

// TestPrintErrorGoesToStderr verifies PrintError does NOT write to stdout.
// Requirements: 6.2
func TestPrintErrorGoesToStderr(t *testing.T) {
	NoColor()
	defer ForceColor()

	stdout := captureStdout(t, func() {
		// Redirect stderr to /dev/null to avoid noise
		old := os.Stderr
		os.Stderr, _ = os.Open(os.DevNull)
		defer func() { os.Stderr = old }()
		PrintError("silent error")
	})
	if strings.Contains(stdout, "silent error") {
		t.Error("PrintError wrote to stdout, expected stderr only")
	}
}

// --- Property-Based Tests ---

// TestPrintFunctionsPrefixCorrectness tests Property 4: Print functions prefix correctness.
// **Feature: test-coverage-improvement, Property 4: Print functions prefix correctness**
// **Validates: Requirements 6.1, 6.2, 6.3, 6.4**
func TestPrintFunctionsPrefixCorrectness(t *testing.T) {
	NoColor()
	defer ForceColor()

	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	msgGen := gen.RegexMatch(`[a-zA-Z][a-zA-Z0-9 ]{1,20}`)

	properties.Property("PrintSuccess output contains ✓", prop.ForAll(
		func(msg string) bool {
			out := captureStdout(t, func() { PrintSuccess("%s", msg) })
			return strings.Contains(out, "✓")
		},
		msgGen,
	))

	properties.Property("PrintWarning output contains ⚠", prop.ForAll(
		func(msg string) bool {
			out := captureStdout(t, func() { PrintWarning("%s", msg) })
			return strings.Contains(out, "⚠")
		},
		msgGen,
	))

	properties.Property("PrintInfo output contains →", prop.ForAll(
		func(msg string) bool {
			out := captureStdout(t, func() { PrintInfo("%s", msg) })
			return strings.Contains(out, "→")
		},
		msgGen,
	))

	properties.Property("PrintError output contains ✗", prop.ForAll(
		func(msg string) bool {
			out := captureStderr(t, func() { PrintError("%s", msg) })
			return strings.Contains(out, "✗")
		},
		msgGen,
	))

	properties.TestingRun(t)
}

// TestFormatPackageContentCorrectness tests Property 5: FormatPackage content correctness.
// **Feature: test-coverage-improvement, Property 5: FormatPackage content correctness**
// **Validates: Requirements 6.5**
func TestFormatPackageContentCorrectness(t *testing.T) {
	NoColor()
	defer ForceColor()

	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	nonEmptyAlpha := gen.RegexMatch(`[a-z][a-z0-9-]{1,15}`)

	properties.Property("FormatPackage contains category and pkg", prop.ForAll(
		func(category, pkg string) bool {
			result := FormatPackage(category, pkg)
			return strings.Contains(result, category) && strings.Contains(result, pkg)
		},
		nonEmptyAlpha,
		nonEmptyAlpha,
	))

	properties.TestingRun(t)
}

// TestBoxOutputContentCorrectness tests Property 6: Box output content correctness.
// **Feature: test-coverage-improvement, Property 6: Box output content correctness**
// **Validates: Requirements 6.7**
func TestBoxOutputContentCorrectness(t *testing.T) {
	NoColor()
	defer ForceColor()

	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	nonEmptyAlpha := gen.RegexMatch(`[a-zA-Z][a-zA-Z0-9 ]{1,20}`)

	properties.Property("Box output contains title, content, and box chars", prop.ForAll(
		func(title, content string) bool {
			out := captureStdout(t, func() { Box(title, content) })
			return strings.Contains(out, title) &&
				strings.Contains(out, content) &&
				strings.Contains(out, "┌") &&
				strings.Contains(out, "└") &&
				strings.Contains(out, "│")
		},
		nonEmptyAlpha,
		nonEmptyAlpha,
	))

	properties.TestingRun(t)
}
