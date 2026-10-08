// Command bentoo is the command-line tool for Bentoo overlay maintainers: it
// manages the overlay's git workflow, checks and applies upstream version
// updates, fetches gated distfiles, authors notices, and drives btrfs
// snapshots.
//
// This package is the composition layer. Each command parses its flags, builds
// the services it needs from internal/ (wired through the deps struct), runs
// them under the process-wide signal context, and renders the result.
package main
