package autoupdate

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/logging"
	"github.com/obentoo/bentoolkit/internal/common/secrets"
)

// errUnsafeAgentDir is wrapped by agentPermissionArgs when an agent's own
// directory cannot be written into a permission rule safely (S051-R2.8).
var errUnsafeAgentDir = errors.New("not an absolute path made only of [A-Za-z0-9._+@/-]")

// errUnsafeHost is returned by webFetchDomainRule for a value that is not a
// lowercase DNS name (S051-R3.6).
var errUnsafeHost = errors.New("not a lowercase DNS name")

// errIPLiteralHost is the S051-R3.9 refusal. It wraps errUnsafeHost, because an
// IPv4 literal is a host no rule may carry, and says why in its own words: the
// value passed the DNS shape and was refused for what a fetcher would read it as.
var errIPLiteralHost = fmt.Errorf("an IPv4 literal, which would reach loopback, the internal network or a metadata endpoint: %w", errUnsafeHost)

// agentDirChars is the whole alphabet an agent directory may use. A space or a
// comma would be re-split by the CLI into a second rule, a parenthesis would end
// the rule early, and a glob character would widen it (S051-R2.8).
var agentDirChars = regexp.MustCompile(`^[A-Za-z0-9._+@/-]+$`)

// dnsHost is the only host shape a WebFetch rule may carry (S051-R3.6): at
// least two lowercase labels, each alphanumeric at both ends. It rejects `*`
// (which would widen the rule) and `)`, `,` and spaces (which would forge a
// second one), because the hosts come from packages.toml and pkgdev output —
// both contributor- or upstream-controlled.
var dnsHost = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// agentFixedHosts are reachable by every agent that holds WebFetch: GitHub is
// where most upstream tarballs and release listings live (S051-R3.3).
var agentFixedHosts = []string{"github.com", "codeload.github.com", "objects.githubusercontent.com"}

// agentPinnedSettings is the inline --settings document every agent runs
// under (S051-R4.2). blockReadsOutsideWorkingDirectories is what actually
// removes `cat`: Claude Code runs its built-in read-only Bash commands (cat, ls,
// head, grep, find, ...) without approval in every permission mode, so dropping
// the allow rule that named cat would, alone, leave cat working.
// disableBypassPermissionsMode refuses a later switch to bypassPermissions.
//
// It is a fixed literal, not a document assembled from values: nothing is
// spliced into it, so there is nothing to escape, and the tests parse it as
// JSON and pin both settings.
const agentPinnedSettings = `{"permissions":{"blockReadsOutsideWorkingDirectories":true,"disableBypassPermissionsMode":"disable"}}`

// agentPermissions describes what one `claude` agent may do.
type agentPermissions struct {
	// agent names the agent in errors ("manifest fixer", "bump reviewer").
	agent string
	// dir is the agent's own absolute directory: the only place its Read, Edit
	// and Write reach. It may be empty only when tools holds none of the three.
	dir string
	// tools lists the built-in tools the agent holds. An entry is a bare name
	// ("Read", "WebFetch") or a name with one specifier ("Bash(pkgdev *)"); the
	// name goes to --tools, and a specifier entry is also granted verbatim as an
	// allow rule. A bare Bash is granted no allow rule at all.
	tools []string
	// hosts are the WebFetch domains, each already passed through
	// upstreamHosts. They are ignored unless tools holds WebFetch.
	hosts []string
}

// agentPermissionArgs turns p into the argv block every spawner appends:
// --tools, --allowedTools, --disallowedTools, --permission-mode dontAsk,
// --setting-sources "", the pinned --settings and --strict-mcp-config.
//
// # The argv form, measured against claude 2.1.281 on 2026-09-23
//
// --tools, --allowedTools and --disallowedTools are VARIADIC: each swallows every
// following argv element that does not start with "-". So every rule is its own
// element (S051-R2.7) — `Bash(pkgdev *)` as one element was granted and ran
// pkgdev, its space notwithstanding — and every list here is followed by another
// flag, never by a positional value. The block ends with a boolean flag, so a
// caller may append anything after it. `--setting-sources ""` is accepted and
// loads no user, project or local settings (S051-R4.1); --strict-mcp-config
// additionally drops the MCP servers and claude.ai connectors the operator's
// account would otherwise hand the agent (the probe listed Google Drive tools
// in an agent spawned with `--tools Read,Bash`). A refused tool is recorded in
// the result envelope's permission_denials as {tool_name, tool_use_id,
// tool_input}.
//
// # The rule form
//
// Read is granted as `Read(//<dir>/**)`. Edit AND Write are granted as one
// `Edit(//<dir>/**)`, because Claude Code never consults a Write(path) rule —
// Edit rules apply to every built-in tool that edits files. The extra leading
// slash is deliberate: `//` anchors at the filesystem root, while a single `/`
// would anchor at the settings source (S051-R2.3).
//
// Every secrets.Paths() entry is denied as `Read(//<path>)`, and as
// `Edit(//<path>)` when the agent holds Edit or Write (S051-R2.5). The paths are
// read on every call, because secrets.Paths() follows HOME and
// XDG_CONFIG_HOME. A Read-only agent's argv therefore names no Edit at all.
func agentPermissionArgs(p agentPermissions) ([]string, error) {
	var names []string
	var allow []string
	holdsRead, holdsEdit, holdsWebFetch := false, false, false
	for _, entry := range p.tools {
		name, spec, scoped := strings.Cut(entry, "(")
		names = append(names, name)
		switch name {
		case "Read":
			holdsRead = true
		case "Edit", "Write":
			holdsEdit = true
		case "WebFetch":
			holdsWebFetch = true
		}
		if scoped && spec != "" {
			allow = append(allow, entry)
		}
	}

	if p.dir != "" || holdsRead || holdsEdit {
		if err := checkAgentDir(p.dir); err != nil {
			return nil, fmt.Errorf("agent permissions for %s: own directory %q: %w", p.agent, p.dir, err)
		}
	}
	if holdsRead {
		allow = append(allow, "Read(/"+p.dir+"/**)")
	}
	if holdsEdit {
		allow = append(allow, "Edit(/"+p.dir+"/**)")
	}
	if holdsWebFetch {
		for _, h := range p.hosts {
			rule, err := webFetchDomainRule(h)
			if err != nil {
				return nil, fmt.Errorf("agent permissions for %s: %w", p.agent, err)
			}
			allow = append(allow, rule)
		}
	}

	return agentArgv(names, allow, holdsEdit), nil
}

// agentArgv renders the argv block from tool names and allow rules that are
// already safe, adding the secrets deny rules and the pinned flags. It cannot
// fail, which is what lets the text client — no tools, no directory, nothing to
// validate — build its argv without an error path (S051-R2.1).
func agentArgv(names, allow []string, holdsEdit bool) []string {
	var deny []string
	for _, path := range secrets.Paths() {
		deny = append(deny, "Read(/"+path+")")
		if holdsEdit {
			deny = append(deny, "Edit(/"+path+")")
		}
	}

	args := []string{"--tools", strings.Join(names, ",")}
	if len(allow) > 0 {
		args = append(append(args, "--allowedTools"), allow...)
	}
	if len(deny) > 0 {
		args = append(append(args, "--disallowedTools"), deny...)
	}
	return append(args,
		"--permission-mode", "dontAsk",
		"--setting-sources", "",
		"--settings", agentPinnedSettings,
		"--strict-mcp-config",
	)
}

// checkAgentDir enforces S051-R2.8 on an agent's own directory.
func checkAgentDir(dir string) error {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || !agentDirChars.MatchString(dir) {
		return errUnsafeAgentDir
	}
	return nil
}

// webFetchDomainRule returns the WebFetch allow rule for host, or an error when
// host is not a lowercase DNS name (S051-R3.6, S051-R8.1). It never lowercases
// or trims: normalising is upstreamHosts' job, and a rule builder that repaired
// its input would be the place a hostile value got repaired into a valid one.
func webFetchDomainRule(host string) (string, error) {
	if err := checkWebFetchHost(host); err != nil {
		return "", fmt.Errorf("WebFetch host %q: %w", host, err)
	}
	return "WebFetch(domain:" + host + ")", nil
}

// checkWebFetchHost is the one test every WebFetch host passes, wherever it is
// read: a lowercase DNS name (S051-R3.6) that is not an IPv4 literal
// (S051-R3.9). It returns errUnsafeHost or errIPLiteralHost, or nil.
func checkWebFetchHost(host string) error {
	if !dnsHost.MatchString(host) {
		return errUnsafeHost
	}
	if isIPv4Literal(host) {
		return errIPLiteralHost
	}
	return nil
}

// isIPv4Literal reports whether host's last label is a NUMBER in the WHATWG URL
// parser's "ends in a number" sense: all ASCII digits, or "0x"/"0X" followed
// only by hex digits (S051-R3.9). A host like that is parsed as an IPv4 address
// by a WHATWG-based fetcher — 127.1 and 127.0.0.0x1 both reach 127.0.0.1 — while
// no real top-level domain is a number, so no DNS name is lost; dl.0xide, whose
// last label is not hex, stays a name.
func isIPv4Literal(host string) bool {
	last := host[strings.LastIndexByte(host, '.')+1:]
	if last == "" {
		return false
	}
	digits, alphabet := last, "0123456789"
	if len(last) >= 2 && last[0] == '0' && (last[1] == 'x' || last[1] == 'X') {
		digits, alphabet = last[2:], "0123456789abcdefABCDEF"
	}
	for i := 0; i < len(digits); i++ {
		if !strings.ContainsRune(alphabet, rune(digits[i])) {
			return false
		}
	}
	return true
}

// upstreamURLPattern finds http(s) URLs in free text such as pkgdev's error
// output (S051-R3.5). The excluded characters end a URL where prose or quoting
// would.
var upstreamURLPattern = regexp.MustCompile(`https?://[^\s"'<>()]+`)

// upstreamURLsIn returns every http(s) URL in text, in order.
func upstreamURLsIn(text string) []string {
	return upstreamURLPattern.FindAllString(text, -1)
}

// upstreamHosts returns the WebFetch host set for pkg: the lowercased host of
// every http or https URL in urls that is a DNS name, plus agentFixedHosts,
// de-duplicated and sorted so the argv is stable (S051-R3.3).
//
// A URL that does not parse, uses another scheme, or whose host is not a
// lowercase DNS name after lowercasing, or is an IPv4 literal (S051-R3.9), is left out with one warning to log naming pkg
// and the rejected value in attributes (S051-R3.6); nil log discards it. A trailing dot is not
// trimmed: `example.com.` is rejected like any other non-DNS spelling, so the
// host a rule names is always the one the operator can read in packages.toml.
func upstreamHosts(log *slog.Logger, pkg string, urls ...string) []string {
	log = logging.OrDiscard(log)
	set := make(map[string]struct{}, len(urls)+len(agentFixedHosts))
	for _, h := range agentFixedHosts {
		set[h] = struct{}{}
	}
	for _, raw := range urls {
		u, err := url.Parse(raw)
		if err != nil {
			log.Warn("agent hosts: rejected URL", "package", pkg, "url", raw, "err", err)
			continue
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			log.Warn("agent hosts: rejected URL: scheme is not http or https", "package", pkg, "url", raw, "scheme", u.Scheme)
			continue
		}
		host := strings.ToLower(u.Hostname())
		if err := checkWebFetchHost(host); err != nil {
			log.Warn("agent hosts: rejected host", "package", pkg, "host", host, "err", err)
			continue
		}
		set[host] = struct{}{}
	}
	hosts := make([]string, 0, len(set))
	for h := range set {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	return hosts
}

// RefusedToolsNote renders the tools an agent was refused as the suffix a
// "still failed" message carries — ` (agent was refused: WebFetch(host), Bash)` —
// or "" when none were, so a message about a fix that simply did not work names
// no tool (S051-R5.2).
func RefusedToolsNote(denied []string) string {
	if len(denied) == 0 {
		return ""
	}
	return " (agent was refused: " + strings.Join(denied, ", ") + ")"
}
