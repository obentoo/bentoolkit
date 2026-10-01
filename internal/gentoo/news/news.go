// Package news reads the portage news of one repository as notices, the
// tray's offline source: the unread list portage keeps under
// /var/lib/gentoo/news and the GLEP 42 news items that list names.
//
// Every file is opened read-only; this package never writes portage's news
// files (R4.5).
package news

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/obentoo/bentoolkit/internal/notices"
)

// DefaultReposDir is where a repository lives when repos.conf does not name
// its location: DefaultReposDir/<name>.
const DefaultReposDir = "/var/db/repos"

// maxFileBytes bounds how much of an unread list, a news item or a repos.conf
// file is read. Each is a few hundred bytes in practice; a larger file is
// refused rather than truncated.
const maxFileBytes = 1 << 20

// postedLayout is the GLEP 42 Posted header format, a UTC date.
const postedLayout = "2006-01-02"

// Reader reads the unread news of one repository.
type Reader struct {
	unreadPath, repoPath, feedHost string
	tick                           <-chan time.Time

	once    sync.Once
	changed chan struct{}
}

// NewReader returns a Reader over the unread list at unreadPath and the
// repository at repoPath. feedHost is the notices feed's host, which a news
// notice's URL points at. tick drives Changed's poll (a one-minute ticker in
// production); it may be nil when Changed is not used.
func NewReader(unreadPath, repoPath, feedHost string, tick <-chan time.Time) *Reader {
	return &Reader{
		unreadPath: unreadPath,
		repoPath:   repoPath,
		feedHost:   feedHost,
		tick:       tick,
		changed:    make(chan struct{}, 1),
	}
}

// Unread returns one notice per ID in the unread list, in list order: type
// news, severity info, source SourceNews, titled from the item's Title
// header and dated from its Posted header (midnight UTC).
//
// When the list cannot be read, Unread returns no notices and an error naming
// the list's path. When an item cannot be read or lacks a header, its notice
// is still returned (titled with its ID when the title is unknown) and the
// error, joined with the others, names the item's path: a non-nil error with
// notices is a partial result. A line that is not a news item ID (one holding
// a "/") is never used as a path; it is reported in the error and dropped.
func (r *Reader) Unread(ctx context.Context) ([]notices.Notice, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("read unread news list %s: %w", r.unreadPath, err)
	}
	ids, err := readIDs(r.unreadPath)
	if err != nil {
		return nil, err
	}

	var (
		out  []notices.Notice
		errs []error
	)
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("read unread news list %s: %w", r.unreadPath, err)
		}
		if !validID(id) {
			errs = append(errs, fmt.Errorf("unread news list %s: %q is not a news item ID", r.unreadPath, id))
			continue
		}
		n, err := r.notice(id)
		if err != nil {
			errs = append(errs, err)
		}
		out = append(out, n)
	}
	return out, errors.Join(errs...)
}

// notice builds the notice for id. It always returns a usable notice; the
// error reports what could not be read from the item.
func (r *Reader) notice(id string) (notices.Notice, error) {
	n := notices.Notice{
		ID:       id,
		Title:    id,
		URL:      "https://" + r.feedHost + "/notices/" + url.PathEscape(id) + "/",
		Type:     "news",
		Severity: "info",
		Source:   notices.SourceNews,
	}
	path := filepath.Join(r.repoPath, "metadata", "news", id, id+".en.txt")
	data, err := readFile(path)
	if err != nil {
		return n, fmt.Errorf("read news item %s: %w", path, err)
	}

	title, posted := headers(data)
	var errs []error
	if title != "" {
		n.Title = title
	} else {
		errs = append(errs, fmt.Errorf("news item %s: no Title header", path))
	}
	if t, err := time.Parse(postedLayout, posted); err == nil {
		n.Published, n.Updated = t, t
	} else {
		errs = append(errs, fmt.Errorf("news item %s: Posted header %q: %w", path, posted, err))
	}
	return n, errors.Join(errs...)
}

// headers returns the Title and Posted header values of a GLEP 42 news item.
// The header block ends at the first blank line, so a "Title:" line in the
// body is not a header. The first occurrence of each header wins.
func headers(data []byte) (title, posted string) {
	var haveTitle, havePosted bool
	for line := range strings.Lines(string(data)) {
		line = strings.TrimRight(line, "\r\n")
		if strings.TrimSpace(line) == "" {
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch value = strings.TrimSpace(value); strings.TrimSpace(key) {
		case "Title":
			if !haveTitle {
				title, haveTitle = value, true
			}
		case "Posted":
			if !havePosted {
				posted, havePosted = value, true
			}
		}
	}
	return title, posted
}

// readIDs returns the non-blank lines of the unread list, trimmed, without
// repeats.
func readIDs(path string) ([]string, error) {
	data, err := readFile(path)
	if err != nil {
		return nil, fmt.Errorf("read unread news list %s: %w", path, err)
	}
	var ids []string
	seen := map[string]bool{}
	for line := range strings.Lines(string(data)) {
		id := strings.TrimSpace(line)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids, nil
}

// validID reports whether id can name a directory under metadata/news without
// leaving it: no "/", no NUL, and not "." or "..".
func validID(id string) bool {
	return id != "." && id != ".." && !strings.ContainsAny(id, "/\x00")
}

// Changed returns a channel that receives a value on the first tick after the
// unread list's size or modification time changed, appearing or disappearing
// included. It is buffered, so a change seen while nobody is receiving is kept
// until it is taken, and several changes before that collapse into one.
//
// The first call records the list's current state and starts the poll, which
// stats the list once per tick and stops when the tick channel is closed.
// Every call returns the same channel. With a nil tick the channel never
// receives.
func (r *Reader) Changed() <-chan struct{} {
	r.once.Do(func() {
		if r.tick == nil {
			return
		}
		// The baseline is taken before Changed returns, so a change made
		// right after the call is reported on the next tick.
		go r.poll(r.stamp())
	})
	return r.changed
}

// poll stats the unread list on every tick and signals a change against last.
func (r *Reader) poll(last fileStamp) {
	for range r.tick {
		cur := r.stamp()
		if cur.equal(last) {
			continue
		}
		last = cur
		select {
		case r.changed <- struct{}{}:
		default: // a change is already pending
		}
	}
}

// fileStamp is what Changed compares between ticks.
type fileStamp struct {
	exists bool
	size   int64
	mtime  time.Time
}

func (s fileStamp) equal(o fileStamp) bool {
	return s.exists == o.exists && s.size == o.size && s.mtime.Equal(o.mtime)
}

// stamp stats the unread list. A list that cannot be stat'ed counts as
// absent; Unread reports why when it is next read.
func (r *Reader) stamp() fileStamp {
	fi, err := os.Stat(r.unreadPath)
	if err != nil {
		return fileStamp{}
	}
	return fileStamp{exists: true, size: fi.Size(), mtime: fi.ModTime()}
}

// RepoLocation returns the location of the repository called name, as
// portage's repos.conf at reposConf gives it. reposConf is a file, or a
// directory whose *.conf files are read in lexical order; a later
// definition of the location overrides an earlier one. Only the section whose
// name is exactly name counts.
//
// When repos.conf does not exist or does not name the location, the result is
// DefaultReposDir/<name> and the error is nil. When it cannot be read, the
// result is the same fallback and the error names the path, so a caller can
// warn and go on with the fallback.
func RepoLocation(reposConf string, name string) (string, error) {
	fallback := filepath.Join(DefaultReposDir, name)
	files, err := confFiles(reposConf)
	if err != nil {
		return fallback, err
	}
	location := ""
	for _, path := range files {
		data, err := readFile(path)
		if err != nil {
			return fallback, fmt.Errorf("read repository configuration %s: %w", path, err)
		}
		if loc, ok := sectionLocation(data, name); ok {
			location = loc
		}
	}
	if location == "" {
		return fallback, nil
	}
	return location, nil
}

// confFiles lists the repos.conf files to read: the path itself when it is a
// file, its *.conf entries (sorted, hidden ones skipped) when it is a
// directory, and none when it does not exist.
func confFiles(path string) ([]string, error) {
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read repository configuration %s: %w", path, err)
	}
	if !fi.IsDir() {
		return []string{path}, nil
	}
	// ReadDir returns the entries sorted by name.
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("read repository configuration %s: %w", path, err)
	}
	var files []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".conf") {
			continue
		}
		files = append(files, filepath.Join(path, name))
	}
	return files, nil
}

// sectionLocation returns the last non-empty "location" value inside the INI
// section [name] of data. Keys compare case-insensitively and take "=" or ":"
// as the delimiter, as portage's configparser does; "#" and ";" start a
// comment line.
func sectionLocation(data []byte, name string) (location string, ok bool) {
	inSection := false
	for line := range strings.Lines(string(data)) {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || line[0] == '#' || line[0] == ';':
			continue
		case line[0] == '[' && line[len(line)-1] == ']':
			inSection = line[1:len(line)-1] == name
			continue
		case !inSection:
			continue
		}
		i := strings.IndexAny(line, "=:")
		if i < 0 || !strings.EqualFold(strings.TrimSpace(line[:i]), "location") {
			continue
		}
		if v := strings.TrimSpace(line[i+1:]); v != "" {
			location, ok = v, true
		}
	}
	return location, ok
}

// readFile returns the content of the file at path, opened read-only (R4.5),
// refusing a file larger than maxFileBytes.
func readFile(path string) ([]byte, error) {
	// path is the unread list or repos.conf path the caller configured, an
	// entry ReadDir listed under repos.conf, or a news item path whose ID
	// passed validID.
	f, err := os.Open(path) //nolint:gosec // G304: configured portage path, a ReadDir entry, or a validID-checked item ID under the repository
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck // read-only handle: a failed close cannot lose data

	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxFileBytes {
		return nil, fmt.Errorf("larger than %d bytes", maxFileBytes)
	}
	return data, nil
}
