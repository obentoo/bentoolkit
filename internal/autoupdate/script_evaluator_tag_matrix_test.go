// Story 069, sub-task 2.1 (R5.1-R5.4): the lint, vulnerability and tagged
// build matrix follow the single browser backend.
//
// golangci-lint, govulncheck and `go build` all skip a file behind a tag they
// were not given, so each loop below is the only thing that ever looks at the
// chromedp evaluator. Dropping chromedp from one leaves that file unchecked and
// still green; keeping the removed tag makes the loop fail on the legacy-tag
// guard. Both directions are asserted.

package autoupdate

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// matrixModuleRoot walks up from the package directory to this module's root.
func matrixModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil {
			if !regexp.MustCompile(`(?m)^module\s+github\.com/obentoo/bentoolkit\s*$`).Match(data) {
				t.Fatalf("%s/go.mod is not the bentoolkit module", dir)
			}
			return dir
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("reading %s/go.mod: %v", dir, err)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the package directory")
		}
		dir = parent
	}
}

// matrixLoop matches a shell `for tag in WORDS; do` header.
var matrixLoop = regexp.MustCompile(`(?m)^\s*for\s+tag\s+in\s+([^;\n]*);\s*do\b`)

// matrixWords splits a shell word list, honouring single and double quotes, so
// `"" chromedp` is the two words "" and "chromedp", and `"chromedp other"` is
// one word.
func matrixWords(list string) []string {
	var words []string
	var cur strings.Builder
	inWord := false
	var quote rune
	for _, r := range list {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote = r
			inWord = true
		case r == ' ' || r == '\t':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words
}

// matrixLoops returns the word lists of every tag loop in body.
func matrixLoops(body string) [][]string {
	var out [][]string
	for _, m := range matrixLoop.FindAllStringSubmatch(body, -1) {
		out = append(out, matrixWords(m[1]))
	}
	return out
}

// matrixRequire compares a loop's tag sets against want, exactly: no extra
// tag set (the removed backend, or a combined "chromedp playwright"), no
// missing one (the default build or chromedp), no duplicate.
func matrixRequire(t *testing.T, where string, got, want []string) {
	t.Helper()
	g := append([]string(nil), got...)
	w := append([]string(nil), want...)
	sort.Strings(g)
	sort.Strings(w)
	if strings.Join(g, "\x00") != strings.Join(w, "\x00") {
		t.Errorf("%s iterates tag sets %q, want exactly %q", where, got, want)
	}
}

// matrixMakeRecipe returns the recipe lines of a Makefile target.
func matrixMakeRecipe(makefile, target string) string {
	var b strings.Builder
	in := false
	for _, line := range strings.Split(makefile, "\n") {
		if strings.HasPrefix(line, target+":") {
			in = true
			continue
		}
		if !in {
			continue
		}
		if strings.HasPrefix(line, "\t") {
			b.WriteString(line)
			b.WriteByte('\n')
			continue
		}
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		break
	}
	return b.String()
}

func TestSingleBackendLintAndBuildTagMatrix(t *testing.T) {
	root := matrixModuleRoot(t)

	// R5.1 - make lint.
	t.Run("make lint", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join(root, "Makefile"))
		if err != nil {
			t.Fatalf("reading Makefile: %v", err)
		}
		recipe := matrixMakeRecipe(string(data), "lint")
		if recipe == "" {
			t.Fatal("the Makefile has no lint target recipe")
		}
		loops := matrixLoops(recipe)
		if len(loops) != 1 {
			t.Fatalf("the lint recipe has %d tag loops, want 1", len(loops))
		}
		matrixRequire(t, "make lint", loops[0], []string{"", "chromedp"})
	})

	// R5.2-R5.4 - the CI loops, identified by what they run rather than by
	// step name, so a renamed step is still found.
	t.Run("ci.yml", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
		if err != nil {
			t.Fatalf("reading ci.yml: %v", err)
		}
		var wf struct {
			Jobs map[string]struct {
				Steps []struct {
					Name string `yaml:"name"`
					Run  string `yaml:"run"`
				} `yaml:"steps"`
			} `yaml:"jobs"`
		}
		if err := yaml.Unmarshal(data, &wf); err != nil {
			t.Fatalf("parsing ci.yml: %v", err)
		}

		kinds := []struct {
			name   string
			marker string
			want   []string
		}{
			{"the golangci-lint loop (R5.2)", "golangci-lint run", []string{"", "chromedp"}},
			{"the govulncheck loop (R5.3)", "govulncheck", []string{"", "chromedp"}},
			{"the tagged build-and-vet loop (R5.4)", "go build -tags", []string{"chromedp"}},
		}
		seen := make([]int, len(kinds))
		for job, j := range wf.Jobs {
			for _, step := range j.Steps {
				for _, words := range matrixLoops(step.Run) {
					where := "ci.yml " + job + " / " + step.Name
					matched := false
					for i, k := range kinds {
						if strings.Contains(step.Run, k.marker) {
							matched = true
							seen[i]++
							matrixRequire(t, where+": "+k.name, words, k.want)
						}
					}
					if !matched {
						for _, w := range words {
							if strings.Contains(strings.ToLower(w), "playwright") {
								t.Errorf("%s: a tag loop still iterates %q", where, w)
							}
						}
					}
				}
			}
		}
		for i, k := range kinds {
			if seen[i] != 1 {
				t.Errorf("ci.yml has %d tag loops for %s, want exactly 1", seen[i], k.name)
			}
		}
	})
}
