package llm

// Story 092, sub-task 3.1, tech review cycle 1: each test pins one fix to the
// stream parser that the pre-authored tests did not reach.

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// s092rToolUse builds one assistant line carrying a single tool_use block.
func s092rToolUse(t *testing.T, tool string, input map[string]string) string {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"type": "assistant",
		"message": map[string]any{"content": []any{
			map[string]any{"type": "tool_use", "name": tool, "input": input},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(line)
}

func s092rJoin(lines ...string) []byte { return []byte(strings.Join(lines, "\n")) }

// A URL host is bounded like every other target: a 300-label-character host
// passes the DNS shape check and must still be cut to the terminal limit.
func TestParseStreamReviewURLHostCapped(t *testing.T) {
	host := strings.Repeat("a", 300) + ".example.org"
	stream := s092rJoin(
		s092rToolUse(t, "WebFetch", map[string]string{"url": "https://" + host + "/x"}),
		`{"type":"result","subtype":"success","result":"ok"}`,
	)
	_, events, err := s092Parse(t, stream)
	if err != nil {
		t.Fatalf("ParseStream: %v", err)
	}
	calls := s092Kind(events, ToolCall)
	if len(calls) != 1 {
		t.Fatalf("tool calls = %+v, want one", calls)
	}
	if n := utf8.RuneCountInString(calls[0].Target); n == 0 || n > maxStreamTargetRunes+1 {
		t.Errorf("URL target is %d runes, want 1..%d: %q", n, maxStreamTargetRunes+1, calls[0].Target)
	}
}

// A leading assignment is skipped whole, quotes and escapes included, so no
// fragment of an inline secret becomes the target. What cannot be delimited
// with certainty yields an empty target, never a guess.
func TestParseStreamReviewCommandWordSkipsWholeAssignments(t *testing.T) {
	cases := []struct{ command, want string }{
		{`API_KEY="sk-ant secretpart" curl https://x`, "curl"},
		{`TOKEN='a b' pkgdev manifest`, "pkgdev"},
		{`A+=sekrit pkgdev`, "pkgdev"},
		{`A="x \" secretpart" pkgdev`, "pkgdev"},
		{`A=x\ secretpart pkgdev`, "pkgdev"},
		{`A=1 B='c d' C+="e f" pkgdev manifest`, "pkgdev"},
		{"A=x\tpkgdev", "pkgdev"},
		{`pkgdev manifest`, "pkgdev"},
		{`A="secretpart pkgdev`, ""},
		{`A='secretpart pkgdev`, ""},
		{`A=$(echo secretpart x) pkgdev`, ""},
		{`T=$[ secretpart ] pkgdev`, ""},
		{`T=$[1 + secretpart] pkgdev`, ""},
		{`A=${X:-secretpart x} pkgdev`, ""},
		{`A="$(echo " secretpart ")" pkgdev`, ""},
		{`A=x\`, ""},
		{`"pkgdev" manifest`, ""},
		{`1A=secretpart pkgdev`, ""},
		{`A=only`, ""},
	}
	for _, tc := range cases {
		stream := s092rJoin(
			s092rToolUse(t, "Bash", map[string]string{"command": tc.command}),
			`{"type":"result","subtype":"success","result":"ok"}`,
		)
		_, events, err := s092Parse(t, stream)
		if err != nil {
			t.Fatalf("%q: ParseStream: %v", tc.command, err)
		}
		calls := s092Kind(events, ToolCall)
		if len(calls) != 1 {
			t.Fatalf("%q: tool calls = %+v, want one", tc.command, calls)
		}
		got := calls[0].Target
		if got != tc.want {
			t.Errorf("%q: target = %q, want %q", tc.command, got, tc.want)
		}
		for _, leak := range []string{"secret", "sekrit", "sk-ant", `"`, "'"} {
			if strings.Contains(got, leak) {
				t.Errorf("%q: target %q carries %q", tc.command, got, leak)
			}
		}
	}
}

const (
	s092rGoodResult   = `{"type":"result","subtype":"success","result":"good","permission_denials":[{"tool_name":"Bash","tool_input":{"command":"x"}}]}`
	s092rBrokenResult = `{"type":"result","subtype":"success","total_cost_usd":"not a number"}`
)

// The last result event decides. A broken final result fails the parse even
// after a good one, and reports none of the good one's refusals; a good final
// result wins over an earlier broken one.
func TestParseStreamReviewLastResultDecides(t *testing.T) {
	env, events, err := s092Parse(t, s092rJoin(s092rGoodResult, s092rBrokenResult))
	if err == nil {
		t.Errorf("good then broken result parsed as success: %+v", env)
	}
	if !reflect.DeepEqual(env, ClaudeCodeEnvelope{}) {
		t.Errorf("good then broken result returned a non-zero envelope: %+v", env)
	}
	if denied := s092Kind(events, PermissionDenied); len(denied) != 0 {
		t.Errorf("a failed parse reported refusals: %+v", denied)
	}

	env, events, err = s092Parse(t, s092rJoin(s092rBrokenResult, s092rGoodResult))
	if err != nil {
		t.Fatalf("broken then good result: %v", err)
	}
	if env.Result != "good" {
		t.Errorf("broken then good result: envelope %+v", env)
	}
	if denied := s092Kind(events, PermissionDenied); len(denied) != 1 || denied[0].Tool != "Bash" {
		t.Errorf("broken then good result: denials %+v, want one Bash", denied)
	}
}

// A DEBUG record names a decode error by kind and position only: neither a
// rejected number literal nor the offending character reaches the log.
func TestParseStreamReviewDebugRecordsCarryNoLineContent(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))
	stream := s092rJoin(
		`{"type":"result","total_cost_usd":98765e4321}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":98765e4321}]}}`,
		`{"type":"result" Zq}`,
		`{"type":"result","subtype":"success","result":"ok"}`,
	)
	if _, err := ParseStream(bytes.NewReader(stream), nil, WithStreamLogger(log)); err != nil {
		t.Fatalf("ParseStream: %v", err)
	}
	out := buf.String()
	if strings.Count(out, "malformed line skipped") != 3 {
		t.Fatalf("want three DEBUG records, got:\n%s", out)
	}
	for _, leak := range []string{"98765", "e4321", "Z", "q}"} {
		if strings.Contains(out, leak) {
			t.Errorf("a DEBUG record carries line content %q:\n%s", leak, out)
		}
	}
	for _, want := range []string{"err_kind=syntax", "err_kind=type", "json_kind=number", "offset="} {
		if !strings.Contains(out, want) {
			t.Errorf("DEBUG records lack %q:\n%s", want, out)
		}
	}
}

// Refusals are reported once, from the deciding envelope, after the stream
// ends: an earlier result's refusals and a repeated refusal add no event.
func TestParseStreamReviewDenialsOnceFromFinalResult(t *testing.T) {
	first := `{"type":"result","subtype":"success","result":"first","permission_denials":[` +
		`{"tool_name":"Bash","tool_input":{"command":"a"}},` +
		`{"tool_name":"WebFetch","tool_input":{"url":"https://a.example.org/"}}]}`
	last := `{"type":"result","subtype":"success","result":"last","permission_denials":[` +
		`{"tool_name":"WebFetch","tool_input":{"url":"https://a.example.org/"}},` +
		`{"tool_name":"Edit","tool_input":{"file_path":"/x"}}]}`
	stream := s092rJoin(
		first,
		s092rToolUse(t, "Read", map[string]string{"file_path": "/between"}),
		last,
	)
	env, events, err := s092Parse(t, stream)
	if err != nil {
		t.Fatalf("ParseStream: %v", err)
	}
	if env.Result != "last" {
		t.Errorf("envelope = %+v, want the last result", env)
	}
	want := []StreamEvent{
		{Kind: ToolCall, Tool: "Read", Target: "/between"},
		{Kind: PermissionDenied, Tool: "WebFetch(a.example.org)"},
		{Kind: PermissionDenied, Tool: "Edit"},
	}
	if !reflect.DeepEqual(events, want) {
		t.Errorf("events:\n got %+v\nwant %+v", events, want)
	}
}

// WithStreamScrub sees each raw field before the terminal cut, so a value the
// cut would split is masked whole; a nil scrub changes nothing.
func TestParseStreamReviewScrubBeforeCut(t *testing.T) {
	const secret = "sk-ant-SECRETVALUE123"
	path := "/" + strings.Repeat("a", 150) + secret
	stream := s092rJoin(
		s092rToolUse(t, "Read", map[string]string{"file_path": path}),
		s092rToolUse(t, "Bash", map[string]string{"command": strings.Repeat("b", 155) + secret + " --x"}),
		s092rToolUse(t, "WebFetch", map[string]string{"url": "https://" + secret + ".example.org/"}),
		`{"type":"result","subtype":"success","result":"ok"}`,
	)
	mask := func(s string) string { return strings.ReplaceAll(s, secret, "***") }
	for name, opt := range map[string]StreamOption{"scrub": WithStreamScrub(mask), "nil": WithStreamScrub(nil)} {
		var events []StreamEvent
		if _, err := ParseStream(bytes.NewReader(stream), func(e StreamEvent) { events = append(events, e) }, opt); err != nil {
			t.Fatalf("%s: ParseStream: %v", name, err)
		}
		if len(events) != 3 {
			t.Fatalf("%s: events = %+v, want 3", name, events)
		}
		if name == "nil" {
			if !strings.Contains(events[0].Target, "sk-ant-SE") {
				t.Errorf("nil scrub changed the target: %q", events[0].Target)
			}
			continue
		}
		for _, e := range events {
			if strings.Contains(e.Target, "sk-ant") || strings.Contains(e.Tool, "sk-ant") {
				t.Errorf("event shows part of the secret: %+v", e)
			}
		}
		if !strings.Contains(events[0].Target, "***") {
			t.Errorf("file path not masked before the cut: %q", events[0].Target)
		}
	}
}
