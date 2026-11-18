// Command i18n-extract writes po/bentoolkit.pot: every message marked with
// i18n.N or i18n.T under cmd/ and internal/, and of the safety scripts under
// misc/safety/. Run it from the repository root,
// normally through "make i18n-update".
package main

import (
	"bytes"
	"fmt"
	"os"

	"github.com/obentoo/bentoolkit/internal/common/i18n/po"
)

func main() {
	out := "po/bentoolkit.pot"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	msgs, err := po.Extract(".", "cmd", "internal", "misc/safety")
	if err != nil {
		fmt.Fprintln(os.Stderr, "i18n-extract:", err)
		os.Exit(1)
	}
	var buf bytes.Buffer
	if err := po.WritePOT(&buf, msgs); err != nil {
		fmt.Fprintln(os.Stderr, "i18n-extract:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(out, buf.Bytes(), 0o644); err != nil { //nolint:gosec // G306: a source file in the checkout, world-readable like every other.
		fmt.Fprintln(os.Stderr, "i18n-extract:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "i18n-extract: %d messages -> %s\n", len(msgs), out)
}
