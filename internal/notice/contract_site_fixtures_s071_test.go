package notice

// Story 071, sub-task 8.1 (Q12): for every notice fixture of site story 073,
// copied into testdata/site-fixtures/, the YAML RenderSiteYAML produces from
// the equivalent Notice decodes to the same field names, value types and
// values. The site build consumes this output, so the site's own fixtures are
// the contract — not this package's reading of the schema.
//
// Each fixture must be reproducible from `notice new` input: the Notice is
// built by New from the flag values the fixture implies (name, --published
// date, one --affects string per entry), not assembled by hand. The set is
// four notices, including the edge notice 2026-09-28-edge+case_1, all at
// midnight UTC with updated == published.
//
// Values are compared after a generic decode (map[string]any), which keeps
// YAML's typing: a version, slot or timestamp that the fixture writes as a
// string and the renderer writes bare decodes to a different Go type and fails
// here, exactly as it would in the site build.

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

type contractFixtureS071 struct {
	ID       string `yaml:"id"`
	Type     string `yaml:"type"`
	Severity string `yaml:"severity"`
	Title    string `yaml:"title"`
	Summary  string `yaml:"summary"`
	Body     string `yaml:"body"`
	Affects  []struct {
		CP     string `yaml:"cp"`
		Slot   string `yaml:"slot"`
		Ranges []struct {
			Op  string `yaml:"op"`
			Ver string `yaml:"ver"`
		} `yaml:"ranges"`
	} `yaml:"affects"`
	Published string `yaml:"published"`
	Updated   string `yaml:"updated"`
}

func contractNoticeS071(t *testing.T, f contractFixtureS071) Notice {
	t.Helper()
	if len(f.ID) < 12 || f.ID[10] != '-' {
		t.Fatalf("fixture id %q is not YYYY-MM-DD-<name>", f.ID)
	}
	date, name := f.ID[:10], f.ID[11:]
	midnight := date + "T00:00:00Z"
	if f.Published != midnight || f.Updated != midnight {
		t.Fatalf("fixture %s: published %q / updated %q, want both %q (notice new dates a notice at midnight UTC)", f.ID, f.Published, f.Updated, midnight)
	}
	in := Input{
		Type: f.Type, Severity: f.Severity, Name: name, Title: f.Title, Summary: f.Summary,
		Published: date, Author: "bentoo <bentoo@example.org>", Body: f.Body,
	}
	for _, a := range f.Affects {
		spec := a.CP
		if a.Slot != "" {
			spec += ":" + a.Slot
		}
		var ranges []string
		for _, r := range a.Ranges {
			ranges = append(ranges, r.Op+r.Ver)
		}
		if len(ranges) > 0 {
			spec += " " + strings.Join(ranges, ",")
		}
		in.Affects = append(in.Affects, spec)
	}
	pub, err := time.Parse(time.DateOnly, date)
	if err != nil {
		t.Fatalf("fixture %s: date %q: %v", f.ID, date, err)
	}
	n, err := New(in, pub.Add(12*time.Hour))
	if err != nil {
		t.Fatalf("fixture %s is not reproducible from notice new input: %v", f.ID, err)
	}
	return n
}

func contractDiffS071(path string, want, got any) []string {
	if reflect.DeepEqual(want, got) {
		return nil
	}
	wm, wok := want.(map[string]any)
	gm, gok := got.(map[string]any)
	if wok && gok {
		keys := map[string]bool{}
		for k := range wm {
			keys[k] = true
		}
		for k := range gm {
			keys[k] = true
		}
		var names []string
		for k := range keys {
			names = append(names, k)
		}
		sort.Strings(names)
		var out []string
		for _, k := range names {
			out = append(out, contractDiffS071(path+"."+k, wm[k], gm[k])...)
		}
		return out
	}
	ws, wok := want.([]any)
	gs, gok := got.([]any)
	if wok && gok && len(ws) == len(gs) {
		var out []string
		for i := range ws {
			out = append(out, contractDiffS071(fmt.Sprintf("%s[%d]", path, i), ws[i], gs[i])...)
		}
		return out
	}
	return []string{fmt.Sprintf("%s: fixture %#v (%T), rendered %#v (%T)", path, want, want, got, got)}
}

func TestContract_SiteFixturesRenderIdentically(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("testdata", "site-fixtures", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no site fixtures in testdata/site-fixtures/: copy site story 073's tests/fixtures/notices/*.yaml there")
	}
	if len(paths) != 4 {
		t.Errorf("found %d site fixtures, want the four of site story 073", len(paths))
	}
	contractEdgeS071(t, filepath.Join("testdata", "site-fixtures", "2026-09-28-edge+case_1.yaml"))
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var f contractFixtureS071
			if err := yaml.Unmarshal(raw, &f); err != nil {
				t.Fatalf("decoding the fixture: %v", err)
			}
			var want any
			if err := yaml.Unmarshal(raw, &want); err != nil {
				t.Fatal(err)
			}

			rendered, err := RenderSiteYAML(contractNoticeS071(t, f))
			if err != nil {
				t.Fatalf("RenderSiteYAML: %v", err)
			}
			var got any
			if err := yaml.Unmarshal(rendered, &got); err != nil {
				t.Fatalf("the rendered YAML does not parse: %v\n%s", err, rendered)
			}
			for _, d := range contractDiffS071("notice", want, got) {
				t.Error(d)
			}
		})
	}
}

// contractEdgeS071 pins that the edge notice is present and is the one the
// plan names: the characters most likely to be mangled (`+` in a package
// name, a dotted slot, stacked suffixes, a letter before a revision).
func contractEdgeS071(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("the edge fixture is missing: %v", err)
		return
	}
	var f contractFixtureS071
	if err := yaml.Unmarshal(raw, &f); err != nil {
		t.Errorf("decoding the edge fixture: %v", err)
		return
	}
	if f.ID != "2026-09-28-edge+case_1" || f.Type != "news" || f.Severity != "info" {
		t.Errorf("edge fixture id/type/severity = %q/%q/%q", f.ID, f.Type, f.Severity)
	}
	if len(f.Affects) != 1 || f.Affects[0].CP != "dev-libs/libfoo+" || f.Affects[0].Slot != "0.1" || len(f.Affects[0].Ranges) != 2 ||
		f.Affects[0].Ranges[0].Op != ">=" || f.Affects[0].Ranges[0].Ver != "1.0_rc1_p2" ||
		f.Affects[0].Ranges[1].Op != "<" || f.Affects[0].Ranges[1].Ver != "1.0b-r0" {
		t.Errorf("edge fixture affects = %+v, want dev-libs/libfoo+ slot 0.1 >=1.0_rc1_p2,<1.0b-r0", f.Affects)
	}
}
