package autoupdate

import "os/exec"

// The core keeps its own copy of the claude-CLI seam: the fixers check for the
// binary before they build an agent, and the applier uses the same seam for
// pkgcheck. internal/autoupdate/llm keeps the original for the Claude Code
// client, so each package's tests swap their own variable (story 061).

// lookPath is the seam used to detect the `claude` binary. It defaults to
// exec.LookPath and is overridable in tests so construction is deterministic
// regardless of the host PATH.
var lookPath = exec.LookPath

// claudeAvailable reports whether the `claude` CLI is resolvable on PATH (S003-R6.1).
func claudeAvailable() bool {
	_, err := lookPath("claude")
	return err == nil
}
