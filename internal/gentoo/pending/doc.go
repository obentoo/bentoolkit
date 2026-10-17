// Package pending asks Portage what is waiting on this host: the packages an
// `emerge --pretend --update --deep --newuse @world` would merge, and the
// security advisories `glsa-check --list affected` reports as applicable.
//
// Run starts one such command without a shell, behind optional priority
// wrappers (nice, ionice), in a process group of its own that is killed as a
// whole on timeout or cancel, with its output capped. ParseWorld and
// ParseGLSA read that output, which is treated as untrusted text: lines that
// are not recognised are skipped, never guessed at.
//
// The package returns errors and never logs; the caller decides what a
// failure means for its monitor.
package pending
