package notices

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/ebuild"
)

// feedVersion is the only JSON Feed version this client reads.
const feedVersion = "https://jsonfeed.org/version/1.1"

// The patterns below mirror the site's notice schema
// (src/content/noticeSchema.ts), so an item the site publishes is an item the
// tray accepts.
var (
	// idPattern is a GLEP 42 news item name: YYYY-MM-DD-<short-name>. The
	// date must also be a real calendar date, checked separately.
	idPattern = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})-[a-z0-9+_-]{1,20}$`)
	// cpPattern is category/package.
	cpPattern = regexp.MustCompile(`^[A-Za-z0-9+_.-]+/[A-Za-z0-9+_-]+$`)
	// slotPattern is a slot without a subslot: the tray compares slots only.
	slotPattern = regexp.MustCompile(`^[A-Za-z0-9+_.-]+$`)
)

var (
	noticeTypes = map[string]bool{"security": true, "release": true, "news": true, "announcement": true}
	severities  = map[string]bool{"info": true, "warning": true, "critical": true}
	rangeOps    = map[string]bool{"<": true, "<=": true, "=": true, ">=": true, ">": true}
)

// wireFeed is the JSON Feed 1.1 document as the site publishes it. Fields the
// tray does not read are ignored. Items stay raw so a malformed item can still
// be reported with its ID.
type wireFeed struct {
	Version string             `json:"version"`
	Bentoo  *wireFeedExtension `json:"_bentoo"`
	Items   *[]json.RawMessage `json:"items"`
}

type wireFeedExtension struct {
	Serial  int64  `json:"serial"`
	Expires string `json:"expires"`
}

type wireItem struct {
	ID            string             `json:"id"`
	URL           string             `json:"url"`
	Title         string             `json:"title"`
	Summary       string             `json:"summary"`
	DatePublished string             `json:"date_published"`
	DateModified  string             `json:"date_modified"`
	Bentoo        *wireItemExtension `json:"_bentoo"`
}

type wireItemExtension struct {
	Type     string        `json:"type"`
	Severity string        `json:"severity"`
	Affects  []wireAffects `json:"affects"`
}

type wireAffects struct {
	CP     string      `json:"cp"`
	Slot   *string     `json:"slot"`
	Ranges []wireRange `json:"ranges"`
}

type wireRange struct {
	Op  string `json:"op"`
	Ver string `json:"ver"`
}

// ParseFeed decodes and validates a JSON Feed 1.1 document carrying the bentoo
// _bentoo extension. Every error wraps ErrInvalidFeed; an item that breaks the
// contract is reported as an *ItemError naming its ID and the failing field.
// A feed is all or nothing: one bad item rejects the whole document, so the
// caller keeps its previous notices.
func ParseFeed(data []byte) (Feed, error) {
	var w wireFeed
	if err := json.Unmarshal(data, &w); err != nil {
		return Feed{}, fmt.Errorf("%w: decode JSON: %w", ErrInvalidFeed, err)
	}
	if w.Version != feedVersion {
		return Feed{}, fmt.Errorf("%w: version %q, want %q", ErrInvalidFeed, w.Version, feedVersion)
	}
	if w.Bentoo == nil {
		return Feed{}, fmt.Errorf("%w: missing _bentoo", ErrInvalidFeed)
	}
	if w.Bentoo.Serial <= 0 {
		return Feed{}, fmt.Errorf("%w: _bentoo.serial %d is not positive", ErrInvalidFeed, w.Bentoo.Serial)
	}
	if w.Bentoo.Expires == "" {
		return Feed{}, fmt.Errorf("%w: missing _bentoo.expires", ErrInvalidFeed)
	}
	expires, err := time.Parse(time.RFC3339, w.Bentoo.Expires)
	if err != nil {
		return Feed{}, fmt.Errorf("%w: _bentoo.expires: %w", ErrInvalidFeed, err)
	}
	if w.Items == nil {
		return Feed{}, fmt.Errorf("%w: missing items", ErrInvalidFeed)
	}

	feed := Feed{Serial: w.Bentoo.Serial, Expires: expires, Items: make([]Notice, 0, len(*w.Items))}
	seen := make(map[string]bool, len(*w.Items))
	for _, raw := range *w.Items {
		n, err := parseItem(raw)
		if err != nil {
			return Feed{}, err
		}
		// The ID is the notice's identity: read state is keyed by it, so two
		// items sharing one cannot both be honoured.
		if seen[n.ID] {
			return Feed{}, &ItemError{ID: n.ID, Field: "id", Reason: "duplicate id"}
		}
		seen[n.ID] = true
		feed.Items = append(feed.Items, n)
	}
	return feed, nil
}

// parseItem decodes and validates one feed item.
func parseItem(raw json.RawMessage) (Notice, error) {
	var w wireItem
	if err := json.Unmarshal(raw, &w); err != nil {
		return Notice{}, decodeItemError(raw, err)
	}
	if err := validateID(w.ID); err != nil {
		return Notice{}, &ItemError{ID: w.ID, Field: "id", Reason: err.Error()}
	}
	itemErr := func(field, reason string) error {
		return &ItemError{ID: w.ID, Field: field, Reason: reason}
	}

	published, err := time.Parse(time.RFC3339, w.DatePublished)
	if err != nil {
		return Notice{}, itemErr("date_published", "not an RFC 3339 timestamp")
	}
	// JSON Feed makes date_modified optional; an unmodified notice was last
	// updated when it was published.
	updated := published
	if w.DateModified != "" {
		if updated, err = time.Parse(time.RFC3339, w.DateModified); err != nil {
			return Notice{}, itemErr("date_modified", "not an RFC 3339 timestamp")
		}
	}

	if w.Bentoo == nil {
		return Notice{}, itemErr("_bentoo", "missing")
	}
	if !noticeTypes[w.Bentoo.Type] {
		return Notice{}, itemErr("_bentoo.type", fmt.Sprintf("%q is not one of security, release, news, announcement", w.Bentoo.Type))
	}
	if !severities[w.Bentoo.Severity] {
		return Notice{}, itemErr("_bentoo.severity", fmt.Sprintf("%q is not one of info, warning, critical", w.Bentoo.Severity))
	}

	affects := make([]Affects, 0, len(w.Bentoo.Affects))
	for i, wa := range w.Bentoo.Affects {
		field := fmt.Sprintf("_bentoo.affects[%d]", i)
		if !cpPattern.MatchString(wa.CP) {
			return Notice{}, itemErr(field+".cp", fmt.Sprintf("%q is not category/package", wa.CP))
		}
		a := Affects{CP: wa.CP, Ranges: make([]Range, 0, len(wa.Ranges))}
		if wa.Slot != nil {
			if !slotPattern.MatchString(*wa.Slot) {
				return Notice{}, itemErr(field+".slot", fmt.Sprintf("%q is not a slot without a subslot", *wa.Slot))
			}
			a.Slot = *wa.Slot
		}
		for j, wr := range wa.Ranges {
			rangeField := fmt.Sprintf("%s.ranges[%d]", field, j)
			if err := validateOp(wr.Op); err != nil {
				return Notice{}, itemErr(rangeField+".op", err.Error())
			}
			if err := validateVer(wr.Ver); err != nil {
				return Notice{}, itemErr(rangeField+".ver", err.Error())
			}
			a.Ranges = append(a.Ranges, Range(wr))
		}
		affects = append(affects, a)
	}

	return Notice{
		ID:        w.ID,
		Title:     w.Title,
		Summary:   w.Summary,
		URL:       w.URL,
		Type:      w.Bentoo.Type,
		Severity:  w.Bentoo.Severity,
		Affects:   affects,
		Published: published,
		Updated:   updated,
		Source:    SourceFeed,
	}, nil
}

// decodeItemError turns a failure to decode an item into an *ItemError. The
// ID is recovered on a best-effort basis, and a JSON type error names the
// field it hit.
func decodeItemError(raw json.RawMessage, err error) error {
	var probe struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &probe) // best effort: an undecodable id stays empty
	field := "item"
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) && typeErr.Field != "" {
		field = typeErr.Field
	}
	return &ItemError{ID: probe.ID, Field: field, Reason: err.Error()}
}

// validateID checks the GLEP 42 name shape and that its date exists.
func validateID(id string) error {
	m := idPattern.FindStringSubmatch(id)
	if m == nil {
		return errors.New("not YYYY-MM-DD-<short-name> with a short name of 1 to 20 characters of [a-z0-9+_-]")
	}
	if _, err := time.Parse(time.DateOnly, m[1]); err != nil {
		return errors.New("does not start with a valid calendar date")
	}
	return nil
}

// ParseAffectsRange builds a Range from an operator and a version bound. The
// operator must be one of < <= = >= > and the version a PMS version written
// without surrounding whitespace: ebuild.CompareVersions sorts an invalid
// version below every valid one, so an invalid bound would silently match.
func ParseAffectsRange(op, ver string) (Range, error) {
	if err := validateOp(op); err != nil {
		return Range{}, err
	}
	if err := validateVer(ver); err != nil {
		return Range{}, err
	}
	return Range{Op: op, Ver: ver}, nil
}

func validateOp(op string) error {
	if !rangeOps[op] {
		return fmt.Errorf("operator %q is not one of <, <=, =, >=, >", op)
	}
	return nil
}

func validateVer(ver string) error {
	if ver != strings.TrimSpace(ver) || !ebuild.IsValidVersion(ver) {
		return fmt.Errorf("version %q is not a PMS version", ver)
	}
	return nil
}
