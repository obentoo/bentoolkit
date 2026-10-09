// Package git runs the git commands the overlay workflow needs (status, add,
// commit, push, pull, fetch) under a context, with network and local
// timeouts, and parses their output. GitExecutor is the seam: GitRunner runs
// the real git, MockGitRunner records calls for tests.
package git
