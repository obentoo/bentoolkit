package autoupdate

import "github.com/obentoo/bentoolkit/internal/autoupdate/fixer"

// Authored (Red-phase) test for story 014 — RegistryFixer / ClaudeCodeRegistryFixer.
//
// This file is an INDEPENDENT contract spec for sub-tasks 1.2 and 1.3. It reuses
// the package-private exec seam (fixerSeam) and the scripted-CLI envelope pattern
// already established in manifest_fixer_test.go / claude_code_test.go (same
// package, white-box). It references symbols that do not exist yet
// (RegistryFixer, RegistryFixRequest, RegistryFixResult, NewClaudeCodeRegistryFixer,
// WithRegistryFixerExecCommand, WithRegistryFixerTimeout, registryFixAllowedTools),
// so until Task 1 lands the package fails to COMPILE — that compile failure is the
// expected Red signal for these sub-tasks.

// compile-time guard: ClaudeCodeRegistryFixer satisfies RegistryFixer.
var _ fixer.RegistryFixer = (*fixer.ClaudeCodeRegistryFixer)(nil)
