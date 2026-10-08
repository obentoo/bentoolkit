package config

import (
	"os"
	"strconv"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v3"
)

// S048-R3.5 asks the shipped example to DOCUMENT autoupdate.review.timeout, in
// the language that file is written in.
//
// WHY THIS IS NOT TestExampleConfigLoads (config_example_test.go:31). That test
// decodes the example strictly and fails on a key the struct does not have — it
// guards the example against documenting a setting the code ignores. It cannot
// guard the converse, which is what R3.5 is: a knob the code reads and the
// example never mentions decodes perfectly and leaves the operator with no way
// to know the knob exists. TestExampleConfigLoads passes today, passes once the
// struct field lands, and passes forever whether or not the example is ever
// written — so it can never fail for R3.5's reason. This file can: it fails
// while the key is undocumented and passes only once the example says it.
//
// It reads the example as a YAML DOCUMENT rather than through Config on
// purpose: what R3.5 promises is a line an operator can find and copy, and a
// struct decode cannot tell a documented key from an absent one — both arrive
// as the zero value.
//
// SCOPE. This file only ever asserts that documentation was ADDED. The sentence
// at config.example.yaml:25-26 is about `--check/--apply` concurrency, it is
// true, and the word `compare` appears nowhere in the file; nothing here asserts
// on it, requires it to change, or would be satisfied by changing it.

// exampleDocument parses config.example.yaml into a yaml.Node tree, which keeps
// the comments — the part of "documented" that a decode into Config throws away.
func exampleDocument(t *testing.T) *yaml.Node {
	t.Helper()
	path := exampleConfigPath(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("config.example.yaml is not valid YAML: %v", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		t.Fatalf("config.example.yaml does not hold a top-level mapping")
	}
	return doc.Content[0]
}

// mappingEntry returns the key node and the value node for `key` in a mapping,
// or ok=false when the mapping does not carry it.
func mappingEntry(m *yaml.Node, key string) (keyNode, valNode *yaml.Node, ok bool) {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil, nil, false
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i], m.Content[i+1], true
		}
	}
	return nil, nil, false
}

// TestExampleConfig_DocumentsTheReviewTimeoutKey is S048-R3.5: the operator-
// facing file tells the operator this knob exists, as a live key they can copy,
// carrying the unit, in the language of the file it is written into.
func TestExampleConfig_DocumentsTheReviewTimeoutKey(t *testing.T) {
	root := exampleDocument(t)

	_, autoupdate, ok := mappingEntry(root, "autoupdate")
	if !ok {
		t.Fatal("config.example.yaml has no `autoupdate:` block; the review budget is documented inside it (S048-R3.4)")
	}

	reviewKey, review, ok := mappingEntry(autoupdate, "review")
	if !ok {
		t.Fatalf("config.example.yaml does not document `autoupdate.review.timeout` (S048-R3.5): the `autoupdate:` block has no `review:` key.\n" +
			"The budget an operator most needs to raise is the one nothing in the shipped example mentions. " +
			"Follow the cache_ttl / http_timeout shape at config.example.yaml:50-54 — a commented, uncommented-out key with its unit and its default.")
	}

	timeoutKey, timeout, ok := mappingEntry(review, "timeout")
	if !ok {
		t.Fatalf("config.example.yaml has `autoupdate.review:` but no `timeout:` under it; `autoupdate.review.timeout` is the key S048-R3.5 asks for")
	}

	// The key is live, not a placeholder: an operator copies this file whole.
	if timeout.Tag != "!!int" {
		t.Errorf("autoupdate.review.timeout is documented as %s %q, want an integer of seconds — "+
			"the unit cache_ttl, http_timeout and autoupdate.validate.timeout already use (S048-R3.3)", timeout.Tag, timeout.Value)
	} else if seconds, err := strconv.Atoi(timeout.Value); err != nil || seconds <= 0 {
		t.Errorf("autoupdate.review.timeout is documented as %q; a shipped example must carry a usable budget, "+
			"and zero or negative is the value S048-R3.2 treats as unconfigured", timeout.Value)
	}

	// "Documented" means a neighbour-shaped explanation, not a bare key: every
	// entry around it says what it governs, in what unit, with its default.
	doc := strings.TrimSpace(strings.Join([]string{
		reviewKey.HeadComment, reviewKey.LineComment, reviewKey.FootComment,
		timeoutKey.HeadComment, timeoutKey.LineComment, timeoutKey.FootComment,
	}, "\n"))
	if doc == "" {
		t.Fatal("autoupdate.review.timeout is present but carries no comment; every key around it explains what it governs " +
			"and in what unit (config.example.yaml:50-54), and an undocumented key documents nothing (S048-R3.5)")
	}

	// R3.5 says "in the language that file is written in". The file is written
	// in English, and both neighbours name the unit the same way — "in
	// seconds". A comment in another language would leave the file bilingual.
	if !strings.Contains(strings.ToLower(doc), "seconds") {
		t.Errorf("the comment documenting autoupdate.review.timeout does not say `seconds`:\n%s\n"+
			"config.example.yaml is written in English and its two nearest neighbours both give the unit as "+
			"\"in seconds\"; S048-R3.5 asks for this key in the same language, and S048-R3.3 for the same unit", doc)
	}
}
