package messages

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// requiredStrings are the UI strings the requirements name (R6.5, R9.2–R9.5).
var requiredStrings = []string{
	"Open", "Mark as read", "Check now", "Mark all as read",
	"Pause for 1 hour", "Pause until tomorrow", "Resume", "Quit",
}

// tooltipPhrase is the fixed part of the tray tooltip ("<n> unread notices",
// R8.1); the sni item must take it from the catalog too.
const tooltipPhrase = "unread notices"

// keyConstants returns the names of the package's constants of type Key,
// following Go's implicit repetition inside a const block (iota style).
func keyConstants(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, e.Name(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			inherited := false
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				switch {
				case vs.Type != nil:
					id, ok := vs.Type.(*ast.Ident)
					inherited = ok && id.Name == "Key"
				case len(vs.Values) > 0:
					inherited = false
				}
				if inherited {
					for _, n := range vs.Names {
						if n.Name != "_" {
							names = append(names, n.Name)
						}
					}
				}
			}
		}
	}
	return names
}

// TestCatalog_EveryKeyHasText is R9.6: one entry per Key constant, none empty,
// and no entry without a constant.
func TestCatalog_EveryKeyHasText(t *testing.T) {
	keys := keyConstants(t)
	if len(keys) == 0 {
		t.Fatal("the package declares no constants of type Key")
	}
	if len(catalog) != len(keys) {
		t.Errorf("catalog has %d entries for %d Key constants (%v)", len(catalog), len(keys), keys)
	}
	for k, v := range catalog {
		if strings.TrimSpace(v) == "" {
			t.Errorf("catalog entry %v is empty", k)
		}
	}
}

// TestCatalog_CarriesTheRequiredStrings: every menu and action label named by
// the requirements lives in the catalog, in English.
func TestCatalog_CarriesTheRequiredStrings(t *testing.T) {
	have := map[string]bool{}
	for _, v := range catalog {
		have[v] = true
	}
	for _, s := range requiredStrings {
		if !have[s] {
			t.Errorf("catalog lacks %q", s)
		}
	}
	tooltip := false
	for v := range have {
		if strings.Contains(v, tooltipPhrase) {
			tooltip = true
		}
	}
	if !tooltip {
		t.Errorf("no catalog entry carries the tray tooltip text (%q)", tooltipPhrase)
	}
}

// TestCatalog_NoUILiteralsOutsideTheCatalog is R9.6's other half: the tray's
// production code spells none of those labels itself — not the menu (sni),
// not the notification action labels (notify), not the tooltip text (sni) —
// anywhere under internal/tray, internal/desktop or cmd/bentoo-tray.
func TestCatalog_NoUILiteralsOutsideTheCatalog(t *testing.T) {
	forbidden := map[string]bool{}
	for _, s := range requiredStrings {
		forbidden[s] = true
	}
	root := filepath.Join("..", "..", "..")
	self, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, dir := range []string{"internal/tray", "internal/desktop", "cmd/bentoo-tray"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if d.IsDir() {
				if abs, _ := filepath.Abs(path); abs == self {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				if s, err := strconv.Unquote(lit.Value); err == nil && (forbidden[s] || strings.Contains(s, tooltipPhrase)) {
					t.Errorf("%s: UI string %q spelled outside the message catalog", fset.Position(lit.Pos()), s)
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", dir, err)
		}
	}
}
