package llm

// Authored for story 092 (autoupdate-apply-observable), sub-task 3.1: the
// stream-json parser (R4.2-R4.6, Q5, Q9). The fixture under testdata replays
// the shape `claude -p --output-format stream-json --verbose` prints: one JSON
// object per line — system init, assistant messages (text and tool_use
// blocks), user tool_result echoes, unknown event kinds, then one result.

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

const s092Fixture = "testdata/stream_manifest_fix_s092.jsonl"

func s092ReadFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(s092Fixture)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return b
}

// s092ResultLine returns the fixture's final result line.
func s092ResultLine(t *testing.T, fixture []byte) []byte {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(fixture), []byte("\n"))
	last := lines[len(lines)-1]
	if !bytes.Contains(last, []byte(`"type":"result"`)) {
		t.Fatalf("fixture does not end with a result event")
	}
	return last
}

func s092Parse(t *testing.T, stream []byte) (ClaudeCodeEnvelope, []StreamEvent, error) {
	t.Helper()
	var events []StreamEvent
	env, err := ParseStream(bytes.NewReader(stream), func(e StreamEvent) { events = append(events, e) })
	return env, events, err
}

func s092Kind(events []StreamEvent, k StreamEventKind) []StreamEvent {
	var out []StreamEvent
	for _, e := range events {
		if e.Kind == k {
			out = append(out, e)
		}
	}
	return out
}

// R4.2: one ToolCall per tool_use block, in stream order, with the target
// derived per tool: the file path, the command's first word, the URL host.
// Hostile halves: two identical calls (toolu_03 and toolu_05) are TWO events,
// never collapsed; the user tool_result echoes and the system init's tool list
// add NO event, so one call is never split into two.
func TestParseStreamToolCalls(t *testing.T) {
	_, events, err := s092Parse(t, s092ReadFixture(t))
	if err != nil {
		t.Fatalf("ParseStream: %v", err)
	}
	type call struct{ tool, target string }
	want := []call{
		{"Read", "/var/tmp/staging/dev-go/govulncheck/govulncheck-1.1.4.ebuild"},
		{"WebFetch", "github.com"},
		{"Bash", "pkgdev"},
		{"Edit", "/var/tmp/staging/dev-go/govulncheck/govulncheck-1.1.4.ebuild"},
		{"Bash", "pkgdev"},
	}
	var got []call
	for _, e := range s092Kind(events, ToolCall) {
		got = append(got, call{e.Tool, e.Target})
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tool calls:\n got %v\nwant %v", got, want)
	}
}

// R4.4: the model's text never becomes an event — not even prose that names a
// tool and a path, and not the text block sharing a message with a tool_use.
func TestParseStreamIgnoresModelText(t *testing.T) {
	_, events, err := s092Parse(t, s092ReadFixture(t))
	if err != nil {
		t.Fatalf("ParseStream: %v", err)
	}
	for _, e := range events {
		blob := e.Tool + " " + e.Target
		for _, leak := range []string{"MODEL-PROSE-SENTINEL", "/etc/shadow", "rm -rf", "Let me look"} {
			if strings.Contains(blob, leak) {
				t.Errorf("an event carries model text %q: %+v", leak, e)
			}
		}
	}
}

// R4.3 + R6.4: the result's permission_denials become PermissionDenied events
// named by tool (plus the WebFetch host), never by input. Hostile half: two
// WebFetch refusals to different hosts stay two distinguishable events.
func TestParseStreamPermissionDenials(t *testing.T) {
	_, events, err := s092Parse(t, s092ReadFixture(t))
	if err != nil {
		t.Fatalf("ParseStream: %v", err)
	}
	denied := s092Kind(events, PermissionDenied)
	if len(denied) == 0 {
		t.Fatalf("no PermissionDenied event for three refusals")
	}
	hostA, hostB, bash := -1, -1, false
	for i, e := range denied {
		blob := e.Tool + " " + e.Target
		if strings.Contains(blob, "SENTINEL-INPUT-092") {
			t.Errorf("a denial event echoes the refused input: %+v", e)
		}
		if strings.Contains(blob, "a.example.org") {
			hostA = i
		}
		if strings.Contains(blob, "b.example.org") {
			hostB = i
		}
		if strings.HasPrefix(e.Tool, "Bash") {
			bash = true
		}
	}
	if hostA < 0 || hostB < 0 || hostA == hostB {
		t.Errorf("the two WebFetch refusals are not two distinguishable events: %+v", denied)
	}
	if !bash {
		t.Errorf("the Bash refusal produced no event: %+v", denied)
	}
}

// R4.5: the envelope equals what today's single-envelope parse yields for the
// same result object: result, subtype, cost, permission denials.
func TestParseStreamEnvelopeMatchesSingleJSON(t *testing.T) {
	fixture := s092ReadFixture(t)
	var want ClaudeCodeEnvelope
	if err := json.Unmarshal(s092ResultLine(t, fixture), &want); err != nil {
		t.Fatalf("unmarshal result line: %v", err)
	}
	got, _, err := s092Parse(t, fixture)
	if err != nil {
		t.Fatalf("ParseStream: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("envelope:\n got %+v\nwant %+v", got, want)
	}
	if got.Result != "Rewrote SRC_URI to the GitHub release tarball" || got.TotalCostUSD != 0.0423 {
		t.Errorf("result/cost not carried: %+v", got)
	}
	if labels := RefusedToolLabels(got.PermissionDenials); len(labels) != 3 {
		t.Errorf("refused labels = %v, want 3 (two WebFetch hosts and Bash)", labels)
	}
}

// R4.5 converse: a stream holding only the result line (today's json output,
// and what existing test doubles print) parses to the same envelope.
func TestParseStreamSingleEnvelopeLine(t *testing.T) {
	line := s092ResultLine(t, s092ReadFixture(t))
	var want ClaudeCodeEnvelope
	if err := json.Unmarshal(line, &want); err != nil {
		t.Fatal(err)
	}
	got, _, err := s092Parse(t, line) // no trailing newline
	if err != nil {
		t.Fatalf("ParseStream: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("envelope:\n got %+v\nwant %+v", got, want)
	}
}

// Q5: malformed lines, unknown kinds, a 1 MiB tool_result echo and a line past
// any sane scanner bound are skipped; the result after them still parses.
func TestParseStreamSkipsMalformedAndOversizedLines(t *testing.T) {
	fixture := s092ReadFixture(t)
	result := s092ResultLine(t, fixture)
	big := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"` + strings.Repeat("a", 1<<20) + `"}]}}`
	huge := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"` + strings.Repeat("b", 5<<20) + `"}]}}`
	stream := strings.Join([]string{
		"not json at all",
		`{"type":"assistant","message":`,
		`{"type":"assistant","message":{"content":"a string, not blocks"}}`,
		`{"type":"assistant","message":null}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/before/big"}}]}}`,
		big,
		huge,
		"",
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/after/huge"}}]}}`,
		string(result),
	}, "\n")
	env, events, err := s092Parse(t, []byte(stream))
	if err != nil {
		t.Fatalf("ParseStream: %v", err)
	}
	if env.Result != "Rewrote SRC_URI to the GitHub release tarball" {
		t.Errorf("the result after the skipped lines was lost: %+v", env)
	}
	var targets []string
	for _, e := range s092Kind(events, ToolCall) {
		targets = append(targets, e.Target)
	}
	if !reflect.DeepEqual(targets, []string{"/before/big", "/after/huge"}) {
		t.Errorf("tool calls around the skipped lines = %v", targets)
	}
}

// R4.6: no result event is a failure, and the same failure a missing envelope
// (an empty stream) produces.
func TestParseStreamWithoutResultFails(t *testing.T) {
	fixture := s092ReadFixture(t)
	noResult := bytes.TrimSuffix(bytes.TrimSpace(fixture), s092ResultLine(t, fixture))
	env, events, err := s092Parse(t, noResult)
	if err == nil {
		t.Fatalf("a stream without a result event parsed as success: %+v", env)
	}
	if !reflect.DeepEqual(env, ClaudeCodeEnvelope{}) {
		t.Errorf("a failed parse returned a non-zero envelope: %+v", env)
	}
	if len(s092Kind(events, ToolCall)) != 5 {
		t.Errorf("the tool calls before the missing result were not reported: %d", len(events))
	}
	_, _, emptyErr := s092Parse(t, nil)
	if emptyErr == nil {
		t.Fatalf("an empty stream parsed as success")
	}
	if err.Error() != emptyErr.Error() {
		t.Errorf("missing result and empty stream fail differently:\n%v\n%v", err, emptyErr)
	}
}

// R4.2 live: an event reaches the callback while the stream is still open,
// not after EOF — the package row must move while the agent runs.
func TestParseStreamEmitsBeforeEOF(t *testing.T) {
	pr, pw := io.Pipe()
	got := make(chan StreamEvent, 8)
	done := make(chan error, 1)
	go func() {
		_, err := ParseStream(pr, func(e StreamEvent) { got <- e })
		done <- err
	}()
	if _, err := io.WriteString(pw, `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/live"}}]}}`+"\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-got:
		if e.Kind != ToolCall || e.Target != "/live" {
			t.Errorf("first event = %+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event before EOF: the parser buffers the whole stream")
	}
	_, _ = io.WriteString(pw, `{"type":"result","subtype":"success","is_error":false,"result":"ok"}`+"\n")
	_ = pw.Close()
	if err := <-done; err != nil {
		t.Errorf("ParseStream: %v", err)
	}
}

// FuzzStreamEvent (Q9): no input panics the parser, and an arbitrary line
// followed by a valid result never hides that result.
func FuzzStreamEvent(f *testing.F) {
	if b, err := os.ReadFile(s092Fixture); err == nil {
		for _, l := range bytes.Split(b, []byte("\n")) {
			f.Add(l)
		}
	}
	for _, s := range []string{"", "{", "null", "[]", `{"type":"assistant"}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":""}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"WebFetch","input":{"url":"::not a url"}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":null}]}}`,
		`{"type":"result","permission_denials":"x"}`} {
		f.Add([]byte(s))
	}
	const tail = `{"type":"result","subtype":"success","is_error":false,"result":"fuzz-ok"}`
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = ParseStream(bytes.NewReader(data), func(StreamEvent) {})
		if bytes.Contains(data, []byte("result")) {
			return
		}
		stream := append(append(append([]byte{}, data...), '\n'), tail...)
		env, err := ParseStream(bytes.NewReader(stream), func(StreamEvent) {})
		if err != nil || env.Result != "fuzz-ok" {
			t.Errorf("a preceding line hid the result: env=%+v err=%v", env, err)
		}
	})
}
