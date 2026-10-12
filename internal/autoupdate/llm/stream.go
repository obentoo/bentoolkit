package llm

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/obentoo/bentoolkit/internal/common/logging"
)

// StreamLineLimit bounds one line of `claude --output-format stream-json`.
// A longer line (a huge tool_result echo) is skipped, never buffered whole.
const StreamLineLimit = 4 << 20

// maxStreamTargetRunes bounds a StreamEvent's Tool and Target, so one event
// stays one short line in the operator's terminal.
const maxStreamTargetRunes = 160

// StreamEventKind names what a StreamEvent reports.
type StreamEventKind int

const (
	// ToolCall is one tool_use block of an assistant message.
	ToolCall StreamEventKind = iota + 1
	// PermissionDenied is one refused tool, named by its RefusedToolLabels label.
	PermissionDenied
)

// String names the kind for logs and tests.
func (k StreamEventKind) String() string {
	switch k {
	case ToolCall:
		return "tool_call"
	case PermissionDenied:
		return "permission_denied"
	default:
		return fmt.Sprintf("StreamEventKind(%d)", int(k))
	}
}

// StreamEvent is one thing an agent did, as ParseStream reports it live.
//
// Tool and Target are model-controlled text made safe to print: control,
// format and line-separator runes are removed and the length is bounded. For
// a ToolCall, Target is the file path, the command's first word or the URL
// host, and empty when the call carries none of them. For a PermissionDenied,
// Tool is the refusal's label and Target is empty: a denial is never named by
// its input.
type StreamEvent struct {
	Kind   StreamEventKind
	Tool   string
	Target string
}

// StreamOption configures ParseStream.
type StreamOption func(*streamParser)

// WithStreamLogger sets the logger that receives a DEBUG record per skipped
// line. Nil keeps the default, which discards them.
func WithStreamLogger(l *slog.Logger) StreamOption {
	return func(p *streamParser) { p.log = logging.OrDiscard(l) }
}

// WithStreamScrub sets a function applied to a tool's raw name, file path,
// command and URL, and to a refusal's raw label, before they are sanitised and
// cut to the terminal limit. A caller masking secrets passes its masker here:
// masking only the cut text would show the start of a value the cut split.
// Nil keeps the default, which changes nothing.
func WithStreamScrub(scrub func(string) string) StreamOption {
	return func(p *streamParser) {
		if scrub != nil {
			p.scrub = scrub
		}
	}
}

// ParseStream reads the newline-delimited JSON that
// `claude -p --output-format stream-json --verbose` prints and returns the
// envelope carried by its final `result` event, decoded exactly as the single
// `--output-format json` envelope is.
//
// onEvent is called once per tool_use block of an assistant message, in
// stream order, while the stream is still open. Once the stream ends, it is
// called once per refusal label of the returned envelope, so a refusal is
// reported once however many result events repeat it. Model text, tool
// results and unknown event types or fields produce no event. A malformed
// line, or one longer than StreamLineLimit, is skipped with a DEBUG record
// and never ends the parse.
//
// The last result event decides: when it does not decode, the parse fails
// with its decode error even if an earlier result event decoded. A stream
// with no result event fails with the error decoding an empty stdout as the
// envelope produces, so a caller reports it exactly as it reports a missing
// envelope. A read error from r fails the parse as well. Every failure
// returns the zero envelope and reports no refusal.
func ParseStream(r io.Reader, onEvent func(StreamEvent), opts ...StreamOption) (ClaudeCodeEnvelope, error) {
	p := &streamParser{log: logging.OrDiscard(nil), emit: onEvent, scrub: func(s string) string { return s }}
	if p.emit == nil {
		p.emit = func(StreamEvent) {}
	}
	for _, opt := range opts {
		opt(p)
	}
	return p.parse(r)
}

// streamParser holds one ParseStream call's state.
type streamParser struct {
	log  *slog.Logger
	emit func(StreamEvent)
	// scrub is applied to raw model-controlled text before it is sanitised.
	scrub func(string) string
	// lineNo counts physical lines, oversized ones included, for the DEBUG records.
	lineNo int
	// env is the last result event when it decoded; haveEnv says it did.
	env     ClaudeCodeEnvelope
	haveEnv bool
	// resultErr is the decode error of the last result event when it did not.
	resultErr error
}

func (p *streamParser) parse(r io.Reader) (ClaudeCodeEnvelope, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), StreamLineLimit)
	split := &lineSplitter{limit: StreamLineLimit, onSkip: p.skipOversized}
	sc.Split(split.split)
	for sc.Scan() {
		p.lineNo++
		p.handleLine(sc.Bytes())
	}
	if err := sc.Err(); err != nil {
		return ClaudeCodeEnvelope{}, fmt.Errorf("read claude stream at line %d: %w", p.lineNo+1, err)
	}
	if p.haveEnv {
		p.emitDenials()
		return p.env, nil
	}
	if p.resultErr != nil {
		return ClaudeCodeEnvelope{}, p.resultErr
	}
	return ClaudeCodeEnvelope{}, missingEnvelopeError()
}

// missingEnvelopeError is the error a missing envelope has always produced:
// the one decoding an empty stdout as the envelope yields.
func missingEnvelopeError() error {
	var env ClaudeCodeEnvelope
	return json.Unmarshal(nil, &env)
}

func (p *streamParser) skipOversized() {
	p.lineNo++
	p.log.Debug("claude stream: oversized line skipped", "line", p.lineNo, "limit_bytes", StreamLineLimit)
}

// skipMalformed records a skipped line by its number, size and the decode
// error's kind and position only: a decode error's text can quote the line.
func (p *streamParser) skipMalformed(line []byte, err error) {
	args := append([]any{"line", p.lineNo, "bytes", len(line)}, decodeErrorAttrs(err)...)
	p.log.Debug("claude stream: malformed line skipped", args...)
}

// decodeErrorAttrs describes a json decode error without its message, which
// can carry the offending character or a rejected number literal.
func decodeErrorAttrs(err error) []any {
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return []any{"err_kind", "syntax", "offset", syntaxErr.Offset}
	}
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		return []any{
			"err_kind", "type", "offset", typeErr.Offset,
			"field", safeErrorField(typeErr.Field),
			"json_kind", jsonValueKind(typeErr.Value),
			"go_type", typeErr.Type.String(),
		}
	}
	return []any{"err_kind", "other"}
}

// jsonValueKind keeps the kind word of an UnmarshalTypeError's Value and
// drops the literal that follows it for a number.
func jsonValueKind(value string) string {
	kind, _, _ := strings.Cut(value, " ")
	switch kind {
	case "array", "object", "number", "string", "bool":
		return kind
	default:
		return "other"
	}
}

// safeErrorField passes a dotted path of identifier-like names, which is what
// a struct field path is, and replaces anything else.
func safeErrorField(field string) string {
	if len(field) > 64 {
		return "?"
	}
	for _, r := range field {
		if r != '_' && r != '.' && !isASCIILetter(r) && (r < '0' || r > '9') {
			return "?"
		}
	}
	return field
}

// streamLine is the part of every stream event the parser dispatches on.
type streamLine struct {
	Type    string          `json:"type"`
	Message json.RawMessage `json:"message"`
}

func (p *streamParser) handleLine(line []byte) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return
	}
	var ev streamLine
	if err := json.Unmarshal(line, &ev); err != nil {
		p.skipMalformed(line, err)
		return
	}
	switch ev.Type {
	case "assistant":
		p.handleAssistant(line, ev.Message)
	case "result":
		p.handleResult(line)
	}
}

// streamBlock is the part of an assistant content block a tool call is read
// from. Input stays raw so one ill-typed field cannot hide the others.
type streamBlock struct {
	Type  string          `json:"type"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

func (p *streamParser) handleAssistant(line []byte, message json.RawMessage) {
	var msg struct {
		Content []json.RawMessage `json:"content"`
	}
	if len(message) == 0 {
		return
	}
	if err := json.Unmarshal(message, &msg); err != nil {
		p.skipMalformed(line, err)
		return
	}
	for _, raw := range msg.Content {
		var block streamBlock
		if err := json.Unmarshal(raw, &block); err != nil {
			p.skipMalformed(line, err)
			continue
		}
		if block.Type != "tool_use" {
			continue
		}
		tool := sanitizeStreamText(p.scrub(block.Name))
		if tool == "" {
			continue
		}
		p.emit(StreamEvent{Kind: ToolCall, Tool: tool, Target: toolTarget(block.Input, p.scrub)})
	}
}

// handleResult makes this result event the deciding one: a decode failure
// drops any envelope held from an earlier result event.
func (p *streamParser) handleResult(line []byte) {
	var env ClaudeCodeEnvelope
	if err := json.Unmarshal(line, &env); err != nil {
		p.env, p.haveEnv, p.resultErr = ClaudeCodeEnvelope{}, false, err
		p.skipMalformed(line, err)
		return
	}
	p.env, p.haveEnv, p.resultErr = env, true, nil
}

// emitDenials reports the deciding envelope's refusals, one event per label.
func (p *streamParser) emitDenials() {
	for _, label := range RefusedToolLabels(p.env.PermissionDenials) {
		if tool := sanitizeStreamText(p.scrub(label)); tool != "" {
			p.emit(StreamEvent{Kind: PermissionDenied, Tool: tool})
		}
	}
}

// toolTarget derives a tool call's target from its input: the file path, else
// the command's first word, else the URL host. Each field is decoded on its
// own, so a field of the wrong type only drops itself. scrub sees each raw
// field before anything is cut from it.
func toolTarget(input json.RawMessage, scrub func(string) string) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(input, &fields) != nil {
		return ""
	}
	if t := sanitizeStreamText(scrub(stringField(fields, "file_path"))); t != "" {
		return t
	}
	if t := sanitizeStreamText(commandWord(scrub(stringField(fields, "command")))); t != "" {
		return t
	}
	return urlHost(scrub(stringField(fields, "url")))
}

func stringField(fields map[string]json.RawMessage, key string) string {
	var s string
	if json.Unmarshal(fields[key], &s) != nil {
		return ""
	}
	return s
}

// commandWord returns the first word of a shell command, passing over leading
// NAME=value and NAME+=value assignments so an inline secret is never shown.
// A value is skipped whole, quotes included. Whatever this reading cannot be
// sure of yields no word at all: an unclosed quote, a substitution in a
// value, or a first word holding a quote, backslash or `=`.
func commandWord(command string) string {
	rest := command
	for {
		rest = strings.TrimLeftFunc(rest, unicode.IsSpace)
		end, isAssign, ok := assignmentEnd(rest)
		if !ok {
			return ""
		}
		if !isAssign {
			break
		}
		rest = rest[end:]
	}
	word := rest
	if i := strings.IndexFunc(word, unicode.IsSpace); i >= 0 {
		word = word[:i]
	}
	if strings.ContainsAny(word, "'\"`\\=") {
		return ""
	}
	return word
}

// assignmentEnd reports whether s starts with a shell assignment and, if so,
// the byte offset just past its value. ok is false when the value cannot be
// delimited with certainty.
func assignmentEnd(s string) (end int, isAssign, ok bool) {
	i := 0
	for i < len(s) && isShellNameByte(s[i]) {
		i++
	}
	if i == 0 || !isShellName(s[:i]) {
		return 0, false, true
	}
	switch {
	case strings.HasPrefix(s[i:], "="):
		i++
	case strings.HasPrefix(s[i:], "+="):
		i += 2
	default:
		return 0, false, true
	}
	n, ok := valueEnd(s[i:])
	return i + n, true, ok
}

// valueEnd returns the length of the assignment value s starts with: up to
// the first unquoted blank. Only space, tab and newline end it, as in the
// shell, so the value never ends before the shell's does.
func valueEnd(s string) (int, bool) {
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == ' ' || c == '\t' || c == '\n':
			return i, true
		case c == '\'':
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return 0, false
			}
			i += j + 1
		case c == '"':
			j, ok := doubleQuotedEnd(s[i+1:])
			if !ok {
				return 0, false
			}
			i += j + 1
		case c == '\\':
			if i+1 == len(s) {
				return 0, false
			}
			i++
		case c == '`' || c == '(' || strings.HasPrefix(s[i:], "${") || strings.HasPrefix(s[i:], "$'") || strings.HasPrefix(s[i:], "$["):
			return 0, false
		}
	}
	return len(s), true
}

// doubleQuotedEnd returns the offset of the quote closing a double-quoted
// string whose body s starts, honouring backslash escapes. A substitution
// inside the quotes may hold a quote of its own, so it is not delimited.
func doubleQuotedEnd(s string) (int, bool) {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			return i, true
		case '\\':
			i++
		case '`':
			return 0, false
		case '$':
			if i+1 < len(s) && (s[i+1] == '(' || s[i+1] == '{') {
				return 0, false
			}
		}
	}
	return 0, false
}

func isShellNameByte(c byte) bool {
	return c == '_' || (c >= '0' && c <= '9') || isASCIILetter(rune(c))
}

func isShellName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if r != '_' && !isASCIILetter(r) && (i == 0 || r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func isASCIILetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

// urlHost returns the lowercase host of raw when it has the DNS shape a
// refusal label accepts, and nothing else: no scheme, userinfo, port, path or
// query. It is bounded like every other target.
func urlHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if host := strings.ToLower(u.Hostname()); dnsHost.MatchString(host) {
		return sanitizeStreamText(host)
	}
	return ""
}

// sanitizeStreamText makes model-controlled text safe for one terminal line:
// invalid UTF-8 and control, format, line- and paragraph-separator runes are
// dropped, and the result is trimmed and bounded to maxStreamTargetRunes.
func sanitizeStreamText(s string) string {
	var b strings.Builder
	n := 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if r == utf8.RuneError && size <= 1 {
			continue
		}
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			continue
		}
		if n == maxStreamTargetRunes {
			b.WriteString("…")
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

// lineSplitter is a bufio.SplitFunc source that yields newline-terminated
// lines and skips any line of limit bytes or more, so an oversized line costs
// one DEBUG record instead of ending the scan with bufio.ErrTooLong.
type lineSplitter struct {
	limit    int
	skipping bool
	onSkip   func()
}

func (s *lineSplitter) split(data []byte, atEOF bool) (int, []byte, error) {
	if s.skipping {
		return s.discard(data)
	}
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		return i + 1, data[:i], nil
	}
	if len(data) >= s.limit {
		s.skipping = true
		s.onSkip()
		return s.discard(data)
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// discard drops the rest of an oversized line up to and including its newline.
func (s *lineSplitter) discard(data []byte) (int, []byte, error) {
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		s.skipping = false
		return i + 1, nil, nil
	}
	return len(data), nil, nil
}
