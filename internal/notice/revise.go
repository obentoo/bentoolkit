package notice

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/obentoo/bentoolkit/internal/common/ebuild"
	"github.com/obentoo/bentoolkit/internal/common/fileutil"
)

// Errors of Revise.
var (
	ErrNotFound        = errors.New("no news item with this ID")
	ErrSiteMissing     = errors.New("the site has no notice file with this ID")
	ErrNothingToRevise = errors.New("nothing to revise: the text is unchanged and no field was given")
	ErrMalformedNews   = errors.New("malformed news item")
	ErrMalformedSite   = errors.New("malformed site notice")
)

// Changes are the fields `notice revise` replaces. An empty string or a nil
// slice means the flag was not given.
type Changes struct {
	Severity, Title, Summary string
	Affects                  []string
}

func (c Changes) given() bool {
	return c.Severity != "" || c.Title != "" || c.Summary != "" || c.Affects != nil
}

// Revise opens the text of notice id in edit, applies changes, and rewrites
// the news item in overlay and — when sitePath is not empty — the site file,
// each replaced atomically. The news item's Revision goes up by one and
// the site file's updated becomes now in UTC; Posted, published and the ID
// stay. Nothing is written when the text comes back unchanged and no
// field was given, or when any check fails.
func Revise(ctx context.Context, id string, changes Changes, overlay, sitePath string,
	edit BodyEditor, now time.Time,
) (Result, error) {
	if !idRe.MatchString(id) {
		return Result{}, fmt.Errorf("notice ID %q is not YYYY-MM-DD-<name>: %w", id, ErrBadID)
	}

	newsPath := NewsPath(overlay, id)
	newsData, err := os.ReadFile(newsPath) //nolint:gosec // G304: an idRe-confined ID under the configured overlay's metadata/news
	if errors.Is(err, fs.ErrNotExist) {
		return Result{}, fmt.Errorf("%s: %w", newsPath, ErrNotFound)
	} else if err != nil {
		return Result{}, fmt.Errorf("reading news item %s: %w", newsPath, err)
	}
	cur, err := parseNews(string(newsData))
	if err != nil {
		return Result{}, fmt.Errorf("news item %s: %w", newsPath, err)
	}
	cur.ID = id

	var siteFile string
	var siteData []byte
	if sitePath != "" {
		siteFile = SitePath(sitePath, id)
		siteData, err = os.ReadFile(siteFile) //nolint:gosec // G304: an idRe-confined ID under the configured site's notices directory
		if errors.Is(err, fs.ErrNotExist) {
			return Result{}, fmt.Errorf("%s: %w", siteFile, ErrSiteMissing)
		} else if err != nil {
			return Result{}, fmt.Errorf("reading site notice %s: %w", siteFile, err)
		}
		// The site file is the precise record — the body as written, the
		// ranges as given — so it wins over what the news item could keep.
		if err := mergeSite(&cur, siteData); err != nil {
			return Result{}, fmt.Errorf("site notice %s: %w", siteFile, err)
		}
	}

	next, warnings, err := applyChanges(cur, changes, sitePath != "")
	if err != nil {
		return Result{}, err
	}

	edited, err := edit(ctx, cur.Body)
	if err != nil {
		return Result{}, fmt.Errorf("editing the text of notice %s: %w", id, err)
	}
	next.Body = trimBlankLines(edited)
	if next.Body == trimBlankLines(cur.Body) && !changes.given() {
		return Result{}, fmt.Errorf("notice %s: %w", id, ErrNothingToRevise)
	}
	if next.Body == "" {
		return Result{}, fmt.Errorf("notice %s: %w", id, ErrEmptyBody)
	}
	if err := checkText("body", next.Body, false); err != nil {
		return Result{}, err
	}
	next.Revision = cur.Revision + 1
	next.Updated = now.UTC().Truncate(time.Second)

	news, renderWarnings := RenderNews(next)
	res := Result{Warnings: append(warnings, renderWarnings...)}
	if siteFile != "" {
		yamlDoc, err := RenderSiteYAML(next)
		if err != nil {
			return Result{}, err
		}
		// The site file first: if it cannot be replaced, nothing changed.
		if err := fileutil.WriteFileAtomic(siteFile, yamlDoc, 0o644); err != nil {
			return Result{}, fmt.Errorf("replacing site notice %s: %w", siteFile, err)
		}
		res.YAML = yamlDoc
	}
	if err := fileutil.WriteFileAtomic(newsPath, []byte(news), 0o644); err != nil {
		writeErr := fmt.Errorf("replacing news item %s: %w", newsPath, err)
		if siteFile == "" {
			return Result{}, writeErr
		}
		if rbErr := fileutil.WriteFileAtomic(siteFile, siteData, 0o644); rbErr != nil {
			return Result{}, errors.Join(writeErr, fmt.Errorf("restoring site notice %s failed: %w", siteFile, rbErr))
		}
		return Result{}, errors.Join(writeErr, fmt.Errorf("rolled back: restored site notice %s", siteFile))
	}
	res.Paths = []string{newsPath}
	if siteFile != "" {
		res.Paths = append(res.Paths, siteFile)
	}
	return res, nil
}

// applyChanges returns cur with every given field replaced, each validated as
// New validates it.
func applyChanges(cur Notice, c Changes, hasSite bool) (Notice, []string, error) {
	next := cur
	var warnings []string
	if c.Severity != "" {
		if err := ValidateSeverity(c.Severity); err != nil {
			return Notice{}, nil, err
		}
		next.Severity = c.Severity
	}
	if c.Title != "" {
		if err := ValidateTitle(c.Title); err != nil {
			return Notice{}, nil, err
		}
		next.Title = c.Title
	}
	if c.Summary != "" {
		if err := ValidateSummary(c.Summary); err != nil {
			return Notice{}, nil, err
		}
		next.Summary = c.Summary
	}
	if c.Affects != nil {
		affects, err := parseAllAffects(c.Affects)
		if err != nil {
			return Notice{}, nil, err
		}
		next.Affects = affects
	}
	if !hasSite && (c.Severity != "" || c.Summary != "") {
		warnings = append(warnings,
			"--severity and --summary live in the site file only, and no notice.site_path is configured")
	}
	return next, warnings, nil
}

// parseNews reads back a news item this package wrote: the headers up to the
// first blank line, then the text. Only the header block is searched, so a
// text line such as "Revision: 9" stays text.
func parseNews(text string) (Notice, error) {
	head, body, ok := strings.Cut(text, "\n\n")
	if !ok {
		return Notice{}, fmt.Errorf("no blank line after the headers: %w", ErrMalformedNews)
	}
	var n Notice
	for line := range strings.SplitSeq(head, "\n") {
		name, value, ok := strings.Cut(line, ": ")
		if !ok {
			return Notice{}, fmt.Errorf("header line %q: %w", line, ErrMalformedNews)
		}
		switch name {
		case "Title":
			n.Title = value
		case "Author":
			n.Author = value
		case "Posted":
			posted, err := time.ParseInLocation(dateLayout, value, time.UTC)
			if err != nil {
				return Notice{}, fmt.Errorf("header Posted %q: %w", value, errors.Join(ErrMalformedNews, err))
			}
			n.Published, n.Updated = posted, posted
		case "Revision":
			rev, err := strconv.Atoi(value)
			if err != nil || rev < 1 {
				return Notice{}, fmt.Errorf("header Revision %q: %w", value, errors.Join(ErrMalformedNews, err))
			}
			n.Revision = rev
		case "Display-If-Installed":
			a, err := parseAtom(value)
			if err != nil {
				return Notice{}, err
			}
			n.Affects = append(n.Affects, a)
		}
	}
	if n.Revision == 0 || n.Published.IsZero() {
		return Notice{}, fmt.Errorf("missing Posted or Revision header: %w", ErrMalformedNews)
	}
	n.Body = strings.TrimRight(body, "\n")
	return n, nil
}

// parseAtom reverses newsAtom for the two forms it writes: `cat/pkg[:slot]`
// and `<op>cat/pkg-<ver>[:slot]`.
func parseAtom(atom string) (Affects, error) {
	bad := func() (Affects, error) {
		return Affects{}, fmt.Errorf("Display-If-Installed %q: %w", atom, ErrMalformedNews)
	}
	op := ""
	for _, o := range rangeOps {
		if strings.HasPrefix(atom, o) {
			op = o
			break
		}
	}
	rest := atom[len(op):]
	pkg, slot, _ := strings.Cut(rest, ":")
	a := Affects{CP: pkg, Slot: slot}
	if op != "" {
		// The version starts at the first `-` whose remainder is a version.
		found := false
		for i := strings.IndexByte(pkg, '/'); i < len(pkg); i++ {
			if i > 0 && pkg[i] == '-' && ebuild.IsValidVersion(pkg[i+1:]) {
				a.CP, a.Ranges = pkg[:i], []Range{{Op: op, Ver: pkg[i+1:]}}
				found = true
				break
			}
		}
		if !found {
			return bad()
		}
	}
	if !cpRe.MatchString(a.CP) || (slot != "" && !slotRe.MatchString(slot)) {
		return bad()
	}
	return a, nil
}

// siteDoc is the site file as RenderSiteYAML writes it.
type siteDoc struct {
	ID        string `yaml:"id"`
	Type      string `yaml:"type"`
	Severity  string `yaml:"severity"`
	Title     string `yaml:"title"`
	Summary   string `yaml:"summary"`
	Body      string `yaml:"body"`
	Published string `yaml:"published"`
	Affects   []struct {
		CP     string `yaml:"cp"`
		Slot   string `yaml:"slot"`
		Ranges []struct {
			Op  string `yaml:"op"`
			Ver string `yaml:"ver"`
		} `yaml:"ranges"`
	} `yaml:"affects"`
}

// mergeSite overlays the site file's fields on n, which was read from the news
// item: type, severity and summary exist only there, and its body and affects
// are the unwrapped, precise ones.
func mergeSite(n *Notice, data []byte) error {
	var d siteDoc
	if err := yaml.Unmarshal(data, &d); err != nil {
		return fmt.Errorf("decoding: %w", errors.Join(ErrMalformedSite, err))
	}
	if d.ID != n.ID {
		return fmt.Errorf("its id is %q, not %q: %w", d.ID, n.ID, ErrMalformedSite)
	}
	published, err := time.Parse(time.RFC3339, d.Published)
	if err != nil {
		return fmt.Errorf("published %q: %w", d.Published, errors.Join(ErrMalformedSite, err))
	}
	n.Type, n.Severity, n.Title, n.Summary = d.Type, d.Severity, d.Title, d.Summary
	n.Body = strings.TrimRight(d.Body, "\n")
	n.Published = published.UTC()
	n.Affects = nil
	for _, a := range d.Affects {
		entry := Affects{CP: a.CP, Slot: a.Slot}
		for _, r := range a.Ranges {
			entry.Ranges = append(entry.Ranges, Range{Op: r.Op, Ver: r.Ver})
		}
		n.Affects = append(n.Affects, entry)
	}
	return nil
}
