// Package po extracts translatable messages from bentoolkit's Go source and
// reads and writes the gettext .pot/.po files that hold them.
//
// GNU xgettext before 0.25 has no Go parser, so extraction is done here with
// go/ast: every call i18n.N("...") or i18n.T("...") contributes its argument,
// which must be a string literal or a concatenation of literals. Anything else
// is an error for N, because a computed msgid can never be found in a
// catalog; T of a non-literal is skipped, as it translates text that an N
// marked where it was declared.
package po

import (
	"bufio"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// merge adds the messages found in file rel to msgs, which index maps by
// msgid, recording rel as a reference of each.
func merge(msgs []Message, index map[string]int, found []Message, rel string) ([]Message, error) {
	for _, m := range found {
		i, ok := index[m.ID]
		if !ok {
			index[m.ID] = len(msgs)
			msgs = append(msgs, Message{ID: m.ID, Plural: m.Plural, Refs: []string{rel}})
			continue
		}
		if msgs[i].Plural != m.Plural {
			// gettext keys an entry by its msgid alone: one msgid cannot
			// be both a plain and a plural message, or two plurals.
			return nil, fmt.Errorf("%s: %q is used with two plural forms (%q and %q)", rel, m.ID, msgs[i].Plural, m.Plural)
		}
		if !slices.Contains(msgs[i].Refs, rel) {
			msgs[i].Refs = append(msgs[i].Refs, rel)
		}
	}
	return msgs, nil
}

// i18nImport is the import path whose N and T calls are extracted.
const i18nImport = "github.com/obentoo/bentoolkit/internal/common/i18n"

// Message is one msgid and the files that use it.
type Message struct {
	ID     string
	Plural string   // msgid_plural; set for TCount, TPlural and NPlural
	Refs   []string // repository-relative, slash-separated, sorted, unique
}

// Extract walks the given directories under root and returns every message
// marked with i18n.N, or passed as a literal to i18n.T, in non-test Go files, ordered by first
// appearance (file path, then position).
func Extract(root string, dirs ...string) ([]Message, error) {
	var files []string
	for _, d := range dirs {
		err := filepath.WalkDir(filepath.Join(root, d), func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.IsDir() && (e.Name() == "vendor" || e.Name() == "testdata") {
				return filepath.SkipDir
			}
			if !e.IsDir() && strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
				files = append(files, p)
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walking %s: %w", d, err)
		}
	}
	slices.Sort(files)

	var msgs []Message
	index := map[string]int{}
	fset := token.NewFileSet()
	for _, f := range files {
		rel, err := filepath.Rel(root, f)
		if err != nil {
			return nil, fmt.Errorf("relativising %s: %w", f, err)
		}
		rel = filepath.ToSlash(rel)
		found, err := extractFile(fset, f)
		if err != nil {
			return nil, err
		}
		if msgs, err = merge(msgs, index, found, rel); err != nil {
			return nil, err
		}
	}
	for i := range msgs {
		slices.Sort(msgs[i].Refs)
	}
	return msgs, nil
}

func extractFile(fset *token.FileSet, path string) ([]Message, error) {
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	local := ""
	for _, imp := range file.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p == i18nImport {
			local = "i18n"
			if imp.Name != nil {
				local = imp.Name.Name
			}
		}
	}
	if local == "" {
		return nil, nil
	}

	var out []Message
	var bad error
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || bad != nil {
			return bad == nil
		}
		fun := call.Fun
		if ix, ok := fun.(*ast.IndexExpr); ok { // an explicit type argument: TCount[int](...)
			fun = ix.X
		}
		sel, ok := fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if x, ok := sel.X.(*ast.Ident); !ok || x.Name != local {
			return true
		}
		m, err := extractCall(sel.Sel.Name, call.Args)
		if err != nil {
			bad = fmt.Errorf("%s: %s.%s %w", fset.Position(call.Pos()), local, sel.Sel.Name, err)
			return false
		}
		if m != nil {
			out = append(out, *m)
		}
		return true
	})
	return out, bad
}

// extractCall returns the message one i18n call marks, nil for a call that
// marks none (T of a variable, which translates text marked elsewhere, or a
// function that is not a marker), or an error for a marker it cannot read.
func extractCall(fn string, args []ast.Expr) (*Message, error) {
	var nlit, want int // literals the call must carry, arguments it takes
	switch fn {
	case "N", "T":
		nlit, want = 1, 1
	case "TCount":
		nlit, want = 1, 2
	case "NPlural":
		nlit, want = 2, 2
	case "TPlural":
		nlit, want = 2, 3
	default:
		return nil, nil
	}
	if len(args) != want {
		return nil, fmt.Errorf("takes %d arguments", want)
	}
	lits := make([]string, nlit)
	for i := range nlit {
		s, ok := literal(args[i])
		if !ok {
			if fn == "T" || fn == "TPlural" || fn == "TCount" {
				return nil, nil // translates text marked elsewhere (N, NPlural)
			}
			return nil, errors.New("needs a string literal, not a computed value")
		}
		if s == "" {
			return nil, errors.New("of an empty string")
		}
		if strings.ContainsRune(s, '\r') {
			// gettext refuses \r in a message; keep terminal control out of it.
			return nil, errors.New("of a string with \\r: write the \\r outside the message")
		}
		lits[i] = s
	}
	m := &Message{ID: lits[0]}
	switch fn {
	case "TCount":
		m.Plural = lits[0]
	case "NPlural", "TPlural":
		m.Plural = lits[1]
	}
	return m, nil
}

// literal evaluates a string literal or a + concatenation of them.
func literal(e ast.Expr) (string, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(v.Value)
		return s, err == nil
	case *ast.ParenExpr:
		return literal(v.X)
	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return "", false
		}
		a, ok := literal(v.X)
		if !ok {
			return "", false
		}
		b, ok := literal(v.Y)
		return a + b, ok
	default:
		return "", false
	}
}

// WritePOT writes msgs as a gettext template. The output carries no line
// numbers and no creation date, so it changes only when the messages do.
func WritePOT(w io.Writer, msgs []Message) error {
	bw := bufio.NewWriter(w)
	fmt.Fprint(bw, `# Translatable messages of bentoolkit.
# Generated by "make i18n-update" (misc/i18n-extract); do not edit by hand.
msgid ""
msgstr ""
"Project-Id-Version: bentoolkit\n"
"MIME-Version: 1.0\n"
"Content-Type: text/plain; charset=UTF-8\n"
"Content-Transfer-Encoding: 8bit\n"
`)
	for _, m := range msgs {
		fmt.Fprint(bw, "\n")
		fmt.Fprintf(bw, "#: %s\n", strings.Join(m.Refs, " "))
		writeField(bw, "msgid", m.ID)
		if m.Plural != "" {
			writeField(bw, "msgid_plural", m.Plural)
			writeField(bw, "msgstr[0]", "")
			writeField(bw, "msgstr[1]", "")
			continue
		}
		writeField(bw, "msgstr", "")
	}
	return bw.Flush()
}

// writeField writes a keyword and a quoted value the way msgmerge --no-wrap
// does: one line when the value has no newline inside it, otherwise an empty
// first line and one line per "\n"-terminated piece.
func writeField(w io.Writer, keyword, s string) {
	body := strings.TrimSuffix(s, "\n")
	if !strings.Contains(body, "\n") {
		fmt.Fprintf(w, "%s %s\n", keyword, quote(s))
		return
	}
	fmt.Fprintf(w, "%s \"\"\n", keyword)
	for s != "" {
		line, rest, found := strings.Cut(s, "\n")
		if found {
			line += "\n"
		}
		fmt.Fprint(w, quote(line), "\n")
		s = rest
	}
}

func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// Entry is one message of a .po file. A plural entry has Plural set and its
// translations in Forms (Str is then Forms[0]).
type Entry struct {
	ID     string
	Plural string
	Str    string
	Forms  []string
	Fuzzy  bool
}

// Parse reads the messages of a .po or .pot file, skipping the header and
// obsolete (#~) entries. It understands the subset bentoolkit's files use:
// plural forms, but no msgctxt.
func Parse(r io.Reader) ([]Entry, error) {
	_, entries, err := ParseWithHeader(r)
	return entries, err
}

// ParseWithHeader is Parse that also returns the header entry's msgstr
// ("Language: ...\nPlural-Forms: ...").
func ParseWithHeader(r io.Reader) (string, []Entry, error) {
	var p poReader
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		if err := p.line(strings.TrimSpace(sc.Text())); err != nil {
			return "", nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
	}
	if err := sc.Err(); err != nil {
		return "", nil, fmt.Errorf("reading: %w", err)
	}
	p.flush()
	return p.header, p.out, nil
}

// poReader is the state of ParseWithHeader between lines.
type poReader struct {
	out     []Entry
	header  string
	cur     Entry
	field   *string // where a continuation "..." line appends
	started bool
	fuzzy   bool // a "#, fuzzy" flag seen for the entry about to start
}

func (p *poReader) flush() {
	if p.started && p.cur.ID != "" {
		if p.cur.Plural != "" && len(p.cur.Forms) > 0 {
			p.cur.Str = p.cur.Forms[0]
		}
		p.out = append(p.out, p.cur)
	}
	if p.started && p.cur.ID == "" {
		p.header = p.cur.Str
	}
	p.cur, p.field, p.started = Entry{}, nil, false
}

// set points the continuation target at dst and appends q to it.
func (p *poReader) set(dst *string, q string) error {
	p.field = dst
	return appendQuoted(dst, q)
}

func (p *poReader) line(line string) error {
	switch {
	case line == "":
		p.flush()
		p.fuzzy = false
	case strings.HasPrefix(line, "#"):
		p.flush()
		if strings.HasPrefix(line, "#,") && strings.Contains(line, "fuzzy") {
			p.fuzzy = true
		}
	case strings.HasPrefix(line, "msgctxt"):
		return errors.New("msgctxt is not supported")
	case strings.HasPrefix(line, "msgid_plural "):
		if !p.started {
			return errors.New("msgid_plural without msgid")
		}
		return p.set(&p.cur.Plural, strings.TrimPrefix(line, "msgid_plural "))
	case strings.HasPrefix(line, "msgstr["):
		return p.pluralForm(strings.TrimPrefix(line, "msgstr["))
	case strings.HasPrefix(line, "msgid "):
		p.flush()
		p.started, p.cur.Fuzzy, p.fuzzy = true, p.fuzzy, false
		return p.set(&p.cur.ID, strings.TrimPrefix(line, "msgid "))
	case strings.HasPrefix(line, "msgstr "):
		if !p.started {
			return errors.New("msgstr without msgid")
		}
		return p.set(&p.cur.Str, strings.TrimPrefix(line, "msgstr "))
	case strings.HasPrefix(line, `"`):
		if p.field == nil {
			return errors.New("string outside an entry")
		}
		return appendQuoted(p.field, line)
	default:
		return fmt.Errorf("unexpected %q", line)
	}
	return nil
}

// pluralForm reads `N] "..."`, the rest of a msgstr[N] line; forms must come
// in order, after a msgid_plural.
func (p *poReader) pluralForm(rest string) error {
	idx, q, ok := strings.Cut(rest, "] ")
	n, err := strconv.Atoi(idx)
	if !ok || err != nil || p.cur.Plural == "" || n != len(p.cur.Forms) {
		return fmt.Errorf("msgstr[%s] out of order or without msgid_plural", idx)
	}
	p.cur.Forms = append(p.cur.Forms, "")
	return p.set(&p.cur.Forms[n], q)
}

func appendQuoted(dst *string, q string) error {
	s, err := strconv.Unquote(q)
	if err != nil {
		return errors.New("malformed quoted string " + q)
	}
	*dst += s
	return nil
}

// verbRE matches a fmt verb: flags, width, precision and the verb letter, or
// a literal %%.
var verbRE = regexp.MustCompile(`%[-+# 0]*(\[\d+\])?(\d+|\*)?(\.(\d+|\*))?[a-zA-Z%]`)

// Verbs returns the fmt verbs of s in order, %% excluded.
func Verbs(s string) []string {
	var out []string
	for _, v := range verbRE.FindAllString(s, -1) {
		if v != "%%" {
			out = append(out, v)
		}
	}
	return out
}

// ArgVerbs returns the verbs of s keyed by the argument each one formats, as
// sorted "index:verb" strings ("1:%d", "2:%s"), resolving explicit indexes
// the way fmt does: %[n] selects argument n and the next implicit verb takes
// n+1. A translation may reorder its verbs only with explicit indexes
// ("%[2]s ... %[1]d"), since its arguments are passed in the msgid's order;
// it is correct exactly when its ArgVerbs equal the msgid's.
func ArgVerbs(s string) []string {
	var out []string
	n := 0
	for _, m := range verbRE.FindAllStringSubmatch(s, -1) {
		v := m[0]
		if v == "%%" {
			continue
		}
		if m[1] != "" {
			i, err := strconv.Atoi(m[1][1 : len(m[1])-1])
			if err != nil {
				i = -1
			}
			n = i
			v = strings.Replace(v, m[1], "", 1)
		} else {
			n++
		}
		out = append(out, strconv.Itoa(n)+":"+v)
	}
	slices.Sort(out)
	return out
}
