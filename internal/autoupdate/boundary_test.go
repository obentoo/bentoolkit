package autoupdate

// boundary_test.go pins the package graph that story 061 builds out of the old
// god package (design D11). It is the single guard for that graph: there is no
// depguard rule behind it (decided in Clarify).
//
// What it governs, and only that: the root package, its seven sub-packages
// (ebuilds, statefile, registry, fetch, parse, llm, fixer), the pre-existing
// internal/autoupdate/validate and the new internal/gentoo/repo. Every other
// package in the module is left alone (R2.8). A package that is not in
// packageGraphRules is only ever walked THROUGH, as an intermediate between a
// governed package and what it reaches; it never gets a rule of its own.
//
// How it sees edges: one `go list -e -deps -test=false -json` call over the
// governed packages returns every package in their closure together with its
// direct imports, and the rules are evaluated over the transitive closure
// (R2.4). The evaluation is a pure function, graphViolations, so its hostile
// cases are exercised on synthetic graphs without running go list at all
// (TestPackageGraphRulesOnSyntheticGraphs).
//
// It needs no network (R2.5): the go child runs with GOPROXY=off, so a run that
// would need a download fails loudly instead of quietly reaching out. It skips,
// naming the command, when go is not on PATH (R2.6).

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// graphModule is the module path. A path belongs to the module only when it is
// this string exactly or this string followed by "/": a sibling module whose
// path merely starts with the same bytes is third-party.
const graphModule = "github.com/obentoo/bentoolkit"

// graphRule is one row of the allowed-edge table. Patterns are module-relative;
// "x/..." matches x and every package below it, and "..." matches everything.
type graphRule struct {
	pkg   string   // the governed package, module-relative
	allow []string // module packages it may reach, directly or transitively
	deny  []string // module packages it may never reach, even when allow admits them
}

// packageGraphRules is design D11's allowed-edge table. Stdlib and third-party
// imports are not constrained. Adding an edge means editing this table in the
// same change that adds the import (R2.7), and every failure message says so.
var packageGraphRules = []graphRule{
	{pkg: "internal/autoupdate/ebuilds", allow: []string{"internal/common/ebuild", "internal/common/logging"}},
	{pkg: "internal/autoupdate/statefile", allow: []string{"internal/common/filelock"}},
	{pkg: "internal/autoupdate/registry", allow: []string{
		"internal/autoupdate/fetch", "internal/autoupdate/ebuilds", "internal/autoupdate/statefile", "internal/common/...",
	}},
	{pkg: "internal/autoupdate/llm", allow: []string{"internal/autoupdate/ebuilds", "internal/common/..."}},
	{pkg: "internal/autoupdate/fetch", allow: []string{"internal/autoupdate/statefile", "internal/common/..."}},
	{pkg: "internal/autoupdate/parse", allow: []string{
		"internal/autoupdate/registry", "internal/autoupdate/ebuilds", "internal/autoupdate/fetch",
		"internal/autoupdate/statefile", "internal/common/...",
	}},
	{pkg: "internal/autoupdate/fixer", allow: []string{
		"internal/autoupdate/llm", "internal/autoupdate/registry", "internal/autoupdate/ebuilds",
		"internal/autoupdate/fetch", "internal/autoupdate/statefile",
		"internal/autoupdate/validate", "internal/common/...", "internal/gentoo/repo",
	}},
	{pkg: "internal/autoupdate/validate", allow: []string{"internal/gentoo/repo", "internal/common/..."}},
	{pkg: "internal/gentoo/repo", allow: []string{"internal/common/ebuild"}},
	{pkg: "internal/autoupdate", allow: []string{"..."}, deny: []string{"cmd/..."}},
}

const (
	graphCorePkg = "internal/autoupdate"

	// graphTableNote is R2.7's sentence. It travels with every failure.
	graphTableNote = "If this edge is intended, add it to that package's row of packageGraphRules " +
		"(internal/autoupdate/boundary_test.go) in the same change that adds the import, and say why in the commit."

	graphCoreRemedy = "a sub-package must not import the internal/autoupdate core: the core imports every " +
		"sub-package, so the edge back is a cycle waiting to happen. Move the shared code down into the " +
		"sub-package (or a lower layer) and let the core call it, or pass the value in as a parameter."

	graphLayerRemedy = "the autoupdate packages are layered L0 ebuilds, statefile, common/httpx, gentoo/repo; " +
		"L1 fetch, llm; L2 registry; L3 parse; L4 fixer; the core above them (design D11 as amended by story 061's run). " +
		"A package imports only the layers below it. " +
		"Move the code that needs this import into a package allowed to reach it, or pass what it needs in as a value."

	graphCmdRemedy = "cmd/bentoo depends on internal packages, never the reverse. Move the shared code into " +
		"an internal package that both can import."

	graphMissingRemedy = "the allowed-edge table governs a package that go list cannot load. Create it, or, " +
		"if it was renamed or removed on purpose, edit its row of packageGraphRules in the same change."
)

// graphNode is one package as go list reports it.
type graphNode struct {
	ImportPath string
	Imports    []string
	Error      *struct{ Err string }
}

// graphViolation is one broken rule, with what its message needs (R2.11).
type graphViolation struct {
	importer  string   // the governed package, module-relative
	forbidden string   // the module package it must not reach, module-relative; empty when the importer is missing
	via       []string // intermediates between importer and forbidden, module-relative, in order
	allowed   []string // the importer's row, for the message
	remedy    string
	loadErr   string // set when the governed package itself could not be loaded
}

func (v graphViolation) message() string {
	if v.forbidden == "" {
		return fmt.Sprintf("%s is governed by the package-graph table but could not be loaded: %s\nRemedy: %s",
			v.importer, v.loadErr, graphMissingRemedy)
	}
	through := ""
	if len(v.via) > 0 {
		through = " (through " + strings.Join(v.via, " -> ") + ")"
	}
	return fmt.Sprintf("%s imports %s%s, which its row of the allowed-edge table does not list (allowed: %s).\nRemedy: %s\n%s",
		v.importer, v.forbidden, through, strings.Join(v.allowed, ", "), v.remedy, graphTableNote)
}

// graphRel returns the module-relative form of an import path, and whether the
// path is inside the module at all.
func graphRel(importPath string) (string, bool) {
	if importPath == graphModule {
		return ".", true
	}
	rel, ok := strings.CutPrefix(importPath, graphModule+"/")
	return rel, ok
}

// matchGraphPattern reports whether a module-relative package matches a table
// pattern. "x/..." is x itself or a package below x; any other pattern is an
// exact package, so "internal/common/ebuild" never admits
// "internal/common/ebuildx" and "internal/autoupdate" never admits a
// sub-package.
func matchGraphPattern(pattern, rel string) bool {
	if pattern == "..." {
		return true
	}
	if base, ok := strings.CutSuffix(pattern, "/..."); ok {
		return rel == base || strings.HasPrefix(rel, base+"/")
	}
	return rel == pattern
}

// graphVerdict decides one edge for one rule. It returns the remedy when the
// edge is forbidden, or "" when it is allowed.
func graphVerdict(rule graphRule, rel string) string {
	if rule.pkg != graphCorePkg && rel == graphCorePkg {
		return graphCoreRemedy // R2.2, with its own remedy
	}
	for _, d := range rule.deny {
		if matchGraphPattern(d, rel) {
			return graphCmdRemedy
		}
	}
	for _, a := range rule.allow {
		if matchGraphPattern(a, rel) {
			return ""
		}
	}
	return graphLayerRemedy
}

// graphViolations evaluates every rule over the transitive closure of its
// package. It walks only module-internal edges, never expands a package it has
// already found forbidden (fixing that edge removes everything behind it), and
// applies no rule to a package that has no row (R2.8).
func graphViolations(nodes map[string]graphNode, rules []graphRule) []graphViolation {
	var out []graphViolation
	for _, rule := range rules {
		start := graphModule + "/" + rule.pkg
		node, ok := nodes[start]
		if !ok || node.Error != nil {
			msg := "go list did not report it"
			if ok {
				msg = node.Error.Err
			}
			out = append(out, graphViolation{importer: rule.pkg, loadErr: msg})
			continue
		}
		parent := map[string]string{start: ""}
		queue := []string{start}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			imports := slices.Clone(nodes[cur].Imports)
			slices.Sort(imports)
			for _, imp := range imports {
				rel, inModule := graphRel(imp)
				if !inModule {
					continue
				}
				if _, seen := parent[imp]; seen {
					continue
				}
				parent[imp] = cur
				if remedy := graphVerdict(rule, rel); remedy != "" {
					out = append(out, graphViolation{
						importer:  rule.pkg,
						forbidden: rel,
						via:       graphChain(parent, start, cur),
						allowed:   rule.allow,
						remedy:    remedy,
					})
					continue
				}
				queue = append(queue, imp)
			}
		}
	}
	return out
}

// graphChain returns the module-relative intermediates from start (exclusive)
// to last (inclusive), in walking order. It is empty for a direct import.
func graphChain(parent map[string]string, start, last string) []string {
	var chain []string
	for cur := last; cur != start && cur != ""; cur = parent[cur] {
		rel, _ := graphRel(cur)
		chain = append(chain, rel)
	}
	slices.Reverse(chain)
	return chain
}

// parseGoListJSON decodes the concatenated JSON objects go list -json prints.
func parseGoListJSON(r io.Reader) (map[string]graphNode, error) {
	nodes := map[string]graphNode{}
	dec := json.NewDecoder(r)
	for {
		var n graphNode
		err := dec.Decode(&n)
		if errors.Is(err, io.EOF) {
			return nodes, nil
		}
		if err != nil {
			return nil, fmt.Errorf("decoding go list output: %w", err)
		}
		nodes[n.ImportPath] = n
	}
}

// goCommandFor resolves the go command. It returns a skip reason that names the
// missing command when lookPath cannot find it (R2.6), and "" otherwise.
func goCommandFor(lookPath func(string) (string, error)) (path, skipReason string) {
	p, err := lookPath("go")
	if err != nil {
		return "", fmt.Sprintf("skipping: the \"go\" command is not on PATH (%v); this test runs \"go list -deps\" and cannot check the package graph without it", err)
	}
	return p, ""
}

// requireGoCommand skips t when go is not on PATH, and returns its path.
func requireGoCommand(t *testing.T, lookPath func(string) (string, error)) string {
	t.Helper()
	p, reason := goCommandFor(lookPath)
	if reason != "" {
		t.Skip(reason)
	}
	return p
}

// runGoOffline runs the go command with downloads disabled (R2.5) and fails t,
// naming the command, when it does not succeed.
func runGoOffline(t *testing.T, goBin, dir string, args ...string) []byte {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), goBin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOPROXY=off")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("running %s %s in %q: %v\n%s", goBin, strings.Join(args, " "), dir, err, stderr.String())
	}
	return out
}

// graphModuleRoot resolves the module root through go env GOMOD.
func graphModuleRoot(t *testing.T, goBin string) string {
	t.Helper()
	gomod := strings.TrimSpace(string(runGoOffline(t, goBin, ".", "env", "GOMOD")))
	if gomod == "" || gomod == os.DevNull {
		t.Fatalf("go env GOMOD printed %q: the test is not running inside the module", gomod)
	}
	return filepath.Dir(gomod)
}

// TestPackageGraphAllowedEdges is the guard itself: design D11's table, applied
// to the real tree (R2.2, R2.3, R2.4, R2.5, R2.7, R2.11).
func TestPackageGraphAllowedEdges(t *testing.T) {
	goBin := requireGoCommand(t, exec.LookPath)
	root := graphModuleRoot(t, goBin)

	args := []string{"list", "-e", "-deps", "-test=false", "-json=ImportPath,Error,Imports"}
	for _, rule := range packageGraphRules {
		args = append(args, graphModule+"/"+rule.pkg)
	}
	nodes, err := parseGoListJSON(bytes.NewReader(runGoOffline(t, goBin, root, args...)))
	if err != nil {
		t.Fatalf("go %s: %v", strings.Join(args, " "), err)
	}
	if len(nodes) < len(packageGraphRules) {
		t.Fatalf("go list reported %d packages for %d governed ones; the guard would pass without inspecting the graph",
			len(nodes), len(packageGraphRules))
	}
	for _, v := range graphViolations(nodes, packageGraphRules) {
		t.Error(v.message())
	}
}

// TestPackageGraphRulesOnSyntheticGraphs exercises graphViolations on graphs
// built by hand, hostile halves first. These are checks of the guard's own
// logic: they hold on any tree, and what shows they can fail is mutating the
// guard (see the story's red-evidence.yaml).
func TestPackageGraphRulesOnSyntheticGraphs(t *testing.T) {
	m := func(rel string) string { return graphModule + "/" + rel }
	// base returns a graph in which every governed package exists and imports
	// nothing, so each case adds only the edges it is about.
	base := func() map[string]graphNode {
		g := map[string]graphNode{}
		for _, r := range packageGraphRules {
			g[m(r.pkg)] = graphNode{ImportPath: m(r.pkg)}
		}
		return g
	}
	edge := func(g map[string]graphNode, from string, to ...string) {
		n := g[from]
		n.ImportPath = from
		n.Imports = append(n.Imports, to...)
		g[from] = n
		for _, t := range to {
			if _, ok := g[t]; !ok {
				g[t] = graphNode{ImportPath: t}
			}
		}
	}
	type want struct{ importer, forbidden, via string }

	cases := []struct {
		name  string
		build func(g map[string]graphNode)
		want  []want
	}{
		// Hostile: identity of the core must not collapse onto its sub-packages.
		// A rule matching "internal/autoupdate" by prefix would call this a core
		// import; it is an allowed edge.
		{"fixer importing validate is not an import of the core", func(g map[string]graphNode) {
			edge(g, m("internal/autoupdate/fixer"), m("internal/autoupdate/validate"))
		}, nil},
		// Hostile: an exact table entry must not collapse onto a longer name.
		{"gentoo/repo may import common/ebuild but not common/ebuildx", func(g map[string]graphNode) {
			edge(g, m("internal/gentoo/repo"), m("internal/common/ebuildx"))
		}, []want{{"internal/gentoo/repo", "internal/common/ebuildx", ""}}},
		{"common/... does not admit a sibling directory named commonx", func(g map[string]graphNode) {
			edge(g, m("internal/autoupdate/registry"), m("internal/commonx/util"))
		}, []want{{"internal/autoupdate/registry", "internal/commonx/util", ""}}},
		// Hostile (third element): a module whose path merely extends ours is
		// third-party, and must not be read as internal/overlay.
		{"a sibling module sharing the path prefix is third-party", func(g map[string]graphNode) {
			edge(g, m("internal/autoupdate/ebuilds"), "github.com/obentoo/bentoolkit-plugins/internal/overlay")
		}, nil},
		// Hostile: the same package spelled two ways. The table is module-relative,
		// go list prints full paths; they must be recognised as one package.
		{"a full import path matches its module-relative row", func(g map[string]graphNode) {
			edge(g, m("internal/autoupdate/ebuilds"), m("internal/common/ebuild"), m("internal/common/logging"))
		}, nil},
		// R2.2, direct and transitive.
		{"a sub-package importing the core fails", func(g map[string]graphNode) {
			edge(g, m("internal/autoupdate/registry"), m("internal/autoupdate"))
		}, []want{{"internal/autoupdate/registry", "internal/autoupdate", ""}}},
		{"a sub-package reaching the core through another fails, naming the intermediate", func(g map[string]graphNode) {
			edge(g, m("internal/autoupdate/parse"), m("internal/autoupdate/registry"))
			edge(g, m("internal/autoupdate/registry"), m("internal/autoupdate"))
		}, []want{
			{"internal/autoupdate/registry", "internal/autoupdate", ""},
			{"internal/autoupdate/parse", "internal/autoupdate", "internal/autoupdate/registry"},
		}},
		// R2.3 and R2.4: a forbidden edge reached only through an allowed one.
		{"fixer reaching internal/overlay through validate fails for both", func(g map[string]graphNode) {
			edge(g, m("internal/autoupdate/fixer"), m("internal/autoupdate/validate"))
			edge(g, m("internal/autoupdate/validate"), m("internal/overlay"))
		}, []want{
			{"internal/autoupdate/fixer", "internal/overlay", "internal/autoupdate/validate"},
			{"internal/autoupdate/validate", "internal/overlay", ""},
		}},
		{"a forbidden edge two hops down names the whole chain", func(g map[string]graphNode) {
			// story 061's run reversed the registry/fetch edge: registry -> fetch
			edge(g, m("internal/autoupdate/registry"), m("internal/autoupdate/fetch"))
			edge(g, m("internal/autoupdate/fetch"), m("internal/common/secrets"))
			edge(g, m("internal/common/secrets"), m("internal/snapshot"))
		}, []want{
			{"internal/autoupdate/fetch", "internal/snapshot", "internal/common/secrets"},
			{"internal/autoupdate/registry", "internal/snapshot", "internal/autoupdate/fetch -> internal/common/secrets"},
		}},
		{"statefile may reach common/filelock only, not the rest of common", func(g map[string]graphNode) {
			edge(g, m("internal/autoupdate/statefile"), m("internal/common/filelock"))
			edge(g, m("internal/common/filelock"), m("internal/common/logging"))
		}, []want{{"internal/autoupdate/statefile", "internal/common/logging", "internal/common/filelock"}}},
		{"the core may import anything internal but not cmd", func(g map[string]graphNode) {
			edge(g, m("internal/autoupdate"), m("internal/overlay"), m("internal/autoupdate/fixer"), m("cmd/bentoo"))
		}, []want{{"internal/autoupdate", "cmd/bentoo", ""}}},
		// R2.8: no rule for a package the story did not create or move.
		{"an ungoverned package is never judged", func(g map[string]graphNode) {
			edge(g, m("internal/overlay"), m("internal/autoupdate"), m("cmd/bentoo"))
			edge(g, m("internal/realign"), m("internal/autoupdate"), m("internal/overlay"))
		}, nil},
		{"stdlib and third-party imports are not constrained", func(g map[string]graphNode) {
			edge(g, m("internal/gentoo/repo"), "os", "net/http/httputil", "github.com/BurntSushi/toml")
		}, nil},
		{"a governed package that does not exist is reported, not skipped", func(g map[string]graphNode) {
			delete(g, m("internal/autoupdate/llm"))
			g[m("internal/autoupdate/ebuilds")] = graphNode{
				ImportPath: m("internal/autoupdate/ebuilds"),
				Error:      &struct{ Err string }{"directory not found"},
			}
		}, []want{{"internal/autoupdate/ebuilds", "", ""}, {"internal/autoupdate/llm", "", ""}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := base()
			tc.build(g)
			got := graphViolations(g, packageGraphRules)
			var gotKeys, wantKeys []string
			for _, v := range got {
				gotKeys = append(gotKeys, v.importer+" | "+v.forbidden+" | "+strings.Join(v.via, " -> "))
			}
			for _, w := range tc.want {
				wantKeys = append(wantKeys, w.importer+" | "+w.forbidden+" | "+w.via)
			}
			slices.Sort(gotKeys)
			slices.Sort(wantKeys)
			if !slices.Equal(gotKeys, wantKeys) {
				t.Fatalf("violations:\n got  %q\n want %q", gotKeys, wantKeys)
			}
		})
	}

	// R2.11 and R2.7: what a failure message carries.
	t.Run("the message names importer, forbidden import, remedy and the table rule", func(t *testing.T) {
		g := base()
		edge(g, m("internal/autoupdate/fixer"), m("internal/autoupdate/validate"))
		edge(g, m("internal/autoupdate/validate"), m("internal/overlay"))
		var msg string
		for _, v := range graphViolations(g, packageGraphRules) {
			if v.importer == "internal/autoupdate/fixer" {
				msg = v.message()
			}
		}
		for _, part := range []string{
			"internal/autoupdate/fixer imports internal/overlay",
			"through internal/autoupdate/validate",
			"Remedy: " + graphLayerRemedy,
			"in the same change",
			"packageGraphRules",
		} {
			if !strings.Contains(msg, part) {
				t.Errorf("message lacks %q:\n%s", part, msg)
			}
		}
	})
	t.Run("a core import carries the core remedy, not the layering one", func(t *testing.T) {
		g := base()
		edge(g, m("internal/autoupdate/llm"), m("internal/autoupdate"))
		vs := graphViolations(g, packageGraphRules)
		if len(vs) != 1 || vs[0].remedy != graphCoreRemedy {
			t.Fatalf("want one violation with graphCoreRemedy, got %+v", vs)
		}
		if !strings.Contains(vs[0].message(), "in the same change") {
			t.Errorf("message lacks the table rule:\n%s", vs[0].message())
		}
	})
	t.Run("a missing package names itself and the table", func(t *testing.T) {
		g := base()
		delete(g, m("internal/autoupdate/parse"))
		vs := graphViolations(g, packageGraphRules)
		if len(vs) != 1 || !strings.Contains(vs[0].message(), "internal/autoupdate/parse") ||
			!strings.Contains(vs[0].message(), "packageGraphRules") {
			t.Fatalf("want one message naming internal/autoupdate/parse and the table, got %+v", vs)
		}
	})
	t.Run("go list JSON with a load error decodes into the node", func(t *testing.T) {
		in := `{"ImportPath":"` + m("internal/autoupdate/fixer") + `","Error":{"Err":"directory not found"}}
{"ImportPath":"` + m("internal/autoupdate") + `","Imports":["fmt","` + m("internal/autoupdate/fixer") + `"]}`
		nodes, err := parseGoListJSON(strings.NewReader(in))
		if err != nil {
			t.Fatal(err)
		}
		if n := nodes[m("internal/autoupdate/fixer")]; n.Error == nil || n.Error.Err != "directory not found" {
			t.Errorf("load error lost: %+v", n)
		}
		if n := nodes[m("internal/autoupdate")]; len(n.Imports) != 2 {
			t.Errorf("imports lost: %+v", n)
		}
	})
}

// TestPackageGraphSkipsWithoutGo pins R2.6: no go on PATH is a skip that names
// the command, never a failure and never a silent pass.
func TestPackageGraphSkipsWithoutGo(t *testing.T) {
	var asked []string
	missing := func(name string) (string, error) {
		asked = append(asked, name)
		return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
	}

	// Hostile half first: a go that IS on PATH must not produce a skip.
	path, reason := goCommandFor(func(string) (string, error) { return "/opt/go/bin/go", nil })
	if reason != "" || path != "/opt/go/bin/go" {
		t.Errorf("go found on PATH: got path %q, skip reason %q; want the path and no skip", path, reason)
	}

	_, reason = goCommandFor(missing)
	if !strings.Contains(reason, `"go"`) {
		t.Errorf("skip reason %q does not name the missing \"go\" command", reason)
	}
	if !slices.Equal(asked, []string{"go"}) {
		t.Errorf("looked up %q; want exactly [\"go\"]", asked)
	}

	var sub *testing.T
	t.Run("guard", func(st *testing.T) {
		sub = st
		requireGoCommand(st, missing)
		st.Error("requireGoCommand returned without skipping although go is missing")
	})
	if sub == nil || !sub.Skipped() {
		t.Errorf("the guard did not skip when go was missing")
	}
}

// storySubPackages are the seven packages R2.1 names, relative to this package.
var storySubPackages = []string{"ebuilds", "statefile", "registry", "fetch", "parse", "llm", "fixer"}

// packageDocFiles reads every non-test .go file in dir, build tags ignored, and
// returns the package names declared and the files whose package clause carries
// a doc comment. A comment made only of directives (//go:build) is not a doc
// comment.
func packageDocFiles(dir string) (names, docFiles []string, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, parser.PackageClauseOnly|parser.ParseComments)
		if err != nil {
			return nil, nil, err
		}
		if !slices.Contains(names, f.Name.Name) {
			names = append(names, f.Name.Name)
		}
		if f.Doc != nil && strings.TrimSpace(f.Doc.Text()) != "" {
			docFiles = append(docFiles, e.Name())
		}
	}
	slices.Sort(docFiles)
	return names, docFiles, nil
}

// packageDocProblem returns what is wrong with a story package's doc comments
// against R2.1 and R2.10, or "" when nothing is. label names the package in the
// message.
func packageDocProblem(dir, label, want string) string {
	names, docFiles, err := packageDocFiles(dir)
	if err != nil {
		return fmt.Sprintf("%s: cannot read its directory: %v (R2.1 requires the package to exist)", label, err)
	}
	if len(names) == 0 {
		return fmt.Sprintf("%s holds no non-test .go file (R2.1 requires package %s)", label, want)
	}
	if !slices.Equal(names, []string{want}) {
		return fmt.Sprintf("%s declares package %q; want only %q (R2.1)", label, names, want)
	}
	if !slices.Equal(docFiles, []string{"doc.go"}) {
		return fmt.Sprintf("%s carries package doc comments in %q; R2.10 allows exactly one, in doc.go. "+
			"Remedy: keep the package comment in doc.go, and delete the comment above `package %s` in every other file "+
			"or separate it from the package clause with a blank line.", label, docFiles, want)
	}
	return ""
}

// TestStoryPackagesHaveOneDocComment pins R2.1 and R2.10: each of the seven
// packages exists under its own name, with exactly one package doc comment, in
// its doc.go.
func TestStoryPackagesHaveOneDocComment(t *testing.T) {
	for _, name := range storySubPackages {
		if problem := packageDocProblem(name, "internal/autoupdate/"+name, name); problem != "" {
			t.Error(problem)
		}
	}
}

// TestStoryPackageDocRulesOnSyntheticSources exercises packageDocProblem and
// docBeginsWithName on hand-made sources, hostile halves first. Like the
// synthetic graph test, these hold on any tree; mutation is what shows they
// can fail.
func TestStoryPackageDocRulesOnSyntheticSources(t *testing.T) {
	write := func(t *testing.T, files map[string]string) string {
		t.Helper()
		dir := t.TempDir()
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	cases := []struct {
		name    string
		pkg     string
		files   map[string]string
		wantBad bool
	}{
		// Hostile, wrongly collapsing: a second package comment (the stray one
		// header_allowlist.go carried) must not pass as "one".
		{"two package comments fail", "fetch", map[string]string{
			"doc.go":   "// Package fetch fetches.\npackage fetch\n",
			"cache.go": "// Package fetch provides cache management.\npackage fetch\n",
		}, true},
		{"a file comment glued to the package clause counts as a package comment", "parse", map[string]string{
			"doc.go":     "// Package parse parses.\npackage parse\n",
			"chromed.go": "//go:build chromedp\n\n// This file is the headless-browser backend.\npackage parse\n",
		}, true},
		{"the one comment in the wrong file fails", "llm", map[string]string{
			"doc.go":    "package llm\n",
			"client.go": "// Package llm talks to models.\npackage llm\n",
		}, true},
		{"no package comment fails", "llm", map[string]string{"doc.go": "package llm\n"}, true},
		{"a second package name fails", "fixer", map[string]string{
			"doc.go": "// Package fixer fixes.\npackage fixer\n",
			"x.go":   "package autoupdate\n",
		}, true},
		{"an empty directory fails", "llm", map[string]string{"README": "x"}, true},
		// Hostile, wrongly splitting: a build constraint, or a file comment set
		// apart by a blank line, is not a second package comment.
		{"a build constraint is not a package comment", "parse", map[string]string{
			"doc.go":  "// Package parse parses.\npackage parse\n",
			"stub.go": "//go:build !chromedp\n\npackage parse\n",
		}, false},
		{"a file comment separated by a blank line is not a package comment", "fetch", map[string]string{
			"doc.go":  "// Package fetch fetches.\npackage fetch\n",
			"auth.go": "// auth.go holds authenticated fetching.\n\npackage fetch\n",
		}, false},
		{"test files are not the package source", "parse", map[string]string{
			"doc.go":        "// Package parse parses.\npackage parse\n",
			"chrom_test.go": "// Integration test for the backend.\npackage parse\n",
		}, false},
		{"one package comment in doc.go passes", "statefile", map[string]string{
			"doc.go":  "// Package statefile saves state.\npackage statefile\n",
			"save.go": "package statefile\n",
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := write(t, tc.files)
			problem := packageDocProblem(dir, tc.pkg, tc.pkg)
			if tc.wantBad && problem == "" {
				t.Errorf("want a problem, got none")
			}
			if !tc.wantBad && problem != "" {
				t.Errorf("want no problem, got %q", problem)
			}
		})
	}

	nameCases := []struct {
		doc, name string
		want      bool
	}{
		// Hostile, wrongly collapsing: the old unexported name left on a newly
		// exported identifier, and a longer word that starts with the name.
		{"findEbuilds lists the ebuilds in a package directory.\n", "FindEbuilds", false},
		{"FindEbuildsIn lists the ebuilds.\n", "FindEbuilds", false},
		{"Lists the ebuilds. FindEbuilds is exported for registry.\n", "FindEbuilds", false},
		{"", "FindEbuilds", false},
		// Hostile, wrongly splitting: punctuation after the name is still the name.
		{"FindEbuilds, given a package directory, lists its ebuilds.\n", "FindEbuilds", true},
		{"FindEbuilds lists the ebuilds in a package directory.\n", "FindEbuilds", true},
	}
	for _, nc := range nameCases {
		if got := docBeginsWithName(nc.doc, nc.name); got != nc.want {
			t.Errorf("docBeginsWithName(%q, %q) = %v, want %v", nc.doc, nc.name, got, nc.want)
		}
	}
}

// docBeginsWithName reports whether a doc comment's first word is name (R2.9),
// trailing punctuation aside.
func docBeginsWithName(doc, name string) bool {
	fields := strings.Fields(doc)
	if len(fields) == 0 {
		return false
	}
	return strings.TrimRight(fields[0], ",.:;") == name
}

// crossingIdentifiers are the functions design D1, D2, D3, D5 and D6 name as
// becoming exported because they now cross a package boundary. A method is
// written Type.Method. Exported types, constants and fields are not listed;
// R2.9 covers them by review (task 9.2). A recorded deviation that changes
// which identifiers cross edits only the matching entry here.
var crossingIdentifiers = map[string][]string{
	"ebuilds": {
		"FindEbuilds", "SelectCurrentEbuild", "SplitPkgAtom", "SplitPkgSlot", "SplitPkgLabel",
		"ParsePkgAtom", "PkgDirFor", "NewSeriesMatcher", "ExtractVersionFromFilename",
	},
	"statefile": {"WithStateLock", "SnapshotState", "MergeState"},
	"registry":  {"PackageConfig.UpstreamURLs"},
	"fetch":     {"URLTemplateFault", "ValidateMetaFetch", "ParseAuthFetchSpec"},
	"parse":     {"StripVersionPrefix"},
	"llm": {
		"ChildEnv", "ResolveBare", "RefusedToolLabels",
		"AgentPermissionArgs", "UpstreamHosts", "UpstreamURLsIn", "ClassifyClaudeFailure",
	},
}

// funcDocs returns the doc text of every top-level function and method in the
// non-test files of dir, keyed by name or Type.Method.
func funcDocs(dir string) (map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	docs := map[string]string{}
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			key := fn.Name.Name
			if fn.Recv != nil && len(fn.Recv.List) == 1 {
				typ := fn.Recv.List[0].Type
				if star, ok := typ.(*ast.StarExpr); ok {
					typ = star.X
				}
				if id, ok := typ.(*ast.Ident); ok {
					key = id.Name + "." + key
				}
			}
			docs[key] = fn.Doc.Text()
		}
	}
	return docs, nil
}

// TestCrossingIdentifiersHaveDocComments pins R2.9 for the identifiers the
// design names: each is exported in its new package, with a doc comment that
// begins with its exported name.
func TestCrossingIdentifiersHaveDocComments(t *testing.T) {
	for _, pkg := range slices.Sorted(maps.Keys(crossingIdentifiers)) {
		docs, err := funcDocs(pkg)
		if err != nil {
			t.Errorf("internal/autoupdate/%s: cannot read its directory: %v (R2.1 requires the package to exist)", pkg, err)
			continue
		}
		for _, ident := range crossingIdentifiers[pkg] {
			doc, ok := docs[ident]
			switch {
			case !ok:
				t.Errorf("internal/autoupdate/%s declares no function %s; design names it as exported there", pkg, ident)
			case !docBeginsWithName(doc, ident[strings.LastIndex(ident, ".")+1:]):
				t.Errorf("internal/autoupdate/%s.%s: doc comment %q does not begin with its name (R2.9)", pkg, ident, doc)
			}
		}
	}
}

// TestValidateDoesNotDependOnOverlay pins R3.1: internal/autoupdate/validate's
// dependency list no longer includes internal/overlay (design D8).
func TestValidateDoesNotDependOnOverlay(t *testing.T) {
	goBin := requireGoCommand(t, exec.LookPath)
	root := graphModuleRoot(t, goBin)
	args := []string{"list", "-e", "-deps", "-test=false", "-json=ImportPath,Error,Imports", graphModule + "/internal/autoupdate/validate"}
	nodes, err := parseGoListJSON(bytes.NewReader(runGoOffline(t, goBin, root, args...)))
	if err != nil {
		t.Fatalf("go %s: %v", strings.Join(args, " "), err)
	}
	if n, ok := nodes[graphModule+"/internal/autoupdate/validate"]; !ok || n.Error != nil {
		t.Fatalf("go list could not load internal/autoupdate/validate: %+v", n)
	}
	for _, path := range slices.Sorted(maps.Keys(nodes)) {
		for _, imp := range nodes[path].Imports {
			rel, ok := graphRel(imp)
			if ok && matchGraphPattern("internal/overlay/...", rel) {
				from, _ := graphRel(path)
				t.Errorf("internal/autoupdate/validate depends on %s: %s imports it (R3.1). "+
					"Remedy: take ScanOverlay and ScanResult from internal/gentoo/repo (design D8).", rel, from)
			}
		}
	}
}

// listedPackage is one package as go list -json=ImportPath,Name reports it.
type listedPackage struct {
	ImportPath string
	Name       string
}

// httputilNamedPackages returns the module packages named httputil, by package
// clause or by directory (R4.1). The stdlib's net/http/httputil is not one of
// them.
func httputilNamedPackages(pkgs []listedPackage) []string {
	var out []string
	for _, p := range pkgs {
		if _, inModule := graphRel(p.ImportPath); !inModule {
			continue
		}
		if p.Name == "httputil" || filepath.Base(p.ImportPath) == "httputil" {
			out = append(out, fmt.Sprintf("%s (package %s)", p.ImportPath, p.Name))
		}
	}
	return out
}

// TestNoPackageNamedHttputil pins R4.1: no package in the module shadows the
// stdlib's net/http/httputil.
func TestNoPackageNamedHttputil(t *testing.T) {
	goBin := requireGoCommand(t, exec.LookPath)
	root := graphModuleRoot(t, goBin)
	out := runGoOffline(t, goBin, root, "list", "-e", "-json=ImportPath,Name", "./...")
	var pkgs []listedPackage
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var p listedPackage
		err := dec.Decode(&p)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("decoding go list ./... output: %v", err)
		}
		pkgs = append(pkgs, p)
	}
	if len(pkgs) == 0 {
		t.Fatal("go list ./... listed no package; the check would pass without inspecting anything")
	}
	for _, p := range httputilNamedPackages(pkgs) {
		t.Errorf("%s shadows the stdlib net/http/httputil (R4.1). Remedy: it is internal/common/httpx since story 061 (design D9).", p)
	}
}

// TestHttputilNameRuleOnSyntheticLists exercises httputilNamedPackages, hostile
// halves first.
func TestHttputilNameRuleOnSyntheticLists(t *testing.T) {
	m := graphModule + "/"
	cases := []struct {
		name string
		pkgs []listedPackage
		want int
	}{
		// Hostile: a renamed directory whose package clause was left behind.
		{"package httputil in a renamed directory", []listedPackage{{m + "internal/common/httpx", "httputil"}}, 1},
		// Hostile: a renamed package clause whose directory was left behind.
		{"directory httputil with a renamed clause", []listedPackage{{m + "internal/common/httputil", "httpx"}}, 1},
		// Hostile, wrongly flagging: the stdlib package and near-miss names.
		{"the stdlib net/http/httputil is not the module's", []listedPackage{{"net/http/httputil", "httputil"}}, 0},
		{"a sibling module is not the module", []listedPackage{{"github.com/obentoo/bentoolkit-x/httputil", "httputil"}}, 0},
		{"near-miss names pass", []listedPackage{
			{m + "internal/common/httputils", "httputils"},
			{m + "internal/common/httpx", "httpx"},
		}, 0},
	}
	for _, tc := range cases {
		if got := httputilNamedPackages(tc.pkgs); len(got) != tc.want {
			t.Errorf("%s: got %q, want %d entries", tc.name, got, tc.want)
		}
	}
}
