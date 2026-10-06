package autoupdate

// Authored for story 051 (llm-agent-least-privilege), sub-task 7.1 — a refused
// WebFetch to an IPv4-literal host keeps its host in the refusal label
// (S051-R5.4), while a host that is not DNS-shaped still yields the bare
// `WebFetch` label (S051-R5.1's injection guard).
//
// A label is text shown to the operator, not a grant: an IPv4 literal is made of
// digits, dots and hex only, so it cannot carry injected text, and it is exactly
// the detail an operator needs when an agent reached for a metadata endpoint.

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/llm"
)

// ipLiteralDeniedEnvelope is an is_error envelope whose two WebFetch refusals
// target IPv4-literal hosts: the cloud metadata endpoint (with a path, a query
// sentinel and a prompt sentinel) and the WHATWG hex spelling of loopback.
const ipLiteralDeniedEnvelope = `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"stopped","permission_denials":[` +
	`{"tool_name":"WebFetch","tool_use_id":"toolu_71","tool_input":{"url":"https://169.254.169.254/latest/meta-data/iam?token=SENTINEL-QUERY-071","prompt":"SENTINEL-PROMPT-071"}},` +
	`{"tool_name":"WebFetch","tool_use_id":"toolu_72","tool_input":{"url":"http://127.0.0.0x1/x/SENTINEL-PATH-071"}}]}`

// webFetchDenial builds one WebFetch refusal whose tool_input carries rawURL
// JSON-encoded, so no spelling below can break the envelope.
func webFetchDenial(t *testing.T, rawURL string) llm.ClaudePermissionDenial {
	t.Helper()
	in, err := json.Marshal(map[string]string{"url": rawURL, "prompt": "SENTINEL-PROMPT-072"})
	if err != nil {
		t.Fatalf("marshal tool_input for %q: %v", rawURL, err)
	}
	return llm.ClaudePermissionDenial{ToolName: "WebFetch", ToolInput: in}
}

// deniedEnvelopeFor renders denials as an is_error envelope a scripted child
// can print. It fails the test if the envelope carries a single quote, which
// printEnvelopeScript cannot pass through the shell.
func deniedEnvelopeFor(t *testing.T, denials []llm.ClaudePermissionDenial) string {
	t.Helper()
	body, err := json.Marshal(struct {
		Type              string                       `json:"type"`
		Subtype           string                       `json:"subtype"`
		IsError           bool                         `json:"is_error"`
		Result            string                       `json:"result"`
		PermissionDenials []llm.ClaudePermissionDenial `json:"permission_denials"`
	}{"result", "error_during_execution", true, "stopped", denials})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	if strings.ContainsRune(string(body), '\'') {
		t.Fatalf("envelope carries a single quote the shell script cannot print: %s", body)
	}
	return string(body)
}

// TestRefusedToolLabels_NameIPLiteralHosts is R5.4: a refused WebFetch to an
// IPv4-literal host is labelled `WebFetch(<host>)` — in refusedToolLabels and in
// every fixer's error — while the URL's path, query and the fetch prompt never
// appear (R5.1). The two literals denote different hosts and must stay two
// labels; two spellings of ONE literal (a port, an upper-case 0X) must fold into
// one label, not split.
func TestRefusedToolLabels_NameIPLiteralHosts(t *testing.T) {
	t.Run("labels", func(t *testing.T) {
		var env struct {
			PermissionDenials []llm.ClaudePermissionDenial `json:"permission_denials"`
		}
		if err := json.Unmarshal([]byte(ipLiteralDeniedEnvelope), &env); err != nil {
			t.Fatalf("the test envelope does not parse: %v", err)
		}
		denials := env.PermissionDenials
		denials = append(denials,
			webFetchDenial(t, "http://169.254.169.254:80/latest/user-data"),
			webFetchDenial(t, "HTTP://127.0.0.0X1/other"),
		)
		got := llm.RefusedToolLabels(denials)
		want := []string{"WebFetch(169.254.169.254)", "WebFetch(127.0.0.0x1)"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("refusedToolLabels = %q, want %q — an IPv4-literal host is named, one label per host (R5.4)", got, want)
		}
	})

	for _, tc := range []struct{ name, script string }{
		{"non-zero exit", printEnvelopeScript(ipLiteralDeniedEnvelope) + "; exit 1"},
		{"is_error on zero exit", printEnvelopeScript(ipLiteralDeniedEnvelope)},
	} {
		for who, r := range runDeniedFixers(t, tc.script) {
			if r.err == nil {
				t.Errorf("%s / %s: a refused invocation returned no error", tc.name, who)
				continue
			}
			msg := r.err.Error()
			for _, want := range []string{"WebFetch(169.254.169.254)", "WebFetch(127.0.0.0x1)"} {
				if !strings.Contains(msg, want) {
					t.Errorf("%s / %s: error does not name %q (R5.4): %s", tc.name, who, want, msg)
				}
			}
			for _, leak := range []string{"SENTINEL-QUERY-071", "SENTINEL-PATH-071", "SENTINEL-PROMPT-071", "/latest/meta-data", "token="} {
				if strings.Contains(msg, leak) {
					t.Errorf("%s / %s: error echoes the refused call's input %q (R5.1): %s", tc.name, who, leak, msg)
				}
			}
		}
	}
}

// nonDNSHostURLs are WebFetch urls whose host is NOT a lowercase DNS-shaped
// name once lowercased, or which do not parse at all. None may reach a label:
// the host is agent-chosen text, and only a DNS-shaped spelling (which includes
// an IPv4 literal) is safe to show. Several look like an IP literal on purpose.
var nonDNSHostURLs = []string{
	"https://a(b).example/SENTINEL-PATH-072",
	"http://evil host.example/SENTINEL-PATH-072",
	"https://%41%42.example/SENTINEL-PATH-072",
	"https://169.254.169.254(x)/SENTINEL-PATH-072",
	"https://169.254.169.254./SENTINEL-PATH-072",
	"http://[::1]/SENTINEL-PATH-072",
	"https://evil_host.example/SENTINEL-PATH-072",
	"not a url at all SENTINEL-PATH-072",
}

// TestRefusedToolLabels_KeepBareLabelForNonDNSHosts is the converse of R5.4 —
// R5.1's injection guard: naming IPv4-literal hosts must not open the label to
// every host. A WebFetch refusal whose host fails the DNS-shape check, whose url
// does not parse, or whose url is not a string yields the bare `WebFetch` label
// and no host text. A DNS-name control proves the harness can produce a host
// label at all, so the bare results are not vacuous.
func TestRefusedToolLabels_KeepBareLabelForNonDNSHosts(t *testing.T) {
	if got, want := llm.RefusedToolLabels([]llm.ClaudePermissionDenial{webFetchDenial(t, "https://ok.example.com/x")}), []string{"WebFetch(ok.example.com)"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("control: refusedToolLabels = %q, want %q", got, want)
	}

	var denials []llm.ClaudePermissionDenial
	for _, raw := range nonDNSHostURLs {
		d := webFetchDenial(t, raw)
		denials = append(denials, d)
		if got := llm.RefusedToolLabels([]llm.ClaudePermissionDenial{d}); !reflect.DeepEqual(got, []string{"WebFetch"}) {
			t.Errorf("url %q: refusedToolLabels = %q, want the bare [\"WebFetch\"] (R5.1)", raw, got)
		}
	}
	notString := llm.ClaudePermissionDenial{ToolName: "WebFetch", ToolInput: json.RawMessage(`{"url":42}`)}
	denials = append(denials, notString)
	if got := llm.RefusedToolLabels([]llm.ClaudePermissionDenial{notString}); !reflect.DeepEqual(got, []string{"WebFetch"}) {
		t.Errorf("non-string url: refusedToolLabels = %q, want the bare [\"WebFetch\"]", got)
	}

	for who, r := range runDeniedFixers(t, printEnvelopeScript(deniedEnvelopeFor(t, denials))+"; exit 1") {
		if r.err == nil {
			t.Errorf("%s: a refused invocation returned no error", who)
			continue
		}
		msg := r.err.Error()
		if !strings.Contains(msg, "WebFetch") {
			t.Errorf("%s: the error names no refused tool, so the checks below would pass vacuously: %s", who, msg)
		}
		for _, leak := range []string{"WebFetch(", "SENTINEL-PATH-072", "SENTINEL-PROMPT-072", "a(b)", "evil host", "evil_host", "%41", "::1", "169.254.169.254"} {
			if strings.Contains(msg, leak) {
				t.Errorf("%s: the error carries %q from a non-DNS host (R5.1): %s", who, leak, msg)
			}
		}
	}
}
