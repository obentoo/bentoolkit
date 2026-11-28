package validate

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// The host sandbox cuts the network (sandbox.go), and that is policy, not an
// accident: docs/autoupdate.md promises that code a bump brought in cannot
// reach the operator's network, and a sandboxed pass is labelled isolated on
// that promise. An ebuild that declares RESTRICT="network-sandbox" says it
// needs the network to build (sci-ml/llama-swap runs `go mod download` in
// src_unpack), so on the host backend its build can only die offline. The
// 2026-11-28 run built it anyway, read the unpack death as a fault of the
// environment, and the bump waited for ever on a cause no retry would remove.
// Declining it here, naming the restriction, says what actually stands in the
// way and spends no build on it.

// restrictAssignment matches an ebuild's RESTRICT assignment, `=` or `+=`,
// quoted with either quote and possibly spread over several lines.
var restrictAssignment = regexp.MustCompile(`(?m)^[ \t]*RESTRICT\+?=(?:"([^"]*)"|'([^']*)'|([^\s"'#]+))`)

// declaresNetworkSandboxRestrict reports whether the ebuild text assigns
// network-sandbox to RESTRICT. A conditional entry (`test? ( network-sandbox
// )`) counts too: the gate cannot tell which USE the build would take, and a
// refusal that names the restriction costs less than an offline build that
// dies for a reason no message explains.
func declaresNetworkSandboxRestrict(ebuild string) bool {
	for _, m := range restrictAssignment.FindAllStringSubmatch(ebuild, -1) {
		value := m[1] + m[2] + m[3]
		for _, token := range strings.Fields(value) {
			if token == "network-sandbox" {
				return true
			}
		}
	}
	return false
}

// ebuildRestrictsNetworkSandbox reads the staged ebuild at path. An unreadable
// file answers false: the build that follows fails on it in its own words.
func ebuildRestrictsNetworkSandbox(path string) bool {
	raw, err := os.ReadFile(path) //nolint:gosec // G304: the staged candidate's own ebuild, named from a validated atom
	if err != nil {
		return false
	}
	return declaresNetworkSandboxRestrict(string(raw))
}

// networkSandboxRefusedReason words a build declined because the ebuild needs
// the network the host sandbox withholds.
func networkSandboxRefusedReason(pv string) string {
	return fmt.Sprintf("the build of %s was not run: the ebuild declares RESTRICT=\"network-sandbox\" (it needs the network to build), "+
		"and the host backend's sandbox runs every build offline by policy; "+
		"prove it with --build-backend container or vm, or remove the network need (for a Go package, a vendor tarball)", pv)
}
