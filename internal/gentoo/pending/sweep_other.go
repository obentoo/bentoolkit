//go:build !unix

package pending

// sweepGroup does nothing where there is no process group to signal: a helper
// the command left behind keeps running, as procgroup documents.
func sweepGroup(int) {}
