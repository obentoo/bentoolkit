package registry

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// RenderRecord renders one packages.toml record as text: the quoted `[header]`,
// every field the config actually sets in CanonicalFieldOrder, the doc field,
// and the `# END` marker that closes it.
//
// It is the ONLY place a record becomes text: both `overlay analyze --save`
// (savePackagesConfig) and the suggestion `overlay analyze` prints go through
// it, emitting from the same slice the linter ranks against, so a generated
// and a linted record cannot drift apart. A generic TOML encoder would not do:
// it writes fields in struct order, a populated `headers`/`meta` as a
// SUB-TABLE the record scanner reads as a new record, and (BurntSushi v1.6.0
// omitempty ignores numeric zeros) `timeout = 0` / `revision = 0`.
//
// Emission is driven by reflection over PackageConfig's `toml:` tags, so a field
// added to the struct and to CanonicalFieldOrder needs no third edit here;
// TestCanonicalFieldOrderCoversPackageConfig pins that the order slice covers
// every tag exactly once.
//
// A nil config renders nothing rather than a headed but empty record: the
// caller has no record to write.
func RenderRecord(pkg string, cfg *PackageConfig) string {
	if cfg == nil {
		return ""
	}

	rv := reflect.ValueOf(cfg).Elem()

	var b strings.Builder
	b.WriteString("[" + tomlString(pkg) + "]\n")

	for _, key := range CanonicalFieldOrder {
		field, known := recordFieldsByKey[key]
		if !known {
			continue
		}
		fv := rv.Field(field.index)
		if field.omitEmpty && isEmptyFieldValue(fv) {
			continue
		}

		switch key {
		case "enabled":
			// `enabled = true` says exactly what an absent enabled says, and the
			// linter reports it as redundant (LintRedundantEnabled). A
			// writer emitting what the linter next to it reports is the
			// disagreement this function exists to remove, so only the
			// informative `false` is written.
			if !fv.IsNil() && fv.Elem().Bool() {
				continue
			}
		case "comments":
			// The doc field is a multi-line string and cannot go through the
			// value renderer, which quotes on one line: a re-encode would
			// collapse the documentation into one line of escaped \n, and the
			// registry is read — and edited — by humans and by raw-text tooling.
			b.WriteString(formatCommentsField(fv.String()))
			continue
		}

		value, ok := renderTOMLValue(fv)
		if !ok {
			continue
		}
		b.WriteString(key + " = " + value + "\n")
	}

	b.WriteString(RecordEndMarker + "\n")
	return b.String()
}

// recordFieldMeta locates one PackageConfig field by the toml key it carries.
type recordFieldMeta struct {
	index     int
	omitEmpty bool
}

// recordFieldsByKey maps each `toml:` key of PackageConfig to the struct field
// that holds it. Built once, because struct tags cannot change at runtime.
var recordFieldsByKey = func() map[string]recordFieldMeta {
	rt := reflect.TypeOf(PackageConfig{})
	m := make(map[string]recordFieldMeta, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("toml")
		if tag == "" || tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		meta := recordFieldMeta{index: i}
		for _, opt := range strings.Split(opts, ",") {
			if opt == "omitempty" {
				meta.omitEmpty = true
			}
		}
		m[name] = meta
	}
	return m
}()

// isEmptyFieldValue reports whether a field carries nothing worth writing —
// what `omitempty` is meant to mean. It is spelled out here rather than left to
// the encoder because BurntSushi v1.6.0's own isEmpty has no numeric case: both
// `timeout` and `revision` are tagged omitempty and both were still written as
// `= 0` into every saved record, a key no hand-written record declares and
// every maintainer would then have to delete.
//
// Note what stays: `url` and `parser` carry no omitempty, so they are written
// even when empty. That is deliberate — they are the two required fields, and a
// record missing one should show the empty value it needs filled rather than
// hide the gap.
func isEmptyFieldValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Pointer:
		return v.IsNil()
	case reflect.Map, reflect.Slice:
		return v.Len() == 0
	default:
		return v.IsZero()
	}
}

// renderTOMLValue renders one field value as the TOML text right of the "=",
// reporting false for a value it cannot render.
//
// Every kind PackageConfig uses is covered — string, bool, *bool, int,
// map[string]string and [][]string. TestRenderRecordWritesEveryField pins that
// by rendering a record with all 38 fields set and checking each one came out,
// so a field of an unhandled kind fails a test rather than disappearing from the
// registry in silence.
func renderTOMLValue(v reflect.Value) (string, bool) {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return "", false
		}
		return renderTOMLValue(v.Elem())

	case reflect.Bool:
		return strconv.FormatBool(v.Bool()), true

	case reflect.String:
		return tomlString(v.String()), true

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10), true

	case reflect.Map:
		if v.Type() == reflect.TypeFor[map[string]RequireSpec]() {
			return tomlRequiresTable(v.Interface().(map[string]RequireSpec)), true
		}
		if v.Type().Key().Kind() != reflect.String || v.Type().Elem().Kind() != reflect.String {
			return "", false
		}
		return tomlInlineTable(v), true

	case reflect.Slice, reflect.Array:
		parts := make([]string, 0, v.Len())
		for i := 0; i < v.Len(); i++ {
			elem, ok := renderTOMLValue(v.Index(i))
			if !ok {
				return "", false
			}
			parts = append(parts, elem)
		}
		return "[" + strings.Join(parts, ", ") + "]", true
	}

	return "", false
}

// tomlRequiresTable renders a record's `requires` as ONE inline line, nested
// tables included, for the reason tomlInlineTable gives: a sub-table header
// would be read by the record scanner as a new record. Outer keys are sorted and
// each entry's keys come in a fixed order — pattern, url, pin, an empty url
// omitted — so two saves of the same config produce the same bytes.
func tomlRequiresTable(m map[string]RequireSpec) string {
	atoms := make([]string, 0, len(m))
	for atom := range m {
		atoms = append(atoms, atom)
	}
	sort.Strings(atoms)
	parts := make([]string, 0, len(atoms))
	for _, atom := range atoms {
		spec := m[atom]
		fields := []string{"pattern = " + tomlString(spec.Pattern)}
		if spec.URL != "" {
			fields = append(fields, "url = "+tomlString(spec.URL))
		}
		fields = append(fields, "pin = "+tomlString(spec.Pin))
		parts = append(parts, tomlString(atom)+" = { "+strings.Join(fields, ", ")+" }")
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}

// tomlInlineTable renders a string map as a one-line inline table, the shape the
// registry writes: headers = { "User-Agent" = "bentoo-autoupdate" }.
//
// Inline is the whole point. Encoded as a sub-table the same map becomes a
// ["dev-util/x".headers] line, and the record scanner reads that as the header
// of a NEW record — so the record it belongs to is reported unclosed and
// undocumented while the phantom swallows the `# END`. Nothing in the registry
// is corrupted by this today only because no record in it declares a map.
//
// Keys are quoted, as all 167 headers lines in the registry are, and sorted, so
// two saves of the same config produce the same bytes.
func tomlInlineTable(v reflect.Value) string {
	type pair struct{ key, value string }

	pairs := make([]pair, 0, v.Len())
	for iter := v.MapRange(); iter.Next(); {
		pairs = append(pairs, pair{iter.Key().String(), iter.Value().String()})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].key < pairs[j].key })

	parts := make([]string, 0, len(pairs))
	for _, p := range pairs {
		parts = append(parts, tomlString(p.key)+" = "+tomlString(p.value))
	}
	if len(parts) == 0 {
		return "{}"
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}

// tomlString renders a Go string as a single-line TOML string, choosing between
// the two forms the way the registry's hand-written records do: a LITERAL string
// ('…') whenever the value would otherwise need escaping, and a basic string
// ("…") otherwise.
//
// That reproduces what a maintainer types: `url = "https://…"` plain,
// `pattern = 'href="([0-9.]+)/"'` literal, and a pattern carrying its own '
// in the basic form, since a literal string cannot hold a ' at all.
//
// The rule reads the VALUE, not the field. Following the registry's
// field-driven convention would need a hand-kept list of which fields hold
// regexes; without it a full rewrite changes the quoting style of about 53 of
// ~1700 values — style only, no value changes and no lint rule reads quoting.
//
// A value holding a newline or any other control character also takes the basic
// form: a literal string may contain none of them but tab.
func tomlString(s string) string {
	if strings.ContainsAny(s, `"\`) && !strings.ContainsRune(s, '\'') && !hasTOMLControlChar(s) {
		return "'" + s + "'"
	}
	return tomlBasicString(s)
}

// tomlBasicString renders a value as a TOML basic string, escaping what the
// format requires: the backslash, the quote that would end the string, and every
// control character. TOML has no \x escape, so a control character without a
// named escape is written as \uXXXX rather than left raw, which would not parse.
func tomlBasicString(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// hasTOMLControlChar reports whether the string holds a character a TOML literal
// string may not contain: every control character except tab.
func hasTOMLControlChar(s string) bool {
	for _, r := range s {
		if (r < 0x20 && r != '\t') || r == 0x7f {
			return true
		}
	}
	return false
}

// formatCommentsField renders a record's doc text as the TOML multi-line basic
// string that closes the record, marker line excluded.
//
// Three things are escaped or adjusted, all for the same reason — the registry
// is edited by hand and read back by raw-text tooling, so the output has to be
// both valid TOML and safe to scan line by line:
//   - a backslash, and any run of three or more quotes, would either be read as
//     an escape or close the string early;
//   - a line starting with "[" looks like a section header to the surgical edit
//     in setPackagesEnabled, which would cut the record short there, so it is
//     indented by one space (the lint rejects the same shape in hand-written
//     records);
//   - trailing whitespace is dropped so a re-encode is byte-stable.
func formatCommentsField(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, `\`, `\\`)
	// Only a run of three or more quotes can close the string early; escape every
	// quote in such a run and leave ordinary "quoted" words alone.
	s = tripleQuoteRegex.ReplaceAllStringFunc(s, func(run string) string {
		return strings.Repeat(`\"`, len(run))
	})

	lines := strings.Split(strings.Trim(s, "\n"), "\n")
	for i, ln := range lines {
		ln = strings.TrimRight(ln, " \t")
		if strings.HasPrefix(ln, "[") {
			ln = " " + ln
		}
		lines[i] = ln
	}

	return "comments = \"\"\"\n" + strings.Join(lines, "\n") + "\n\"\"\"\n"
}
