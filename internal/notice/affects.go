package notice

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/obentoo/bentoolkit/internal/common/ebuild"
)

// ErrAffects is wrapped by every --affects parse error.
var ErrAffects = errors.New("invalid --affects value")

var (
	// cpRe and slotRe are the site's notice schema patterns. The
	// slot pattern has no `/` on purpose: a subslot is refused, because the
	// feed and the tray compare the slot only and would silently drop it.
	cpRe   = regexp.MustCompile(`^[A-Za-z0-9+_.-]+/[A-Za-z0-9+_-]+$`)
	slotRe = regexp.MustCompile(`^[A-Za-z0-9+_.-]+$`)

	// rangeOps is ordered longest first, so `<=1.0` never reads as `<` and
	// the version `=1.0`.
	rangeOps = []string{"<=", ">=", "<", ">", "="}
)

// ParseAffects parses one --affects value,
// `<category>/<package>[:<slot>][ <range>[,<range>…]]`, where a range is an
// operator from <, <=, =, >=, > immediately followed by a Gentoo version.
// A rejection quotes the value and the part that failed.
func ParseAffects(s string) (Affects, error) {
	fail := func(part, why string) (Affects, error) { //nolint:unparam // shaped like ParseAffects so each rejection is one `return fail(...)`
		return Affects{}, fmt.Errorf("--affects %q: %q %s: %w", s, part, why, ErrAffects)
	}

	value := strings.TrimSpace(s)
	head, tail := value, ""
	if i := strings.IndexFunc(value, unicode.IsSpace); i >= 0 {
		head, tail = value[:i], strings.TrimSpace(value[i:])
	}

	cp, slot, hasSlot := strings.Cut(head, ":")
	if !cpRe.MatchString(cp) {
		return fail(head, "is not category/package")
	}
	if hasSlot && !slotRe.MatchString(slot) {
		return fail(":"+slot, "is not a slot (a subslot or repository is not allowed)")
	}

	a := Affects{CP: cp, Slot: slot}
	if tail == "" {
		return a, nil
	}
	for r := range strings.SplitSeq(tail, ",") {
		if r == "" {
			return fail(",", "leaves an empty range")
		}
		rng, ok := parseRange(r)
		if !ok {
			return fail(r, "is not an operator (<, <=, =, >=, >) followed by a Gentoo version")
		}
		a.Ranges = append(a.Ranges, rng)
	}
	return a, nil
}

func parseRange(r string) (Range, bool) {
	for _, op := range rangeOps {
		ver, ok := strings.CutPrefix(r, op)
		if !ok {
			continue
		}
		// IsValidVersion trims whitespace itself; a version that needs
		// trimming is not the documented syntax, and the site rejects it.
		if ver == "" || strings.IndexFunc(ver, unicode.IsSpace) >= 0 || !ebuild.IsValidVersion(ver) {
			return Range{}, false
		}
		return Range{Op: op, Ver: ver}, true
	}
	return Range{}, false
}
