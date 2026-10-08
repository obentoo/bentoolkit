package config

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// configKeyPaths walks t's yaml tags and returns every key a config file can
// set, as dotted paths ("autoupdate.validate.depths.major"). A map of structs
// contributes its element's keys under a "*" segment, since the map key is
// the operator's own name.
func configKeyPaths(t reflect.Type, prefix string) []string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	var paths []string
	for f := range t.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if name == "" || name == "-" {
			continue
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		paths = append(paths, path)

		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		switch {
		case ft.Kind() == reflect.Struct:
			paths = append(paths, configKeyPaths(ft, path)...)
		case ft.Kind() == reflect.Map && derefKind(ft.Elem()) == reflect.Struct:
			paths = append(paths, configKeyPaths(ft.Elem(), path+".*")...)
		}
	}
	return paths
}

func derefKind(t reflect.Type) reflect.Kind {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.Kind()
}

// TestExampleConfig_DocumentsEveryKey is the other half of
// TestExampleConfigLoads. That test stops the example from naming a key the
// code does not read; this one stops the code from reading a key the example
// never mentions, which is how distdir, distfiles_cache and the whole
// validate block went undocumented. A key counts as documented when its name
// appears as `name:` anywhere in the file, live or commented out — most
// optional keys ship commented on purpose.
func TestExampleConfig_DocumentsEveryKey(t *testing.T) {
	data, err := os.ReadFile(exampleConfigPath(t))
	if err != nil {
		t.Fatalf("read config.example.yaml: %v", err)
	}
	text := string(data)

	paths := configKeyPaths(reflect.TypeFor[Config](), "")
	if len(paths) < 40 {
		t.Fatalf("walked only %d keys from Config; the walker is broken, not the example", len(paths))
	}
	for _, path := range paths {
		leaf := path[strings.LastIndex(path, ".")+1:]
		re := regexp.MustCompile(`(?m)^[\s#]*` + regexp.QuoteMeta(leaf) + `:`)
		if !re.MatchString(text) {
			t.Errorf("config.example.yaml never mentions %s: add it, commented out if it is optional, with its unit and default", path)
		}
	}
}
