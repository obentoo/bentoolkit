package autoupdate

import (
	"path/filepath"
	"sync"

	"github.com/obentoo/bentoolkit/internal/autoupdate/fixer"
	"github.com/obentoo/bentoolkit/internal/common/tui"
)

// agentProgress returns the callback an unattended agent reports its tool
// calls and refusals through, each line joining pkg's task in the live region,
// and the done the call site runs as soon as the agent call returns.
//
// After done, the callback adds nothing: a delivery the fixer had already
// started when it abandoned a slow callback may still arrive, and it must not
// land in a task the applier has moved on from or closed. The check and the
// TaskLine share a lock with done, so a line that passed the check is in the
// task before done returns.
func (a *Applier) agentProgress(pkg string) (progress func(string), done func()) {
	var mu sync.Mutex
	returned := false
	progress = func(line string) {
		mu.Lock()
		defer mu.Unlock()
		if returned {
			return
		}
		a.reporter.TaskLine(pkg, tui.StreamStdout, line, true)
	}
	done = func() {
		mu.Lock()
		returned = true
		mu.Unlock()
	}
	return progress, done
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
