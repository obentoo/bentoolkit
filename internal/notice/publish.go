package notice

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"

	"github.com/obentoo/bentoolkit/internal/common/fileutil"
)

// Errors of Publish and Revise.
var (
	ErrBadID          = errors.New("invalid notice ID")
	ErrNewsExists     = errors.New("the overlay already has a news item with this ID")
	ErrSiteDirMissing = errors.New("the site has no notices directory")
	ErrSiteExists     = errors.New("the site already has a notice file with this ID")
)

// idRe is the notice ID, `YYYY-MM-DD-<name>`. Every path Publish and Revise
// build is `<fixed base>/<id>…`, so an ID that matches this pattern — no `/`,
// no `..` — cannot leave the base it is joined to.
var idRe = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}-[a-z0-9+_-]{1,20}$`)

// Result is what a publish or a revision wrote.
type Result struct {
	// Paths lists every file written, the news item first.
	Paths []string
	// YAML is the site document, for the command to print when no
	// notice.site_path is configured (R4.2).
	YAML []byte
	// Warnings are the news renderer's (R3.5).
	Warnings []string
}

// NewsPath is the news item of id in overlay:
// metadata/news/<id>/<id>.en.txt (R3.1).
func NewsPath(overlay, id string) string {
	return filepath.Join(overlay, "metadata", "news", id, id+".en.txt")
}

// SiteNoticesDir is where the site keeps its notice files.
func SiteNoticesDir(sitePath string) string {
	return filepath.Join(sitePath, "src", "content", "notices")
}

// SitePath is the site notice file of id (R4.1).
func SitePath(sitePath, id string) string {
	return filepath.Join(SiteNoticesDir(sitePath), id+".yaml")
}

// Publish writes n as a news item in overlay and, when sitePath is not empty,
// as a site notice file. Every destination is checked before anything is
// written (R1.7, R1.8, R4.3, R4.4); both files are created without replacing
// anything; and when the site file cannot be written the news item is removed
// again, so the ID stays free (R4.5).
func Publish(ctx context.Context, n Notice, overlay, sitePath string) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, fmt.Errorf("publishing notice %s: %w", n.ID, err)
	}
	if !idRe.MatchString(n.ID) {
		return Result{}, fmt.Errorf("notice ID %q is not YYYY-MM-DD-<name>: %w", n.ID, ErrBadID)
	}

	newsPath := NewsPath(overlay, n.ID)
	newsDir := filepath.Dir(newsPath)
	// The whole directory is the ID: another language's file alone still
	// takes it.
	if _, err := os.Lstat(newsDir); err == nil {
		return Result{}, fmt.Errorf("%s: %w", newsDir, ErrNewsExists)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Result{}, fmt.Errorf("checking for an existing news item at %s: %w", newsDir, err)
	}

	var siteFile string
	if sitePath != "" {
		dir := SiteNoticesDir(sitePath)
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			return Result{}, fmt.Errorf("%s: %w", dir, errors.Join(ErrSiteDirMissing, err))
		}
		siteFile = SitePath(sitePath, n.ID)
		if _, err := os.Lstat(siteFile); err == nil {
			return Result{}, fmt.Errorf("%s: %w", siteFile, ErrSiteExists)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return Result{}, fmt.Errorf("checking for an existing site notice at %s: %w", siteFile, err)
		}
	}

	news, warnings := RenderNews(n)
	yamlDoc, err := RenderSiteYAML(n)
	if err != nil {
		return Result{}, err
	}

	if err := createNewsDir(newsDir); err != nil {
		return Result{}, err
	}
	if err := fileutil.PublishNewFile(newsPath, []byte(news), 0o644); err != nil {
		return Result{}, errors.Join(fmt.Errorf("writing news item %s: %w", newsPath, err), removeNewsDir(newsDir))
	}
	res := Result{Paths: []string{newsPath}, YAML: yamlDoc, Warnings: warnings}
	if siteFile == "" {
		return res, nil
	}

	if err := fileutil.PublishNewFile(siteFile, yamlDoc, 0o644); err != nil {
		writeErr := fmt.Errorf("writing site notice %s: %w", siteFile, err)
		if rbErr := removeNewsDir(newsDir); rbErr != nil {
			return Result{}, errors.Join(writeErr, fmt.Errorf("rolling back news item %s failed: %w", newsPath, rbErr))
		}
		return Result{}, errors.Join(writeErr, fmt.Errorf("rolled back: removed news item %s", newsPath))
	}
	res.Paths = append(res.Paths, siteFile)
	return res, nil
}

// createNewsDir creates the item's directory, 0755 whatever the umask (R3.7).
// os.Mkdir, unlike a check followed by MkdirAll, fails when the directory
// appeared since the pre-check, so a concurrent publish of the same ID loses.
func createNewsDir(dir string) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil { //nolint:gosec // G301: metadata/news is read by portage, which needs 0755
		return fmt.Errorf("creating %s: %w", filepath.Dir(dir), err)
	}
	if err := os.Mkdir(dir, 0o755); err != nil { //nolint:gosec // G301: a news item directory is read by portage, which needs 0755
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%s: %w", dir, ErrNewsExists)
		}
		return fmt.Errorf("creating news item directory %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o755); err != nil { //nolint:gosec // G302: see above
		return errors.Join(fmt.Errorf("setting the mode of %s: %w", dir, err), removeNewsDir(dir))
	}
	return nil
}

// removeNewsDir removes the item directory this publish created, with the
// file in it.
func removeNewsDir(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("removing %s: %w", dir, err)
	}
	return nil
}
