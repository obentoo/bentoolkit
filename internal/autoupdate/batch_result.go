// Package autoupdate provides version checking functionality for ebuild autoupdate.
package autoupdate

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// BatchResult holds the outcome of a batch operation over multiple packages,
// such as checking or analyzing every configured package. It separates the
// successfully produced items from the per-package failures so callers can
// report partial success and derive a process exit code.
//
// Concurrency: BatchResult itself provides no synchronization. When workers
// populate Failures from multiple goroutines, callers MUST ensure all worker
// goroutines have joined (for example, after wg.Wait()) before invoking any
// method on the BatchResult.
type BatchResult[T any] struct {
	// Items holds the successfully produced results of the batch operation.
	Items []T
	// Failures maps a package name (category/package) to the error that
	// occurred while processing it.
	Failures map[string]error
}

// ExitCode returns the process exit code that represents the outcome of the
// batch operation:
//
//   - 0 when there are no failures.
//   - 2 when there are failures and no items were produced (total failure).
//   - 1 otherwise (partial failure: some items produced, some failed).
func (b BatchResult[T]) ExitCode() int {
	if len(b.Failures) == 0 {
		return 0
	}
	if len(b.Items) == 0 {
		return 2
	}
	return 1
}

// HasFailures reports whether the batch operation recorded any per-package
// failures.
func (b BatchResult[T]) HasFailures() bool {
	return len(b.Failures) > 0
}

// FormatFailures writes one line per recorded failure to w, in the form
// "ERROR <pkg>: <err>". Failures are emitted sorted lexically by package name
// so the output is deterministic regardless of map iteration order. Multi-line
// error strings are flattened: every newline within an error is rewritten to a
// newline followed by a two-space continuation indent, keeping each failure a
// single parseable logical record. Each record is terminated with a newline.
//
// Concurrency: FormatFailures is NOT safe to call while other goroutines may
// still be writing to Failures. Callers MUST invoke it only after every worker
// goroutine has joined (for example, after wg.Wait()).
func (b BatchResult[T]) FormatFailures(w io.Writer) {
	pkgs := make([]string, 0, len(b.Failures))
	for pkg := range b.Failures {
		pkgs = append(pkgs, pkg)
	}
	sort.Strings(pkgs)

	for _, pkg := range pkgs {
		err := b.Failures[pkg]
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		// Flatten multi-line errors with a continuation indent so each
		// failure stays a single logical record.
		msg = strings.ReplaceAll(msg, "\n", "\n  ")
		fmt.Fprintf(w, "ERROR %s: %s\n", pkg, msg)
	}
}
