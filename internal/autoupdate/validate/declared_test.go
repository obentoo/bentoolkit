package validate

// Authored for story 031, sub-task 2.3 — R1, R1.1, R1.2, R1.3, R1.4, R4.3.
//
// Written from the contract: design.md fixes
// `OptionsFromArchive(ctx, archive) (Declared, error)` and D3 fixes the
// namespacing rule — the option file lives at the PROJECT ROOT, one per
// project, and what exists beyond it is a subproject with its own root under
// `subprojects/<name>/`, addressed as `<name>:<option>`.
//
// This file pins one name design.md left unnamed: the sentinel
// `ErrBuildSystemUndetermined`, which R4.3 turns into a SKIPPED outcome.
//
// buildTarGz comes from archive_test.go in this package.
//
// Red is DEFERRED to Run mode: the package does not exist yet.

import (
	"context"
	"errors"
	"testing"
)

func declaredNames(opts []Option) []string {
	out := make([]string, 0, len(opts))
	for _, o := range opts {
		out = append(out, o.Name)
	}
	return out
}

func TestOptionsFromArchive_RootMesonOptions(t *testing.T) {
	archive := buildTarGz(t, map[string]string{
		"proj-1.0/meson.build":   "project('proj')\n",
		"proj-1.0/meson.options": "option('aalib')\noption('libcaca')\n",
	})

	got, err := OptionsFromArchive(context.Background(), archive)
	if err != nil {
		t.Fatalf("OptionsFromArchive: %v", err)
	}

	if names := declaredNames(got.Root); !equalStrings(names, []string{"aalib", "libcaca"}) {
		t.Errorf("root options: got %v, want [aalib libcaca]", names)
	}
}

// TestOptionsFromArchive_FallsBackToMesonOptionsTxt covers the pre-Meson-1.1
// filename. Both names are valid and in the wild simultaneously — the very
// package this story exists for ships the newer one.
func TestOptionsFromArchive_FallsBackToMesonOptionsTxt(t *testing.T) {
	archive := buildTarGz(t, map[string]string{
		"proj-1.0/meson.build":       "project('proj')\n",
		"proj-1.0/meson_options.txt": "option('legacy')\n",
	})

	got, err := OptionsFromArchive(context.Background(), archive)
	if err != nil {
		t.Fatalf("OptionsFromArchive: %v", err)
	}

	if names := declaredNames(got.Root); !equalStrings(names, []string{"legacy"}) {
		t.Errorf("root options: got %v, want [legacy]", names)
	}
}

// TestOptionsFromArchive_PrefersMesonOptionsOverTxt pins the precedence when an
// archive carries both, which happens mid-migration upstream.
func TestOptionsFromArchive_PrefersMesonOptionsOverTxt(t *testing.T) {
	archive := buildTarGz(t, map[string]string{
		"proj-1.0/meson.build":       "project('proj')\n",
		"proj-1.0/meson.options":     "option('current')\n",
		"proj-1.0/meson_options.txt": "option('stale')\n",
	})

	got, err := OptionsFromArchive(context.Background(), archive)
	if err != nil {
		t.Fatalf("OptionsFromArchive: %v", err)
	}

	if names := declaredNames(got.Root); !equalStrings(names, []string{"current"}) {
		t.Errorf("root options: got %v, want only the new-style file's [current]", names)
	}
}

// TestOptionsFromArchive_SubprojectIsNamespaced is R1.3 and the false-positive
// guard: a subproject option filed under the root would be compared against
// declarations that never list it.
func TestOptionsFromArchive_SubprojectIsNamespaced(t *testing.T) {
	archive := buildTarGz(t, map[string]string{
		"proj-1.0/meson.build":                        "project('proj')\n",
		"proj-1.0/meson.options":                      "option('qt6')\n",
		"proj-1.0/subprojects/gst-base/meson.options": "option('gl')\n",
		"proj-1.0/subprojects/gst-base/meson.build":   "project('gst-base')\n",
	})

	got, err := OptionsFromArchive(context.Background(), archive)
	if err != nil {
		t.Fatalf("OptionsFromArchive: %v", err)
	}

	if names := declaredNames(got.Root); !equalStrings(names, []string{"qt6"}) {
		t.Errorf("root options: got %v, want [qt6] — a subproject's options are not the root's", names)
	}
	sub, ok := got.Subproject["gst-base"]
	if !ok {
		t.Fatalf("subproject gst-base missing from %v", got.Subproject)
	}
	if names := declaredNames(sub); !equalStrings(names, []string{"gl"}) {
		t.Errorf("gst-base options: got %v, want [gl]", names)
	}
	if sub[0].Subproject != "gst-base" {
		t.Errorf("Option.Subproject: got %q, want %q", sub[0].Subproject, "gst-base")
	}
}

// TestOptionsFromArchive_NoOptionFileIsZeroNotAnError is R1.4. A Meson project
// declaring no options is legitimate, and treating it as a failure would turn
// a whole class of packages into false SKIPPEDs.
func TestOptionsFromArchive_NoOptionFileIsZeroNotAnError(t *testing.T) {
	archive := buildTarGz(t, map[string]string{
		"proj-1.0/meson.build": "project('proj')\n",
	})

	got, err := OptionsFromArchive(context.Background(), archive)
	if err != nil {
		t.Fatalf("OptionsFromArchive: got error %v, want none — a project may declare no options", err)
	}
	if len(got.Root) != 0 {
		t.Errorf("root options: got %v, want none", declaredNames(got.Root))
	}
}

// TestOptionsFromArchive_NoMesonBuildIsUndetermined is R4.3: with no meson.build
// anywhere the build system is not something to guess at, it is something to
// report.
func TestOptionsFromArchive_NoMesonBuildIsUndetermined(t *testing.T) {
	archive := buildTarGz(t, map[string]string{
		"proj-1.0/CMakeLists.txt": "project(proj)\n",
	})

	_, err := OptionsFromArchive(context.Background(), archive)

	if !errors.Is(err, ErrBuildSystemUndetermined) {
		t.Errorf("OptionsFromArchive: got %v, want ErrBuildSystemUndetermined — the caller turns this into a named SKIPPED, never a pass", err)
	}
}

// TestOptionsFromArchive_RecordsItsSources pins the evidence trail. A PASS
// whose sources are not recorded cannot be audited, and the JSON report prints
// this slice for exactly that reason.
func TestOptionsFromArchive_RecordsItsSources(t *testing.T) {
	archive := buildTarGz(t, map[string]string{
		"proj-1.0/meson.build":                   "project('proj')\n",
		"proj-1.0/meson.options":                 "option('qt6')\n",
		"proj-1.0/subprojects/sub/meson.options": "option('gl')\n",
	})

	got, err := OptionsFromArchive(context.Background(), archive)
	if err != nil {
		t.Fatalf("OptionsFromArchive: %v", err)
	}

	if len(got.Sources) != 2 {
		t.Fatalf("Sources: got %v, want the two option files actually read", got.Sources)
	}
	for _, want := range []string{"proj-1.0/meson.options", "proj-1.0/subprojects/sub/meson.options"} {
		if !containsString(got.Sources, want) {
			t.Errorf("Sources %v does not record %q", got.Sources, want)
		}
	}
}
