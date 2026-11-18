package i18n

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// pluralRule maps a count to the index of the plural form to use.
type pluralRule func(n uint64) int

// germanic is gettext's default rule, used when a catalog declares none:
// form 0 for one, form 1 for any other count.
func germanic(n uint64) int {
	if n == 1 {
		return 0
	}
	return 1
}

// parsePluralForms reads a catalog header's "Plural-Forms: nplurals=N;
// plural=EXPR;" line. It returns the number of forms and the rule; a header
// without the line yields gettext's default (2, germanic).
func parsePluralForms(header string) (int, pluralRule, error) {
	line := ""
	for _, l := range strings.Split(header, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "Plural-Forms:"); ok {
			line = v
		}
	}
	if line == "" {
		return 2, germanic, nil
	}
	nplurals, formula := -1, ""
	for _, part := range strings.Split(line, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "nplurals":
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil || n < 1 || n > 6 {
				return 0, nil, fmt.Errorf("plural forms: bad nplurals %q", v)
			}
			nplurals = n
		case "plural":
			formula = v
		}
	}
	if nplurals < 0 || formula == "" {
		return 0, nil, fmt.Errorf("plural forms: incomplete %q", line)
	}
	e, err := parsePluralExpr(formula)
	if err != nil {
		return 0, nil, fmt.Errorf("plural forms: %w", err)
	}
	return nplurals, func(n uint64) int {
		i := e.value(n)
		if i >= uint64(nplurals) { //nolint:gosec // G115: nplurals was checked to be 1..6.
			return 0
		}
		return int(i) //nolint:gosec // G115: i < nplurals <= 6.
	}, nil
}

// node is part of a plural formula: C's unsigned integer arithmetic on the
// single variable n, the subset gettext's Plural-Forms formulas use. It is
// computed by walking the parsed tree; nothing is executed.
type node interface{ value(n uint64) uint64 }

type (
	numNode  uint64
	varNode  struct{}
	notNode  struct{ x node }
	condNode struct{ c, a, b node }
	binNode  struct {
		op   string
		l, r node
	}
)

func (e numNode) value(uint64) uint64   { return uint64(e) }
func (varNode) value(n uint64) uint64   { return n }
func (e notNode) value(n uint64) uint64 { return b2u(e.x.value(n) == 0) }
func (e condNode) value(n uint64) uint64 {
	if e.c.value(n) != 0 {
		return e.a.value(n)
	}
	return e.b.value(n)
}

func (e binNode) value(n uint64) uint64 {
	l := e.l.value(n)
	if e.op == "&&" {
		return b2u(l != 0 && e.r.value(n) != 0)
	}
	if e.op == "||" {
		return b2u(l != 0 || e.r.value(n) != 0)
	}
	r := e.r.value(n)
	switch e.op {
	case "==":
		return b2u(l == r)
	case "!=":
		return b2u(l != r)
	case "<":
		return b2u(l < r)
	case "<=":
		return b2u(l <= r)
	case ">":
		return b2u(l > r)
	case ">=":
		return b2u(l >= r)
	case "+":
		return l + r
	case "-":
		return l - r
	case "*":
		return l * r
	case "/":
		if r == 0 {
			return 0
		}
		return l / r
	default: // "%", the only operator left: the parser admits no other
		if r == 0 {
			return 0
		}
		return l % r
	}
}

func b2u(b bool) uint64 {
	if b {
		return 1
	}
	return 0
}

// maxPluralDepth bounds nesting, so a hostile catalog cannot exhaust the
// stack; real formulas nest a handful of levels.
const maxPluralDepth = 64

type pluralParser struct {
	toks  []string
	pos   int
	depth int
}

// binaryLevels returns C's binary operators from lowest to highest
// precedence.
func binaryLevels() [][]string {
	return [][]string{{"||"}, {"&&"}, {"==", "!="}, {"<", "<=", ">", ">="}, {"+", "-"}, {"*", "/", "%"}}
}

func parsePluralExpr(s string) (node, error) {
	toks, err := tokenizePlural(s)
	if err != nil {
		return nil, err
	}
	p := &pluralParser{toks: toks}
	e, err := p.ternary()
	if err != nil {
		return nil, err
	}
	if p.pos != len(p.toks) {
		return nil, fmt.Errorf("unexpected %q", p.toks[p.pos])
	}
	return e, nil
}

func tokenizePlural(s string) ([]string, error) {
	var toks []string
	for i := 0; i < len(s); {
		c := s[i]
		two := ""
		if i+1 < len(s) {
			two = s[i : i+2]
		}
		switch {
		case c == ' ' || c == '\t':
			i++
		case c >= '0' && c <= '9':
			j := i
			for j < len(s) && s[j] >= '0' && s[j] <= '9' {
				j++
			}
			toks = append(toks, s[i:j])
			i = j
		case two == "&&" || two == "||" || two == "==" || two == "!=" || two == "<=" || two == ">=":
			toks = append(toks, two)
			i += 2
		case c == 'n' || strings.IndexByte("()?:+-*/%<>!", c) >= 0:
			toks = append(toks, string(c))
			i++
		default:
			return nil, fmt.Errorf("unexpected character %q", c)
		}
	}
	return toks, nil
}

func (p *pluralParser) peek() string {
	if p.pos < len(p.toks) {
		return p.toks[p.pos]
	}
	return ""
}

func (p *pluralParser) enter() error {
	p.depth++
	if p.depth > maxPluralDepth {
		return errors.New("expression nested too deeply")
	}
	return nil
}

func (p *pluralParser) ternary() (node, error) {
	if err := p.enter(); err != nil {
		return nil, err
	}
	defer func() { p.depth-- }()
	c, err := p.binary(0)
	if err != nil || p.peek() != "?" {
		return c, err
	}
	p.pos++
	a, err := p.ternary()
	if err != nil {
		return nil, err
	}
	if p.peek() != ":" {
		return nil, errors.New("missing ':' in conditional")
	}
	p.pos++
	b, err := p.ternary()
	if err != nil {
		return nil, err
	}
	return condNode{c, a, b}, nil
}

func (p *pluralParser) binary(level int) (node, error) {
	levels := binaryLevels()
	if level == len(levels) {
		return p.unary()
	}
	l, err := p.binary(level + 1)
	if err != nil {
		return nil, err
	}
	for {
		op := p.peek()
		found := false
		for _, o := range levels[level] {
			if op == o {
				found = true
			}
		}
		if !found {
			return l, nil
		}
		p.pos++
		r, err := p.binary(level + 1)
		if err != nil {
			return nil, err
		}
		l = binNode{op, l, r}
	}
}

func (p *pluralParser) unary() (node, error) {
	if err := p.enter(); err != nil {
		return nil, err
	}
	defer func() { p.depth-- }()
	switch t := p.peek(); {
	case t == "!":
		p.pos++
		x, err := p.unary()
		if err != nil {
			return nil, err
		}
		return notNode{x}, nil
	case t == "n":
		p.pos++
		return varNode{}, nil
	case t == "(":
		p.pos++
		e, err := p.ternary()
		if err != nil {
			return nil, err
		}
		if p.peek() != ")" {
			return nil, errors.New("missing ')'")
		}
		p.pos++
		return e, nil
	case t != "" && t[0] >= '0' && t[0] <= '9':
		p.pos++
		v, err := strconv.ParseUint(t, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("bad number %q", t)
		}
		return numNode(v), nil
	case t == "":
		return nil, errors.New("unexpected end of expression")
	default:
		return nil, fmt.Errorf("unexpected %q", t)
	}
}
