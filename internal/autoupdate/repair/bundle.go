package repair

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	"github.com/obentoo/bentoolkit/internal/common/secrets"
)

// BundleFile is the repair bundle's file name at the root of a staged tree. It
// is validate's spelling, so the staged sweep keeps the trees bundles live in.
const BundleFile = validate.RepairBundleFile

// SchemaVersion is the only bundle schema this package writes and reads.
const SchemaVersion = 1

// failureBudget bounds the failure text a bundle keeps, in bytes.
const failureBudget = 16 << 10

// ErrUnknownSchema reports a bundle whose schema version this build cannot read.
var ErrUnknownSchema = errors.New("unknown repair bundle schema")

// Bundle is everything a repair session needs about one failed bump. It is
// stored as indented JSON at <StagedRoot>/BundleFile.
type Bundle struct {
	Schema         int    `json:"schema"`
	Package        string `json:"package"`
	CurrentVersion string `json:"current_version"`
	Candidate      string `json:"candidate_version"`
	// Stage is the step that failed: manifest, llm-fix, re-check, vendor or
	// gate:<name>.
	Stage string `json:"failed_stage"`
	// Failure is the failure text, already passed through Redact.
	Failure      string    `json:"failure"`
	RefusedTools []string  `json:"refused_tools,omitempty"`
	StagedRoot   string    `json:"staged_root"`
	EbuildPath   string    `json:"ebuild_path"`
	UpstreamURLs []string  `json:"upstream_urls,omitempty"`
	Created      time.Time `json:"created"`
}

// Redact masks every non-empty value in secretValues with the logger's "***"
// through secrets.Scrub, longest value first so one that contains another is
// masked whole, and then keeps the last failureBudget bytes of the result, cut
// forward to a rune boundary. Masking runs before the cut so a secret that
// straddles the boundary is never written in part.
func Redact(failure string, secretValues []string) string {
	ordered := slices.Clone(secretValues)
	slices.SortStableFunc(ordered, func(a, b string) int { return cmp.Compare(len(b), len(a)) })
	for _, v := range ordered {
		// A blank value would mask every space; secrets never records one.
		if strings.TrimSpace(v) == "" {
			continue
		}
		failure = secrets.Scrub(failure, v)
	}
	return tail(failure, failureBudget)
}

// tail returns at most the last budget bytes of s, starting on a rune boundary.
func tail(s string, budget int) string {
	if len(s) <= budget {
		return s
	}
	start := len(s) - budget
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}

// WriteBundle writes b to <b.StagedRoot>/BundleFile atomically: a 0600 temporary
// file in the same directory, synced, then renamed over the bundle. On any
// failure the temporary file is removed and the error names the bundle path.
func WriteBundle(b Bundle) error {
	if b.StagedRoot == "" {
		return fmt.Errorf("write repair bundle for %s: empty staged root", b.Package)
	}
	path := filepath.Join(b.StagedRoot, BundleFile)
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return fmt.Errorf("encode repair bundle %s for %s: %w", path, b.Package, err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(b.StagedRoot, ".bentoo-repair-*")
	if err != nil {
		return fmt.Errorf("write repair bundle %s for %s: %w", path, b.Package, err)
	}
	if err := writeAndClose(tmp, data); err != nil {
		return discardTemp(tmp.Name(), fmt.Errorf("write repair bundle %s for %s: %w", path, b.Package, err))
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return discardTemp(tmp.Name(), fmt.Errorf("write repair bundle %s for %s: %w", path, b.Package, err))
	}
	return nil
}

// writeAndClose sets f to 0600, writes data, syncs and closes it. f is closed
// on every path.
func writeAndClose(f *os.File, data []byte) error {
	err := f.Chmod(0o600)
	if err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// discardTemp removes the temporary file name and returns cause, joined with
// the removal error when the file could not be removed.
func discardTemp(name string, cause error) error {
	if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.Join(cause, fmt.Errorf("remove temporary file %s: %w", name, err))
	}
	return cause
}

// ReadBundle reads and parses the bundle at path. A read or parse failure is
// an error naming path; a schema other than SchemaVersion is ErrUnknownSchema,
// also naming path.
func ReadBundle(path string) (Bundle, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is a bundle inside bentoo's own staging root, found by FindBundle or named by the caller
	if err != nil {
		return Bundle{}, fmt.Errorf("read repair bundle %s: %w", path, err)
	}
	var b Bundle
	if err := json.Unmarshal(data, &b); err != nil {
		return Bundle{}, fmt.Errorf("parse repair bundle %s: %w", path, err)
	}
	if b.Schema != SchemaVersion {
		return Bundle{}, fmt.Errorf("read repair bundle %s: %w %d (this bentoo reads schema %d)",
			path, ErrUnknownSchema, b.Schema, SchemaVersion)
	}
	return b, nil
}
