package autoupdate

// Authored for story 051 (llm-agent-least-privilege), sub-tasks 1.1 and 1.2 —
// the agent environment is a fixed allow-list (S051-R1.1..R1.8, S051-R9.2).
//
// Every assertion here reads the EXACT slice a spawner assigned to cmd.Env,
// captured through the spawner's own exec seam. Nothing reads the first or the
// last match of a name: a name that appears twice is itself a failure (R1.8), so
// which duplicate os/exec would pick is never what makes a test pass.
//
// The file also holds the helpers the other story-051 test files share
// (agentSeam, the argv readers, the hostile parent environment).

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// okEnvelope is a minimal successful `claude --output-format json` envelope.
const okEnvelope = `{"type":"result","subtype":"success","is_error":false,"result":"ok"}`

// agentSpawn records what a spawner handed to its exec seam: the argv, and the
// *exec.Cmd itself so the fields the spawner sets AFTER the factory returns
// (Env, Dir) can be read once the call is over.
type agentSpawn struct {
	mu    sync.Mutex
	count int
	name  string
	args  []string
	cmds  []*exec.Cmd
}

func (s *agentSpawn) spawns() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

// agentSeam returns an exec-seam factory whose child runs script under /bin/sh.
// The shell is named by absolute path so a test that empties the parent
// environment (PATH included) can still start the child.
func agentSeam(script string) (func(ctx context.Context, name string, arg ...string) *exec.Cmd, *agentSpawn) {
	spy := &agentSpawn{}
	factory := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script)
		spy.mu.Lock()
		spy.count++
		spy.name = name
		spy.args = append([]string(nil), arg...)
		spy.cmds = append(spy.cmds, cmd)
		spy.mu.Unlock()
		return cmd
	}
	return factory, spy
}

// printEnvelopeScript prints body verbatim on stdout and exits 0.
func printEnvelopeScript(body string) string {
	return "printf '%s' '" + body + "'"
}

// ---------------------------------------------------------------------------
// argv readers shared by the story-051 permission tests
// ---------------------------------------------------------------------------

// flagValues returns every value given to flag: for each occurrence, the
// elements that follow it up to the next element starting with "-". It reads a
// variadic list (`--allowedTools A B C`) and a repeated flag
// (`--allowedTools A --allowedTools B`) the same way, so the tests do not pin
// which of the two forms the implementation chose.
func flagValues(args []string, flag string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		if args[i] != flag {
			continue
		}
		for j := i + 1; j < len(args) && !strings.HasPrefix(args[j], "-"); j++ {
			out = append(out, args[j])
		}
	}
	return out
}

// ruleList returns the permission rules of one kind ("allow" or "deny"),
// wherever the implementation put them: the --allowedTools/--disallowedTools
// values, or the inline settings' permissions.allow/deny arrays. Empty values
// (the text client's `--allowedTools ""`) are dropped.
func ruleList(t *testing.T, who string, args []string, kind string) []string {
	t.Helper()
	flag := "--allowedTools"
	if kind == "deny" {
		flag = "--disallowedTools"
	}
	var out []string
	for _, v := range flagValues(args, flag) {
		if v != "" {
			out = append(out, v)
		}
	}
	if vals := flagValues(args, "--settings"); len(vals) == 1 {
		var doc struct {
			Permissions struct {
				Allow []string `json:"allow"`
				Deny  []string `json:"deny"`
			} `json:"permissions"`
		}
		if json.Unmarshal([]byte(vals[0]), &doc) == nil {
			if kind == "deny" {
				out = append(out, doc.Permissions.Deny...)
			} else {
				out = append(out, doc.Permissions.Allow...)
			}
		}
	}
	return out
}

func containsRule(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func countOf(list []string, want string) int {
	n := 0
	for _, v := range list {
		if v == want {
			n++
		}
	}
	return n
}

// isolateSecretsPaths points HOME and XDG_CONFIG_HOME at a private directory so
// secrets.Paths() is deterministic and never names the operator's files.
func isolateSecretsPaths(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	return home
}
