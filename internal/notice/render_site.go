package notice

import (
	"fmt"
	"strings"
	"time"

	yaml "go.yaml.in/yaml/v3"
)

// RenderSiteYAML renders n as the site's notice file, with the fields of the
// site's notice data model: id, type, severity, title, summary, body, affects,
// published, updated, in that order. The schema has no author and no revision —
// those live in the news item only.
//
// The document is built as a yaml.Node tree rather than marshalled from a
// struct, because the site's YAML parser types every plain scalar: a version
// `1.10` written bare reads back as the float 1.1, a slot `1` as an integer,
// and a timestamp as a date. Every such scalar is therefore double-quoted, and
// the body is a literal block so its lines survive as written.
//
// The reader is js-yaml (YAML 1.2), not yaml.v3 (YAML 1.1), and the two
// disagree on what a plain scalar means: js-yaml reads `2026-10-02 10:00:00Z`
// or `._5` as a date or a number that yaml.v3 would leave plain. Title and
// summary are free text, so they are always quoted too.
func RenderSiteYAML(n Notice) ([]byte, error) {
	affects := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	if len(n.Affects) == 0 {
		affects.Style = yaml.FlowStyle // `affects: []`, never null
	}
	for _, a := range n.Affects {
		entry := mapping(field{"cp", str(a.CP)})
		if a.Slot != "" {
			entry.Content = append(entry.Content, key("slot"), quoted(a.Slot))
		}
		ranges := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		if len(a.Ranges) == 0 {
			ranges.Style = yaml.FlowStyle
		}
		for _, r := range a.Ranges {
			rn := mapping(field{"op", quoted(r.Op)}, field{"ver", quoted(r.Ver)})
			rn.Style = yaml.FlowStyle
			ranges.Content = append(ranges.Content, rn)
		}
		entry.Content = append(entry.Content, key("ranges"), ranges)
		affects.Content = append(affects.Content, entry)
	}

	body := str(strings.TrimRight(n.Body, "\n") + "\n")
	body.Style = bodyStyle(body.Value)

	doc := mapping(
		field{"id", str(n.ID)},
		field{"type", str(n.Type)},
		field{"severity", str(n.Severity)},
		field{"title", quoted(n.Title)},
		field{"summary", quoted(n.Summary)},
		field{"body", body},
		field{"affects", affects},
		field{"published", quoted(timestamp(n.Published))},
		field{"updated", quoted(timestamp(n.Updated))},
	)

	var b strings.Builder
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("encoding the site YAML of notice %s: %w", n.ID, err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("encoding the site YAML of notice %s: %w", n.ID, err)
	}
	return []byte(b.String()), nil
}

// bodyStyle is a literal block, except where yaml.v3 would write one that
// js-yaml reads back differently: U+2028 and U+2029 are line breaks to yaml.v3
// (YAML 1.1) and ordinary characters to js-yaml (YAML 1.2), so the indentation
// yaml.v3 adds after them would join the text; and a leading line feed makes
// yaml.v3 drop one blank line. Those bodies are double-quoted, escaped.
func bodyStyle(body string) yaml.Style {
	if strings.HasPrefix(body, "\n") || strings.ContainsAny(body, "\u2028\u2029") {
		return yaml.DoubleQuotedStyle
	}
	return yaml.LiteralStyle
}

// timestamp is the schema's RFC 3339 form, in UTC with a `Z` offset and no
// fractional seconds.
func timestamp(t time.Time) string {
	return t.UTC().Truncate(time.Second).Format(time.RFC3339)
}

// field is one key of a mapping node, in document order.
type field struct {
	k string
	v *yaml.Node
}

func mapping(fields ...field) *yaml.Node {
	m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, f := range fields {
		m.Content = append(m.Content, key(f.k), f.v)
	}
	return m
}

func key(s string) *yaml.Node { return str(s) }

// str is a string scalar; the encoder still quotes it when a plain scalar
// would read back as something else.
func str(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
}

// quoted is a string scalar that is always double-quoted.
func quoted(s string) *yaml.Node {
	n := str(s)
	n.Style = yaml.DoubleQuotedStyle
	return n
}
