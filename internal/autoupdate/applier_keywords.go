package autoupdate

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/obentoo/bentoolkit/internal/autoupdate/ebuilds"
	"github.com/obentoo/bentoolkit/internal/common/fileutil"
	"github.com/obentoo/bentoolkit/internal/gentoo/keywords"
)

// revisionSuffixRe is a Gentoo -rN revision at the end of a version. A bump
// is "past ::gentoo" by upstream version, so revisions on either side never
// make two versions differ or agree.
var revisionSuffixRe = regexp.MustCompile(`-r[0-9]+$`)

// pastGentoo reports whether a bump from oldPV to newPV leaves ::gentoo
// behind: ::gentoo carries category/pn at oldPV and not at newPV, both read
// without their -rN. Only then was the overlay copy a mirror that the bump
// turns into a version ::gentoo never keyworded.
//
// known is false when there is no ::gentoo tree to ask — an empty path, or one
// that does not exist. The caller must not read that as "::gentoo lacks the
// package": the command always passes a default path, so a missing tree is
// the production case, and guessing there would rewrite KEYWORDS on every
// bump of an unsynced host.
func pastGentoo(gentooPath, category, pn, oldPV, newPV string) (past, known bool) {
	if gentooPath == "" {
		return false, false
	}
	if info, err := os.Stat(gentooPath); err != nil || !info.IsDir() {
		return false, false
	}
	newBase := revisionSuffixRe.ReplaceAllString(newPV, "")
	if strings.HasSuffix(newBase, "9999") {
		return false, true
	}
	entries, err := os.ReadDir(filepath.Join(gentooPath, category, pn))
	if errors.Is(err, fs.ErrNotExist) {
		return false, true
	}
	if err != nil {
		return false, false
	}
	carried := make(map[string]bool, len(entries))
	for _, e := range entries {
		version, ok := strings.CutPrefix(e.Name(), pn+"-")
		if !ok || e.IsDir() {
			continue
		}
		version, ok = strings.CutSuffix(version, ".ebuild")
		if !ok || version == "" || version[0] < '0' || version[0] > '9' {
			continue
		}
		carried[revisionSuffixRe.ReplaceAllString(version, "")] = true
	}
	oldBase := revisionSuffixRe.ReplaceAllString(oldPV, "")
	return carried[oldBase] && !carried[newBase], true
}

// overlayBaselineKeywords is what a bump past ::gentoo is keyworded with: the
// overlay's platform baseline (bentoo CLAUDE.md), since no ::gentoo arch team
// ever reviewed the new version.
const overlayBaselineKeywords = "~amd64"

// keywordsDivergenceTag is written beside the copy's other divergence tags
// when its KEYWORDS is rewritten, so the next reader of the ebuild sees why it
// no longer matches ::gentoo's keywords.
const keywordsDivergenceTag = "# BENTOO-DIVERGENCE: KEYWORDS - ~amd64 only: a bump past ::gentoo is no longer a mirror, so it follows the overlay baseline (amd64, x86-64-v3)."

// convertMirrorKeywords rewrites a single-line KEYWORDS to the overlay
// baseline and adds the KEYWORDS divergence tag, unless one is already there.
// changed is false, and body is returned as it was, when there is nothing to
// rewrite: no KEYWORDS, one already at the baseline, or one that is not a
// single-line literal — tagging that last case "~amd64 only" would be false.
func convertMirrorKeywords(body []byte) ([]byte, bool) {
	rewritten, changed := keywords.Rewrite(body, overlayBaselineKeywords)
	if !changed {
		return body, false
	}
	return insertKeywordsTag(rewritten), true
}

// insertKeywordsTag puts keywordsDivergenceTag directly above the first
// existing divergence tag, else above PYTHON_COMPAT= or the first inherit,
// else above the KEYWORDS assignment. A body that already carries a KEYWORDS
// tag, in any wording, is returned unchanged.
func insertKeywordsTag(body []byte) []byte {
	lines := strings.SplitAfter(string(body), "\n")
	anchor := -1
	for _, want := range []func(string) bool{
		func(l string) bool { return strings.HasPrefix(l, "# BENTOO-DIVERGENCE:") },
		func(l string) bool { return strings.HasPrefix(l, "PYTHON_COMPAT=") || strings.HasPrefix(l, "inherit ") },
		func(l string) bool { return strings.HasPrefix(strings.TrimLeft(l, " \t"), "KEYWORDS=") },
	} {
		for i, l := range lines {
			if strings.HasPrefix(l, "# BENTOO-DIVERGENCE: KEYWORDS ") {
				return body
			}
			if anchor < 0 && want(l) {
				anchor = i
			}
		}
		if anchor >= 0 {
			break
		}
	}
	if anchor < 0 {
		return body
	}
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:anchor]...)
	out = append(out, keywordsDivergenceTag+"\n")
	out = append(out, lines[anchor:]...)
	return []byte(strings.Join(out, ""))
}

// convertMirrorBump applies convertMirrorKeywords to the candidate at
// ebuildPath when the bump in update leaves ::gentoo behind. An unknown
// ::gentoo (no tree) converts nothing; the apply summary says so once.
func (a *Applier) convertMirrorBump(ebuildPath, pkg string, update *PendingUpdate) error {
	category, pn, ok := ebuilds.SplitPkgAtom(pkg)
	if !ok {
		return nil
	}
	if past, known := pastGentoo(a.gentooPath, category, pn, update.CurrentVersion, update.NewVersion); !past || !known {
		return nil
	}
	body, err := os.ReadFile(ebuildPath) //nolint:gosec // G304: ebuildPath is the candidate applySubstitutions was handed, inside the overlay or its staged tree
	if err != nil {
		return fmt.Errorf("reading %s to convert its mirror KEYWORDS: %w", ebuildPath, err)
	}
	converted, changed := convertMirrorKeywords(body)
	if !changed {
		return nil
	}
	if err := fileutil.WriteFileAtomic(ebuildPath, converted, 0o644); err != nil {
		return fmt.Errorf("writing the converted KEYWORDS of %s: %w", ebuildPath, err)
	}
	return nil
}

// MirrorConversion describes a published bump that left ::gentoo behind.
type MirrorConversion struct {
	// KeywordsNotRewritten is true when the ebuild's KEYWORDS is not a
	// single-line literal, so it was left as it was and still needs the
	// overlay baseline by hand.
	KeywordsNotRewritten bool
}

// noteMirrorConversion records on result whether the published bump left
// ::gentoo behind. It reads the PUBLISHED ebuild rather than trusting the
// staging run: `--apply` usually promotes a tree an earlier `--check` staged,
// so the conversion happened in another process and only the file remains.
func (a *Applier) noteMirrorConversion(pkg string, result *ApplyResult) {
	category, pn, ok := ebuilds.SplitPkgAtom(pkg)
	if !ok {
		return
	}
	past, known := pastGentoo(a.gentooPath, category, pn, result.OldVersion, result.NewVersion)
	if !known {
		result.GentooTreeUnknown = true
		return
	}
	if !past {
		return
	}
	conv := &MirrorConversion{}
	path := a.EbuildPath(pkg, result.NewVersion)
	body, err := os.ReadFile(path) //nolint:gosec // G304: path is EbuildPath of the package just published in this applier's overlay
	if err != nil {
		// The bump is published either way; only the "KEYWORDS not rewritten"
		// note is lost, so this degrades the summary rather than the apply.
		a.logger().Debug("mirror conversion: published ebuild unreadable", "package", pkg, "path", path, "err", err)
	} else if _, singleLine, present := keywords.Read(body); present && !singleLine {
		conv.KeywordsNotRewritten = true
	}
	result.MirrorConversion = conv
}
