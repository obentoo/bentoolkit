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

// JSON writes run as one indented JSON document: the whole report, for a reader
// that is a program rather than a person.
//
// The model IS the document: report.Run goes to encoding/json as it stands, with
// no wire type, so the document carries every fact the renderers read by
// construction. No payload TYPE is named here, so a field added to any payload
// reaches the export with no edit. The model's json tags are therefore a
// contract (a renamed tag breaks a consumer's jq); root keys are declared in
// report/run.go, the ones under "payload" by each payload. kind and schema sit
// at the root: kind is what a consumer branches on, and schema is 2 because the
// unversioned pre-envelope shape is schema 1 in the field.
//
// It takes no Options, so an export has no Width to shorten to and no ShowAll to
// honour: it always carries every unit and every reason in full. It writes and
// never reads (see report.Run). A nil slice reaches the wire as null, on
// purpose: rewriting it to [] would break the lossless round trip the payload
// is pinned on, so the producer that left it nil is where to fix it. HTML
// escaping stays on, matching renderValidateJSON in cmd/bentoo, so the CLI
// emits one dialect of JSON. A write error is returned, never swallowed.
func JSON(w io.Writer, run report.Run) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", jsonIndent)

	if err := enc.Encode(run); err != nil {
		return fmt.Errorf("writing the JSON report: %w", err)
	}
	return nil
}
