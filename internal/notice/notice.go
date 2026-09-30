// Package notice authors bentoo notices: one input becomes a GLEP 42 news item
// in the overlay and a YAML file in the site repository, sharing one ID
// (story 071).
//
// The package is pure where it can be — validation and both renderers touch no
// disk — so every file format is pinned by tests before anything is written.
// It prints nothing and logs nothing: errors and warnings are returned to the
// command, which owns the terminal.
package notice

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Limits of the GLEP 42 news item and the site schema (site story 002, R2).
const (
	maxNameLen    = 20
	maxTitleLen   = 50
	maxSummaryLen = 300
	dateLayout    = "2006-01-02"
)

// Sentinel errors. Every error New returns wraps one of these and names the
// flag or field involved, so a caller can branch with errors.Is and a person
// can read which input to fix.
var (
	ErrType            = errors.New("invalid notice type")
	ErrSeverity        = errors.New("invalid notice severity")
	ErrName            = errors.New("invalid notice name")
	ErrTitle           = errors.New("invalid notice title")
	ErrSummary         = errors.New("invalid notice summary")
	ErrPublished       = errors.New("invalid publication date")
	ErrFuture          = errors.New("publication date is in the future")
	ErrControlChar     = errors.New("forbidden control character")
	ErrAffectsRequired = errors.New("this notice type needs at least one --affects")
)

var (
	allowedTypes      = []string{"security", "release", "news", "announcement"}
	allowedSeverities = []string{"info", "warning", "critical"}

	// nameRe is the GLEP 42 short-name alphabet; the length is checked apart
	// so the error can say which rule failed.
	nameRe = regexp.MustCompile(`^[a-z0-9+_-]+$`)
)

// Input is the raw, unvalidated notice as the command received it: flag values
// verbatim, plus the body read from a file or the editor.
type Input struct {
	Type, Severity, Name, Title, Summary, Published, Author string
	Affects                                                 []string
	Body                                                    string
}

// Range is one version bound of an affects entry: an operator from <, <=, =,
// >=, > and a Gentoo version.
type Range struct {
	Op, Ver string
}

// Affects is one affected package: a category/package, an optional slot, and
// zero or more ranges that combine with AND.
type Affects struct {
	CP, Slot string
	Ranges   []Range
}

// Notice is a validated notice, the single source both files are rendered from.
type Notice struct {
	ID, Type, Severity, Title, Summary, Author, Body string
	Affects                                          []Affects
	// Published and Updated are UTC. A new notice has both at midnight of
	// its publication date; a revision moves Updated only.
	Published, Updated time.Time
	// Revision is the news item's Revision header: 1 for a new notice.
	Revision int
}

// New validates in and returns the notice it describes, with Revision 1 and
// Updated equal to Published. now supplies "today" (in UTC) for the default
// and the future-date check. The first rule broken is returned, wrapping its
// sentinel and naming the flag.
func New(in Input, now time.Time) (Notice, error) {
	if !slices.Contains(allowedTypes, in.Type) {
		return Notice{}, fmt.Errorf("--type %q: must be one of %s: %w",
			in.Type, strings.Join(allowedTypes, ", "), ErrType)
	}
	if err := ValidateSeverity(in.Severity); err != nil {
		return Notice{}, err
	}
	if err := validateName(in.Name); err != nil {
		return Notice{}, err
	}
	if err := ValidateTitle(in.Title); err != nil {
		return Notice{}, err
	}
	if err := ValidateSummary(in.Summary); err != nil {
		return Notice{}, err
	}
	if err := checkText("body", in.Body, false); err != nil {
		return Notice{}, err
	}
	if err := checkText("--author", in.Author, true); err != nil {
		return Notice{}, err
	}
	published, err := publicationDate(in.Published, now)
	if err != nil {
		return Notice{}, err
	}
	affects, err := parseAllAffects(in.Affects)
	if err != nil {
		return Notice{}, err
	}
	if (in.Type == "security" || in.Type == "release") && len(affects) == 0 {
		return Notice{}, fmt.Errorf("--type %s: %w", in.Type, ErrAffectsRequired)
	}

	return Notice{
		ID:        published.Format(dateLayout) + "-" + in.Name,
		Type:      in.Type,
		Severity:  in.Severity,
		Title:     in.Title,
		Summary:   in.Summary,
		Author:    in.Author,
		Body:      in.Body,
		Affects:   affects,
		Published: published,
		Updated:   published,
		Revision:  1,
	}, nil
}

// ValidateSeverity checks a --severity value (R1.2).
func ValidateSeverity(s string) error {
	if !slices.Contains(allowedSeverities, s) {
		return fmt.Errorf("--severity %q: must be one of %s: %w",
			s, strings.Join(allowedSeverities, ", "), ErrSeverity)
	}
	return nil
}

// ValidateTitle checks a --title value: 1..50 code points, single line, no
// forbidden control character (R1.4, R1.10, R1.12).
func ValidateTitle(s string) error {
	n := utf8.RuneCountInString(s)
	if n == 0 || n > maxTitleLen {
		return fmt.Errorf("--title: %d code points given, the limit is 1..%d: %w", n, maxTitleLen, ErrTitle)
	}
	return checkText("--title", s, true)
}

// ValidateSummary checks a --summary value: 1..300 code points, single line,
// no forbidden control character (R1.9, R1.10, R1.12).
func ValidateSummary(s string) error {
	n := utf8.RuneCountInString(s)
	if n == 0 || n > maxSummaryLen {
		return fmt.Errorf("--summary: %d code points given, the limit is 1..%d: %w", n, maxSummaryLen, ErrSummary)
	}
	return checkText("--summary", s, true)
}

func validateName(s string) error {
	switch {
	case s == "":
		return fmt.Errorf("--name is empty; it must be 1..%d characters of [a-z0-9+_-]: %w", maxNameLen, ErrName)
	case len(s) > maxNameLen:
		return fmt.Errorf("--name %q is %d characters, the limit is %d: %w", s, len(s), maxNameLen, ErrName)
	case !nameRe.MatchString(s):
		return fmt.Errorf("--name %q: only [a-z0-9+_-] is allowed: %w", s, ErrName)
	}
	return nil
}

// checkText refuses C0 controls other than tab and line feed, and the
// noncharacters U+FFFE and U+FFFF (R1.10). A single-line field refuses tab
// and line feed too (R1.12): the news item's Title header is one line.
func checkText(field, s string, singleLine bool) error {
	for i, r := range s {
		bad := r == 0xFFFE || r == 0xFFFF || (r < 0x20 && r != '\t' && r != '\n')
		if singleLine && (r == '\t' || r == '\n') {
			bad = true
		}
		if bad {
			return fmt.Errorf("%s: character %U at byte %d is not allowed: %w", field, r, i, ErrControlChar)
		}
	}
	return nil
}

// publicationDate parses --published (YYYY-MM-DD), defaulting to today in UTC,
// and refuses a date after today in UTC (R1.11).
func publicationDate(s string, now time.Time) (time.Time, error) {
	y, m, d := now.UTC().Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	if s == "" {
		return today, nil
	}
	date, err := time.ParseInLocation(dateLayout, s, time.UTC)
	if err != nil {
		return time.Time{}, fmt.Errorf("--published %q: want YYYY-MM-DD: %w", s, errors.Join(ErrPublished, err))
	}
	if date.After(today) {
		return time.Time{}, fmt.Errorf("--published %s is after today (%s, UTC): %w",
			s, today.Format(dateLayout), ErrFuture)
	}
	return date, nil
}

func parseAllAffects(raw []string) ([]Affects, error) {
	out := make([]Affects, 0, len(raw))
	for _, s := range raw {
		a, err := ParseAffects(s)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}
