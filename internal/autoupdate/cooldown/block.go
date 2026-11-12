// Package cooldown is the autoupdate cooldown block as a value: the managed
// section of an overlay's profiles/package.mask that holds every freshly
// promoted version masked until it is released by time, by installation or by
// hand.
//
// The block is a contract read by Portage (it is a plain package.mask
// section), by bentoolkit and by the tray. Its shape is fixed here:
//
//	# BEGIN bentoo-cooldown format=1 days=7
//	# Managed by `bentoo overlay autoupdate`. Edit with `bentoo overlay cooldown`, never by hand.
//	# cooldown since=2026-11-07T07:10:00Z origin=autoupdate group=sys-apps/a,sys-apps/b
//	=sys-apps/a-3.2.0
//	# cooldown since=2026-11-08T09:00:00Z origin=autoupdate line=app-editors/zed@preview first=2026-11-06T09:00:00Z
//	=app-editors/zed-1.28.0_pre
//	# END bentoo-cooldown
//
// The presence of the BEGIN line is what enables cooldown for an overlay; an
// overlay without it is untouched. Enable writes it. The package never logs: it returns errors
// and leaves logging to its callers.
package cooldown

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/ebuild"
)

const (
	// MaskRel is the overlay-relative path of the file holding the block.
	MaskRel = "profiles/package.mask"
	// UnmaskRel is the overlay-relative path of the published unmask list.
	UnmaskRel = ".autoupdate/cooldown.unmask"

	beginPrefix = "# BEGIN bentoo-cooldown"
	endLine     = "# END bentoo-cooldown"
	headerLine  = "# Managed by `bentoo overlay autoupdate`. Edit with `bentoo overlay cooldown`, never by hand."
	entryPrefix = "# cooldown "

	// MaxDays bounds days=N: ten years is far past any sensible delay, and a
	// bound keeps the release date from overflowing time.Duration.
	MaxDays = 3650

	// FormatVersion is the only block format this package reads and writes.
	FormatVersion = 1

	// MaxWaitFactor bounds how long a release line can stay masked when every
	// candidate is replaced before it ages: the line's first entry plus
	// MaxWaitFactor times its days releases whatever candidate is current.
	// With days=7 that is 21 days.
	MaxWaitFactor = 3

	// OriginAutoupdate marks an entry the applier added on promotion.
	OriginAutoupdate = "autoupdate"
	// OriginManual marks an entry added by `bentoo overlay cooldown add`.
	OriginManual = "manual"
)

// ErrMalformed is wrapped by every error Load returns for block content it
// cannot read. The message names the file and the line.
var ErrMalformed = errors.New("malformed cooldown block")

// Entry is one masked version.
type Entry struct {
	// Package is "cat/pkg".
	Package string
	// Version is the full PVR ("1.2.3-r1").
	Version string
	// Since is when the version was masked; the release clock starts here.
	// A candidate that replaces another starts its own clock.
	Since time.Time
	// Line is the registry record key ("cat/pkg", "cat/pkg@label",
	// "cat/pkg:slot") whose release line the entry is the candidate of, or
	// empty for an entry that belongs to no line (a manual add). A line holds
	// at most one candidate: Replace supersedes the previous one.
	Line string
	// First is when the line's current run of candidates started: the Since
	// of the first candidate a chain of replacements descends from. It bounds
	// the wait (MaxWaitFactor). Zero means Since.
	First time.Time
	// Days overrides the block's days=N for this entry (a record's
	// cooldown_days); zero means the block's.
	// Group is the sorted, comma-joined set {pkg} ∪ cooldown_with, or empty.
	Group string
	// Origin is OriginAutoupdate or OriginManual.
	Origin string
	Days   int
}

// Atom returns the entry's package.mask atom, "=cat/pkg-PVR".
func (e Entry) Atom() string { return "=" + e.Package + "-" + e.Version }

// Block is the parsed managed block.
type Block struct {
	// Days is the release delay of the BEGIN line's days=N.
	Days int
	// Entries are kept sorted by package, then Gentoo version.
	Entries []Entry

	// loadHash is the SHA-256 of profiles/package.mask when the block was
	// loaded (or last saved). Save refuses to write over a file whose bytes
	// changed since. hashed is false on a Block built in memory.
	loadHash [sha256.Size]byte
	hashed   bool
}

// Category and package-name grammar of PMS 3.1.1 and 3.1.2. A package name
// that ends in "-<version>" is ruled out by the atom split, not by this
// expression.
var (
	categoryRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9+_.-]*$`)
	pkgNameRe  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9+_-]*$`)
)

// ValidatePackage reports whether pkg is a bare "cat/pkg".
func ValidatePackage(pkg string) error {
	cat, name, ok := strings.Cut(pkg, "/")
	if !ok || !categoryRe.MatchString(cat) || !pkgNameRe.MatchString(name) {
		return fmt.Errorf("%q is not a cat/pkg", pkg)
	}
	return nil
}

// validVersion is ebuild.IsValidVersion without its whitespace trimming: a
// version that is written into a mask file must be the bare form.
func validVersion(v string) bool {
	return v == strings.TrimSpace(v) && ebuild.IsValidVersion(v)
}

// ValidateAtom reports whether pkg and pvr form a safe "=cat/pkg-PVR" atom:
// a bare cat/pkg and a PVR of the Gentoo version grammar, so nothing written
// from them can carry a second line, an operator or a repository.
func ValidateAtom(pkg, pvr string) error {
	if err := ValidatePackage(pkg); err != nil {
		return fmt.Errorf("cooldown atom: %w", err)
	}
	if !validVersion(pvr) {
		return fmt.Errorf("cooldown atom: %q is not a Gentoo version (PVR) for %s", pvr, pkg)
	}
	return nil
}

// ParseAtom splits "=cat/pkg-PVR" into its package and version. The version
// is the shortest valid version suffix scanned from the right, so names that
// hold "-<digit>" segments (libfoo-2d) keep them.
func ParseAtom(atom string) (pkg, version string, err error) {
	rest, ok := strings.CutPrefix(atom, "=")
	if !ok {
		return "", "", fmt.Errorf("%q: a cooldown atom starts with '='", atom)
	}
	slash := strings.IndexByte(rest, '/')
	if slash < 0 {
		return "", "", fmt.Errorf("%q does not name cat/pkg-version", atom)
	}
	for i := strings.LastIndexByte(rest, '-'); i > slash; i = strings.LastIndexByte(rest[:i], '-') {
		v := rest[i+1:]
		if !validVersion(v) {
			continue
		}
		pkg = rest[:i]
		if err := ValidateAtom(pkg, v); err != nil {
			return "", "", fmt.Errorf("%q: %w", atom, err)
		}
		return pkg, v, nil
	}
	return "", "", fmt.Errorf("%q carries no version", atom)
}

// span locates the block inside the file: the byte offset where its BEGIN
// line starts and the offset just past its END line (newline included).
type span struct{ start, stop int }

// Load reads the block of overlayRoot's profiles/package.mask. It returns
// nil, nil when the file or the block does not exist (cooldown disabled for
// this overlay), and an error wrapping ErrMalformed, naming the file line, on
// any content it cannot read. Load never writes.
func Load(overlayRoot string) (*Block, error) {
	path := filepath.Join(overlayRoot, MaskRel)
	data, err := os.ReadFile(path) // #nosec G304 -- the overlay's own profiles/package.mask
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	b, _, err := parse(path, data)
	if err != nil || b == nil {
		return nil, err
	}
	b.loadHash, b.hashed = sha256.Sum256(data), true
	return b, nil
}

// parse reads data (the bytes of path). A nil block with a nil error means no
// BEGIN line.
func parse(path string, data []byte) (*Block, span, error) {
	p := &parser{path: path, seen: map[string]int{}}
	text := string(data)
	offset := 0
	for lineNo := 1; offset < len(text); lineNo++ {
		next := len(text)
		if end := strings.IndexByte(text[offset:], '\n'); end >= 0 {
			next = offset + end + 1
		}
		if err := p.line(lineNo, offset, next, strings.TrimRight(text[offset:next], "\n")); err != nil {
			return nil, span{}, err
		}
		offset = next
	}
	if p.inBlock {
		return nil, span{}, p.malformed(lastLine(text), "the cooldown block has no %q line", endLine)
	}
	if p.b != nil {
		sortEntries(p.b.Entries)
	}
	return p.b, p.sp, nil
}

// parser is parse's state between lines.
type parser struct {
	path     string
	b        *Block
	sp       span
	inBlock  bool
	ended    bool
	pending  *Entry
	pendLine int
	seen     map[string]int // "cat/pkg-PVR" -> line first listed
}

func (p *parser) malformed(line int, format string, args ...any) error {
	return fmt.Errorf("%s:%d: %s: %w", p.path, line, fmt.Sprintf(format, args...), ErrMalformed)
}

// line consumes one file line, which spans the bytes [start, next).
func (p *parser) line(lineNo, start, next int, line string) error {
	isBegin := line == beginPrefix || strings.HasPrefix(line, beginPrefix+" ")
	switch {
	case !p.inBlock && isBegin:
		return p.begin(lineNo, start, line)
	case !p.inBlock, line == headerLine:
		return nil
	case isBegin:
		return p.malformed(lineNo, "a second BEGIN line before the END line")
	case line == endLine:
		if p.pending != nil {
			return p.malformed(p.pendLine, "a `# cooldown` line without its atom")
		}
		p.sp.stop, p.inBlock, p.ended = next, false, true
		return nil
	case strings.HasPrefix(line, entryPrefix):
		if p.pending != nil {
			return p.malformed(p.pendLine, "a `# cooldown` line without its atom")
		}
		e, err := parseEntryComment(strings.TrimPrefix(line, entryPrefix))
		if err != nil {
			return p.malformed(lineNo, "%v", err)
		}
		p.pending, p.pendLine = &e, lineNo
		return nil
	case strings.HasPrefix(line, "="):
		return p.atom(lineNo, line)
	default:
		return p.malformed(lineNo, "unknown line %q inside the cooldown block", line)
	}
}

func (p *parser) begin(lineNo, start int, line string) error {
	if p.ended {
		return p.malformed(lineNo, "a second BEGIN line; only one cooldown block is allowed")
	}
	days, err := parseBegin(line)
	if err != nil {
		return p.malformed(lineNo, "%v", err)
	}
	p.b, p.sp.start, p.inBlock = &Block{Days: days}, start, true
	return nil
}

func (p *parser) atom(lineNo int, line string) error {
	if p.pending == nil {
		return p.malformed(lineNo, "atom %q without its `# cooldown` line", line)
	}
	pkg, ver, err := ParseAtom(line)
	if err != nil {
		return p.malformed(lineNo, "%v", err)
	}
	key := pkg + "-" + ver
	if first, dup := p.seen[key]; dup {
		return p.malformed(lineNo, "=%s is already listed at line %d", key, first)
	}
	p.seen[key] = lineNo
	p.pending.Package, p.pending.Version = pkg, ver
	if err := validateEntry(*p.pending); err != nil {
		return p.malformed(lineNo, "%v", err)
	}
	p.b.Entries = append(p.b.Entries, *p.pending)
	p.pending = nil
	return nil
}

// lastLine is the number of the last line of text.
func lastLine(text string) int {
	n := strings.Count(text, "\n")
	if !strings.HasSuffix(text, "\n") {
		n++
	}
	return n
}

// parseBegin reads "format=1 days=N" off a BEGIN line and returns N.
func parseBegin(line string) (int, error) {
	fields := strings.Fields(strings.TrimPrefix(line, beginPrefix))
	format, days := "", ""
	for _, f := range fields {
		k, v, ok := strings.Cut(f, "=")
		switch {
		case !ok:
			return 0, fmt.Errorf("unknown BEGIN field %q", f)
		case k == "format":
			format = v
		case k == "days":
			days = v
		default:
			return 0, fmt.Errorf("unknown BEGIN field %q", f)
		}
	}
	if format != strconv.Itoa(FormatVersion) {
		return 0, fmt.Errorf("unknown cooldown block format %q (this bentoolkit reads format=%d only)", format, FormatVersion)
	}
	n, err := strconv.Atoi(days)
	if err != nil || n < 1 || n > MaxDays || days != strconv.Itoa(n) {
		return 0, fmt.Errorf("days=%q must be a whole number of days from 1 to %d", days, MaxDays)
	}
	return n, nil
}

// parseEntryComment reads "since=<RFC3339> origin=<o> [group=a,b] [line=<record>] [first=<RFC3339>] [days=N]".
func parseEntryComment(s string) (Entry, error) {
	var e Entry
	var hasSince bool
	for _, f := range strings.Fields(s) {
		k, v, ok := strings.Cut(f, "=")
		if !ok {
			return e, fmt.Errorf("unknown `# cooldown` field %q", f)
		}
		switch k {
		case "since":
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return e, fmt.Errorf("since=%q is not an RFC 3339 time: %w", v, err)
			}
			e.Since, hasSince = t.UTC(), true
		case "origin":
			if v != OriginAutoupdate && v != OriginManual {
				return e, fmt.Errorf("origin=%q must be %s or %s", v, OriginAutoupdate, OriginManual)
			}
			e.Origin = v
		case "group":
			e.Group = v
		case "line":
			e.Line = v
		case "first":
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return e, fmt.Errorf("first=%q is not an RFC 3339 time: %w", v, err)
			}
			e.First = t.UTC()
		case "days":
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > MaxDays {
				return e, fmt.Errorf("days=%q must be a whole number of days from 1 to %d", v, MaxDays)
			}
			e.Days = n
		default:
			return e, fmt.Errorf("unknown `# cooldown` field %q", f)
		}
	}
	if !hasSince {
		return e, errors.New("a `# cooldown` line needs since=")
	}
	if e.Origin == "" {
		return e, errors.New("a `# cooldown` line needs origin=")
	}
	return e, nil
}

// validateEntry checks what render writes and parse reads back: a safe atom,
// a known origin, a recorded since, and a group that is a sorted set of two
// or more packages holding the entry's own.
func validateEntry(e Entry) error {
	if err := ValidateAtom(e.Package, e.Version); err != nil {
		return err
	}
	if e.Origin != OriginAutoupdate && e.Origin != OriginManual {
		return fmt.Errorf("=%s-%s: origin %q must be %s or %s", e.Package, e.Version, e.Origin, OriginAutoupdate, OriginManual)
	}
	if e.Since.IsZero() {
		return fmt.Errorf("=%s-%s: no since time", e.Package, e.Version)
	}
	if !e.First.IsZero() && e.First.After(e.Since) {
		return fmt.Errorf("=%s-%s: first %s is after since %s", e.Package, e.Version,
			e.First.UTC().Format(time.RFC3339), e.Since.UTC().Format(time.RFC3339))
	}
	if e.Days < 0 || e.Days > MaxDays {
		return fmt.Errorf("=%s-%s: days %d must be from 1 to %d", e.Package, e.Version, e.Days, MaxDays)
	}
	if e.Line != "" && strings.ContainsAny(e.Line, " \t\n") {
		return fmt.Errorf("=%s-%s: line %q must not contain whitespace", e.Package, e.Version, e.Line)
	}
	if e.Group == "" {
		return nil
	}
	members := strings.Split(e.Group, ",")
	for i, m := range members {
		if err := ValidatePackage(m); err != nil {
			return fmt.Errorf("=%s-%s: group member: %w", e.Package, e.Version, err)
		}
		if i > 0 && members[i-1] >= m {
			return fmt.Errorf("=%s-%s: group %q must be sorted with no duplicate", e.Package, e.Version, e.Group)
		}
	}
	if len(members) < 2 || !groupHas(e.Group, e.Package) {
		return fmt.Errorf("=%s-%s: group %q must list %s and at least one other package", e.Package, e.Version, e.Group, e.Package)
	}
	return nil
}

// render returns the block's text: BEGIN, the header, two lines per entry
// in sorted order, END — every line newline-terminated.
func (b *Block) render() string {
	sortEntries(b.Entries)
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s format=%d days=%d\n%s\n", beginPrefix, FormatVersion, b.Days, headerLine)
	for _, e := range b.Entries {
		fmt.Fprintf(&sb, "%ssince=%s origin=%s", entryPrefix, e.Since.UTC().Format(time.RFC3339), e.Origin)
		if e.Group != "" {
			sb.WriteString(" group=" + e.Group)
		}
		if e.Line != "" {
			sb.WriteString(" line=" + e.Line)
		}
		if !e.First.IsZero() && !e.First.Equal(e.Since) {
			sb.WriteString(" first=" + e.First.UTC().Format(time.RFC3339))
		}
		if e.Days != 0 {
			fmt.Fprintf(&sb, " days=%d", e.Days)
		}
		sb.WriteString("\n" + e.Atom() + "\n")
	}
	sb.WriteString(endLine + "\n")
	return sb.String()
}
