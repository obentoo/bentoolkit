package validate

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// pkgcheck's JsonStream reporter writes one JSON object per line. The struct
// below was designed against records CAPTURED from a real scan, not from the
// documentation — design.md D7 recorded that no record had ever been observed,
// and that the field carrying a level was therefore an assumption.
//
// # What the capture settled
//
// Two things, and the first explains the second.
//
//  1. The empty scans D7 reported were not "no findings". pkgcheck's GitAddon
//     raises on this overlay's history, prints a Python traceback to STDERR and
//     exits 0 with an empty stdout — which is why three scans looked clean.
//     Running with `--cache=-git` produces findings immediately. The existing
//     runQACheck in applier.go already documents that traceback; nothing had
//     connected it to the empty output.
//
//  2. Across 95 real records there is NO LEVEL FIELD. The only classifier is
//     `__class__`, the keyword's own name. Every other key is specific to that
//     keyword — `lineno`, `eclass`, `flags`, `replacement`, and so on.
//
// So every pkgcheck finding is carried at `info`, and the report says so. That
// is the plan's own IF-branch, and it is the honest option: inferring a level
// from the message text would be a guess wearing a severity's clothes, and this
// story exists to remove exactly that.
//
// (`pkgcheck show --keywords` DOES print a severity per keyword. Reading it
// would be a lookup rather than an inference, and is a reasonable follow-up —
// but it is a second invocation and a second cache, and the exit code does not
// depend on any of it (D8), so it buys presentation only.)

// pkgcheckIdentity is the part of a record whose shape is stable across every
// keyword. Everything else varies per keyword and is rendered generically.
type pkgcheckIdentity struct {
	Class    string `json:"__class__"`
	Category string `json:"category"`
	Package  string `json:"package"`
	Version  string `json:"version"`
}

// identityKeys are the fields pkgcheckIdentity already consumes, so the
// generic renderer does not repeat them.
var identityKeys = map[string]bool{
	"__class__": true, "category": true, "package": true, "version": true,
}

// decodePkgcheckRecord turns one JsonStream line into a Finding.
//
// The keyword-specific fields are rendered generically, in key order, rather
// than mapped one keyword at a time. pkgcheck ships hundreds of keywords and
// adds more every release; a per-keyword mapping would silently lose the detail
// of every keyword written after this code, and a QA finding stripped of its
// detail is one nobody can act on.
//
// A line that cannot be decoded returns an error rather than being skipped. A
// dropped record makes a noisy package look clean, and the caller turns this
// error into a named SKIPPED — which says "the QA gate did not run", not "the
// QA gate found nothing".
func decodePkgcheckRecord(line []byte) (Finding, error) {
	var id pkgcheckIdentity
	if err := json.Unmarshal(line, &id); err != nil {
		return Finding{}, fmt.Errorf("decoding pkgcheck record %q: %w", truncate(string(line)), err)
	}
	if id.Class == "" {
		return Finding{}, fmt.Errorf("pkgcheck record %q carries no __class__; it is not a JsonStream finding", truncate(string(line)))
	}

	var rest map[string]json.RawMessage
	if err := json.Unmarshal(line, &rest); err != nil {
		return Finding{}, fmt.Errorf("decoding pkgcheck record %q: %w", truncate(string(line)), err)
	}

	var b strings.Builder
	b.WriteString(id.Class)
	if id.Category != "" && id.Package != "" {
		b.WriteString(" ")
		b.WriteString(id.Category + "/" + id.Package)
		if id.Version != "" {
			b.WriteString("-" + id.Version)
		}
	}

	keys := make([]string, 0, len(rest))
	for k := range rest {
		if !identityKeys[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for i, k := range keys {
		if i == 0 {
			b.WriteString(": ")
		} else {
			b.WriteString(", ")
		}
		b.WriteString(k + "=" + string(rest[k]))
	}

	return Finding{
		Gate:     GateQA,
		Severity: SeverityInfo,
		Detail:   b.String(),
	}, nil
}

// truncate keeps a malformed line from filling the report with something that
// is, by definition, not worth reading in full.
func truncate(s string) string {
	const max = 120
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
