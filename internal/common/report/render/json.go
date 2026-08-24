package render

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/obentoo/bentoolkit/internal/common/report"
)

// jsonIndent is one level of indentation in the exported document.
//
// It is named because this package already has an indent, and the two must
// never be unified: that one counts DISPLAY CELLS a terminal renderer pads
// with, this one is bytes a machine reader ignores entirely. They agree on two
// only by coincidence, and a reader meeting the bare literal below would have no
// way to tell which of the two kinds it was.
const jsonIndent = "  "

// JSON writes r as one indented JSON document: the whole report, for a reader
// that is a program rather than a person (R9, R9.4).
//
// # The model IS the document
//
// There is no wire type here, and no struct literal copying field by field into
// one. report.Report is handed to encoding/json as it stands, which is what
// makes R9.4 — "a machine reader sees the same fields the renderers saw" — true
// by construction rather than by a mapping somebody has to keep in sync (D8). A
// field added to the model reaches the export with no edit to this file; a
// mapping would reach it only when someone remembered, and the failure would be
// silent on both sides.
//
// The wire keys are therefore the model's json tags, and those tags are a
// contract: renaming one renames the key under a consumer who has already
// shipped a jq expression against it. See internal/common/report/model.go.
//
// # It takes no Options, exactly as Markdown does not
//
// R9.3 says an export carries the complete report — every package, every reason
// in full, no shortening, whatever the terminal was asked for. The absence of a
// parameter is how that is enforced: there is no Width here to shorten to and no
// ShowAll here to honour, so an export that mirrored screen truncation is not
// something to remember not to write. It cannot be written.
//
// # A nil slice reaches the wire as null, on purpose
//
// internal/autoupdate/validate has a Normalized() that turns nil slices into
// empty ones so jq never meets a null. It is deliberately not copied here. That
// is a transformation, and a transformation is the very thing D8 removes: the
// round trip this file is pinned on — marshal, unmarshal, compare whole — stops
// being lossless the moment the document says something the model did not. A
// consumer that trips over a null is looking at a producer that left a slice
// nil; that is where it gets fixed, not by a quiet rewrite on the way out.
//
// # The encoder's defaults are kept
//
// HTML escaping stays on, matching renderValidateJSON in cmd/bentoo — the CLI's
// other JSON surface. It costs readability on a reason containing > or &, and
// costs nothing in meaning, because the value round-trips identically. One tool
// emitting two dialects of JSON costs more.
//
// # The write is checked
//
// A report redirected to a full disk is an ordinary failure, and a renderer that
// swallowed it would report success for output nobody received.
func JSON(w io.Writer, r report.Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", jsonIndent)

	if err := enc.Encode(r); err != nil {
		return fmt.Errorf("writing the JSON report: %w", err)
	}
	return nil
}
