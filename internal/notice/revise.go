package notice

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	yaml "go.yaml.in/yaml/v3"

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
	// Translations add a language or replace one's title, translators and
	// text; the languages not given keep theirs.
	Translations []Translation
}

func (c Changes) given() bool {
	return c.Severity != "" || c.Title != "" || c.Summary != "" || c.Affects != nil ||
		len(c.Translations) > 0
}

// Revise opens the text of notice id in edit, applies changes, and rewrites
// the news item in overlay — the English file and every translation — and,
// when sitePath is not empty, the site file, each replaced atomically. The
// news item's Revision goes up by one in every language and the site file's
// updated becomes now in UTC; Posted, published and the ID stay. A
// translation not given in changes keeps its title and text but takes the
// English headers, with a warning when the English title or text changed.
// Nothing is written when the text comes back unchanged and no field was
// given, or when any check fails; when a write fails, the files already
// replaced are restored.
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
	cur.Translations, err = readTranslations(filepath.Dir(newsPath), id)
	if err != nil {
		return Result{}, err
	}

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
	if next.Body != trimBlankLines(cur.Body) || next.Title != cur.Title {
		for _, lang := range staleTranslations(cur.Translations, changes.Translations) {
			warnings = append(warnings, fmt.Sprintf(
				"translation %s keeps its old title and text; update it with --translation %s=<file>", lang, lang))
		}
	}

	news, renderWarnings := RenderNews(next)
	res := Result{Warnings: append(warnings, renderWarnings...)}
	var writes []pendingWrite
	if siteFile != "" {
		yamlDoc, err := RenderSiteYAML(next)
		if err != nil {
			return Result{}, err
		}
		// The site file first: if it cannot be replaced, nothing changed.
		writes = append(writes, pendingWrite{siteFile, yamlDoc, siteData, "site notice"})
		res.YAML = yamlDoc
	}
	writes = append(writes, pendingWrite{newsPath, []byte(news), newsData, "news item"})
	translated, err := translationWrites(next, cur.Translations, overlay)
	if err != nil {
		return Result{}, err
	}
	writes = append(writes, translated...)
	res.Paths = []string{newsPath}
	for _, w := range translated {
		res.Paths = append(res.Paths, w.path)
	}
	if err := writeAll(writes); err != nil {
		return Result{}, err
	}
	if siteFile != "" {
		res.Paths = append(res.Paths, siteFile)
	}
	return res, nil
}

// translationWrites renders every translation of next, with the content of
// each one cur already had, to restore on a rollback.
func translationWrites(next Notice, cur []Translation, overlay string) ([]pendingWrite, error) {
	out := make([]pendingWrite, 0, len(next.Translations))
	for _, t := range next.Translations {
		path := TranslationPath(overlay, next.ID, t.Lang)
		var old []byte
		if slices.ContainsFunc(cur, func(c Translation) bool { return c.Lang == t.Lang }) {
			var err error
			if old, err = os.ReadFile(path); err != nil { //nolint:gosec // G304: a file readTranslations listed in the item's directory
				return nil, fmt.Errorf("reading translated news item %s: %w", path, err)
			}
		}
		out = append(out, pendingWrite{path, []byte(RenderTranslation(next, t)), old, "translated news item"})
	}
	return out, nil
}

// pendingWrite is one file Revise replaces: its new content, and the old one
// to restore — nil for a file that did not exist.
type pendingWrite struct {
	path     string
	data     []byte
	old      []byte
	whatFile string
}

// writeAll replaces every file in order. When one fails, the files already
// written get their old content back (or are removed, when new), so the
// item is never left with languages at different revisions.
func writeAll(writes []pendingWrite) error {
	for i, w := range writes {
		err := fileutil.WriteFileAtomic(w.path, w.data, 0o644)
		if err == nil {
			continue
		}
		errs := []error{fmt.Errorf("replacing %s %s: %w", w.whatFile, w.path, err)}
		for _, done := range slices.Backward(writes[:i]) {
			if done.old == nil {
				if rmErr := os.Remove(done.path); rmErr != nil {
					errs = append(errs, fmt.Errorf("removing %s %s failed: %w", done.whatFile, done.path, rmErr))
					continue
				}
			} else if rbErr := fileutil.WriteFileAtomic(done.path, done.old, 0o644); rbErr != nil {
				errs = append(errs, fmt.Errorf("restoring %s %s failed: %w", done.whatFile, done.path, rbErr))
				continue
			}
			errs = append(errs, fmt.Errorf("rolled back: restored %s %s", done.whatFile, done.path))
		}
		return errors.Join(errs...)
	}
	return nil
}

// mergeTranslations returns cur with every given translation added or put in
// place of the one in its language, ordered by language.
func mergeTranslations(cur, given []Translation) []Translation {
	out := slices.Clone(cur)
	for _, g := range given {
		if i := slices.IndexFunc(out, func(t Translation) bool { return t.Lang == g.Lang }); i >= 0 {
			out[i] = g
		} else {
			out = append(out, g)
		}
	}
	sortTranslations(out)
	return out
}

// staleTranslations lists the languages of cur that given does not replace.
func staleTranslations(cur, given []Translation) []string {
	var out []string
	for _, t := range cur {
		if !slices.ContainsFunc(given, func(g Translation) bool { return g.Lang == t.Lang }) {
			out = append(out, t.Lang)
		}
	}
	return out
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
	if err := validateTranslations(c.Translations); err != nil {
		return Notice{}, nil, err
	}
	next.Translations = mergeTranslations(cur.Translations, c.Translations)
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
