package autoupdate

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
// It is the ONLY place a record becomes text. Both writers go through it — the
// registry rewrite behind `overlay analyze --save` (savePackagesConfig) and the
// suggestion `overlay analyze` prints for the maintainer to paste — so a
// generated record and a linted record cannot drift apart: both are emitted from
// the same slice the linter ranks against (R8, R8.1).
//
// The encoder it replaces lost that agreement three ways, all of them producing
// a file the linter next to it rejected:
//
//   - fields came out in STRUCT-DECLARATION order, which is not the canonical
//     one — the struct declares timeout before select, the registry writes
//     select before timeout;
//   - a populated `headers` or `meta` came out as a SUB-TABLE,
//     ["dev-util/x".headers], which the record scanner reads as the header of a
//     new record: the record it belongs to was then reported unclosed and
//     undocumented while the phantom collected its `# END`;
//   - `omitempty` does not suppress a numeric zero in BurntSushi v1.6.0 (its
//     isEmpty has no numeric case), so every saved record carried `timeout = 0`
//     and `revision = 0`, two keys not one of the registry's 411 records
//     declares.
//
// Emission is driven by reflection over PackageConfig's own `toml:` tags rather
// than by a hand-written switch, so a field added to the struct and to
// CanonicalFieldOrder is written without a third edit here — and cannot be
// forgotten: TestCanonicalFieldOrderCoversPackageConfig pins that the order
// slice covers every tag exactly once, and a tag absent from it is a tag this
// function never emits.
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
			// linter reports it as redundant (LintRedundantEnabled, R2.1). A
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

	b.WriteString(recordEndMarker + "\n")
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
// That is measured practice, not taste. The registry writes 414 of its regexes
// as literal strings and 2 as basic ones, and those 2 are correct rather than
// sloppy: their regex contains a ', which a literal string cannot hold at all —
// TOML gives it no escape. The rule below reproduces that split, so what this
// emits is what a maintainer would have typed: `url = "https://…"` plain,
// `pattern = 'href="([0-9.]+)/"'` literal, and a pattern carrying its own quote
// escaped into the basic form.
//
// The rule reads the VALUE, not the field. The registry's own convention is
// field-driven — a regex field is literal-quoted whether or not it needs to be —
// and reproducing that would mean a second list, this one naming which of the 38
// fields hold regexes, kept in step with the struct by hand. Measured against
// the real registry, declining to keep that list costs 53 values out of ~1700 a
// change of quoting style on a full rewrite (5 scalars such as `series = '_pre$'`
// and 48 transform elements such as `['^v', ""]`), every one of them style only:
// no value changes, nothing stops parsing, and no lint rule reads quoting at all.
// Revisit it only if the registry starts caring about the style itself.
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
