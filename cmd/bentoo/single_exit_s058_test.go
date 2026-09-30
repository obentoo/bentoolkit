package main

// Authored for story 058, sub-task 7.1 — R1.1, R1.4.
//
// Two whole-package sweeps. The first walks the tree newRootCmd builds and
// requires the exact set of 29 runnable commands (27 at 6be73ec, plus
// notice new and notice revise from main at e051559), each on RunE; the second
// parses every production file of this package and requires that nothing but
// func main ends the process (and that main does, exactly once). Both assert
// what they swept, so a sweep that finds nothing cannot pass.
//
// Red on arrival: all 27 commands use Run, and 157 osExit calls end the
// process from handlers and helpers.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

var s058AllRunnable = []string{
	"completion", "distfile fetch",
	"notice new", "notice revise",
	"overlay add", "overlay analyze", "overlay autoupdate", "overlay commit", "overlay compare",
	"overlay diff", "overlay init", "overlay log", "overlay manifest", "overlay prune",
	"overlay pull", "overlay push", "overlay rename", "overlay staged clean", "overlay status",
	"overlay validate",
	"snapshot apply", "snapshot hook", "snapshot list", "snapshot prune", "snapshot restore",
	"snapshot rollback", "snapshot run", "snapshot status",
	"version",
}

func TestS058EveryRunnableCommandUsesRunE(t *testing.T) {
	var found, bad []string
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Run != nil || c.RunE != nil {
			path := strings.TrimPrefix(c.CommandPath(), "bentoo ")
			found = append(found, path)
			if c.RunE == nil || c.Run != nil {
				bad = append(bad, fmt.Sprintf("%s (Run set: %t, RunE set: %t)", path, c.Run != nil, c.RunE != nil))
			}
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(newRootCmd())
	sort.Strings(found)

	if strings.Join(found, "\n") != strings.Join(s058AllRunnable, "\n") {
		t.Errorf("the tree's runnable commands are not the 29 the story covers:\n got  %q\n want %q", found, s058AllRunnable)
	}
	if len(bad) > 0 {
		t.Errorf("%d runnable command(s) are not RunE-only (R1.1):\n  %s", len(bad), strings.Join(bad, "\n  "))
	}
}

func TestS058OnlyMainEndsTheProcess(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	swept, mainExits := 0, 0
	var offenders []string
	report := func(n ast.Node, what string) {
		offenders = append(offenders, fmt.Sprintf("%s: %s", fset.Position(n.Pos()), what))
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		swept++
		for _, decl := range file.Decls {
			fn, _ := decl.(*ast.FuncDecl)
			owner := ""
			if fn != nil && fn.Recv == nil {
				owner = fn.Name.Name
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.SelectorExpr:
					pkg, ok := x.X.(*ast.Ident)
					if !ok {
						return true
					}
					switch sel := pkg.Name + "." + x.Sel.Name; sel {
					case "os.Exit":
						if owner != "exitProcess" {
							report(x, sel+" outside func exitProcess")
						}
					case "syscall.Exit", "log.Fatal", "log.Fatalf", "log.Fatalln":
						report(x, sel)
					}
				case *ast.Ident:
					switch x.Name {
					case "osExit":
						report(x, "osExit")
					case "exitProcess":
						switch {
						case fn != nil && fn.Name == x:
						case owner == "main":
							mainExits++
						default:
							report(x, "exitProcess outside func main")
						}
					}
				}
				return true
			})
		}
	}
	if swept < 30 {
		t.Fatalf("swept %d production files; the package has far more, so the sweep is not looking where it should", swept)
	}
	if mainExits != 1 {
		t.Errorf("func main names exitProcess %d time(s), want exactly 1: main is where the process ends (R1.4)", mainExits)
	}
	if len(offenders) > 0 {
		t.Errorf("%d place(s) other than func main end the process or can (R1.4):\n  %s", len(offenders), strings.Join(offenders, "\n  "))
	}
}
