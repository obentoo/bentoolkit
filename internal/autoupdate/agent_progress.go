package autoupdate

import (
	"path/filepath"

	"github.com/obentoo/bentoolkit/internal/autoupdate/fixer"
	"github.com/obentoo/bentoolkit/internal/common/tui"
)

// agentProgress returns the callback an unattended agent reports its tool
// calls and refusals through: each line joins pkg's task in the live region.
func (a *Applier) agentProgress(pkg string) func(string) {
	return func(line string) {
		a.reporter.TaskLine(pkg, tui.StreamStdout, line, true)
	}
}

// agentLogPath is the file an agent run in stage keeps its event stream in:
// <logsDir>/<category>/<package>/<version>/<stage>-agent.jsonl. It is empty
// when no logs directory is configured, and when pkg or version cannot name a
// directory under it, which is a WARN: the agent still runs, unrecorded.
func (a *Applier) agentLogPath(pkg, version, stage string) string {
	if a.logsDir == "" {
		return ""
	}
	dir, err := packageLogDir(a.logsDir, pkg, version)
	if err != nil {
		a.logger().Warn("the agent's event stream will not be kept",
			"package", pkg, "version", version, "stage", stage, "err", err)
		return ""
	}
	return filepath.Join(dir, fixer.AgentLogName(stage))
}
