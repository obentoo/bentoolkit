package llm

// Authored for story 051 (llm-agent-least-privilege), sub-task 5.3 — a
// regression guard, GREEN today (S051-R5.3).
//
// R5.3: nothing an operator writes can widen an agent's tools, hosts or paths.
// The two places operator configuration reaches this package are LLMConfig
// (the [llm] block) and PackageConfig (one packages.toml section). This walks
// both — nested structs included — and refuses any field whose Go name or toml
// key names a permission-shaped knob. A green run says no such key exists; it
// does not say how an existing key is used, which the argv tests cover.

import (
	"reflect"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
)

var wideningKnobWords = []string{"tool", "permission", "allowed", "webfetch", "domain", "bash", "adddir", "add_dir", "sandbox", "disallow"}

func wideningKnobs(tp reflect.Type, path string, seen map[reflect.Type]bool) []string {
	for tp.Kind() == reflect.Pointer || tp.Kind() == reflect.Slice || tp.Kind() == reflect.Map {
		tp = tp.Elem()
	}
	if tp.Kind() != reflect.Struct || seen[tp] {
		return nil
	}
	seen[tp] = true
	var out []string
	for i := 0; i < tp.NumField(); i++ {
		f := tp.Field(i)
		key := strings.ToLower(f.Name + " " + f.Tag.Get("toml"))
		for _, w := range wideningKnobWords {
			if strings.Contains(key, w) {
				out = append(out, path+"."+f.Name+" (toml "+f.Tag.Get("toml")+")")
				break
			}
		}
		out = append(out, wideningKnobs(f.Type, path+"."+f.Name, seen)...)
	}
	return out
}

// TestNoConfigKeyWidensAnAgent is R5.3's "no configuration key" half.
func TestNoConfigKeyWidensAnAgent(t *testing.T) {
	for _, root := range []struct {
		name string
		tp   reflect.Type
	}{
		{"LLMConfig", reflect.TypeOf(LLMConfig{})},
		{"PackageConfig", reflect.TypeOf(registry.PackageConfig{})},
	} {
		if knobs := wideningKnobs(root.tp, root.name, map[reflect.Type]bool{}); len(knobs) > 0 {
			t.Errorf("%s exposes a key that could widen an agent's tools, hosts or paths (R5.3): %v", root.name, knobs)
		}
	}
}
