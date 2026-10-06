package autoupdate

import "os/exec"

// lookPath answers "is this tool installed" for the core: the applier's
// post-fix pkgcheck QA gate uses it, and core tests swap it. The fixer and llm
// packages keep their own copies for the claude CLI, so every package's tests
// swap their own variable.
var lookPath = exec.LookPath
