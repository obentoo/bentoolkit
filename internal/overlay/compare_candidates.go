package overlay

import (
	"crypto/md5" // #nosec G501 -- md5-cache's own _md5_ field, a freshness check, not security
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/ebuild"
)

// compareArch is the keyword a ::gentoo version must carry, stable or testing,
// to count as something ::gentoo SHIPS. The overlay is amd64-only by policy
// (its KEYWORDS are "~amd64"), so a version that is not keyworded there is not
// one any user of the overlay could have installed from ::gentoo instead.
const compareArch = "amd64"

// comparableRemoteVersions narrows ::gentoo's version list to the versions the
// overlay's latest one can honestly be measured against. Before it existed the
// comparison took ::gentoo's newest version, full stop, and every one of these
// read as "::gentoo moved ahead" (2026-11-05, 18 of 19 "needs rebase" rows):
//
//   - a version with no amd64 keyword: unkeyworded snapshots (llvm 24 _pre,
//     gcc 17 _p), a revision ::gentoo has not keyworded yet (glibc -r5), a
//     live ebuild the "9999" filter misses (mirrorselect-9998);
//   - a version in ANOTHER main SLOT: atkmm 2.36 next to our atkmm:0 2.28 is a
//     second package, not a newer copy of ours;
//   - a version ::gentoo masks in profiles/package.mask (eigen:3/5.0).
//
// It reads both trees in place and never fails: anything it cannot read or
// resolve counts in the version's FAVOUR, so the narrowing can only remove a
// version it has positive evidence against. If nothing survives, the original
// list comes back unchanged — the old behaviour, never an invented "not in
// remote".
func comparableRemoteVersions(versions []string, localDir, pkg, localVersion, remoteDir string) []string {
	localSlot, localOK := ebuildMainSlot(localDir, pkg, localVersion)
	masks := readPackageMask(filepath.Join(filepath.Dir(filepath.Dir(remoteDir)), "profiles", "package.mask"),
		filepath.Base(filepath.Dir(remoteDir))+"/"+pkg)

	kept := make([]string, 0, len(versions))
	for _, v := range versions {
		meta, ok := readEbuildMeta(remoteDir, pkg, v)
		if !ok {
			kept = append(kept, v)
			continue
		}
		if !meta.keyworded(compareArch) {
			continue
		}
		slot, sub, slotOK := meta.slot(v)
		if localOK && slotOK && slot != localSlot {
			continue
		}
		if maskedBy(masks, v, slot, sub, slotOK) {
			continue
		}
		kept = append(kept, v)
	}
	if len(kept) == 0 {
		return versions
	}
	return kept
}

// keywordsRe finds every uncommented KEYWORDS assignment; a quoted value may
// span lines. A "#KEYWORDS=" line — ::gentoo's way of unkeywording a revision —
// does not match, because only blanks may precede the name.
var keywordsRe = regexp.MustCompile(`(?m)^[ \t]*KEYWORDS=("[^"]*"|'[^']*'|\S*)`)

// keywordedFor reports whether any KEYWORDS assignment names arch, stable or
// testing. An ebuild with no assignment at all is unkeyworded.
func keywordedFor(body, arch string) bool {
	for _, m := range keywordsRe.FindAllStringSubmatch(body, -1) {
		for _, kw := range strings.Fields(strings.Trim(m[1], `"'`)) {
			if kw == arch || kw == "~"+arch {
				return true
			}
		}
	}
	return false
}

var slotRe = regexp.MustCompile(`(?m)^[ \t]*SLOT=("[^"]*"|'[^']*'|\S*)`)

// ebuildMeta is what the narrowing needs from one ebuild: its text and, when
// the tree's md5-cache holds a fresh entry for it, the SLOT and KEYWORDS
// Portage computed. The cache is what covers a SLOT the text cannot resolve
// without bash — automake's SLOT="${MANGLED_SLOT}" (issue #307).
type ebuildMeta struct {
	body string
	// cache holds the md5-cache entry's KEY=value lines; nil when there is no
	// entry or its _md5_ does not match the ebuild, so a stale entry never
	// outranks the text.
	cache map[string]string
}

// readEbuildMeta reads pkgDir/pkg-version.ebuild and its md5-cache entry at
// <tree>/metadata/md5-cache/<category>/<pkg>-<version>, pkgDir being
// <tree>/<category>/<pkg>.
func readEbuildMeta(pkgDir, pkg, version string) (ebuildMeta, bool) {
	text, err := os.ReadFile(filepath.Join(pkgDir, pkg+"-"+version+".ebuild")) // #nosec G304 -- built from scanned directory names and the provider's own version list
	if err != nil {
		return ebuildMeta{}, false
	}
	meta := ebuildMeta{body: string(text)}
	tree, category := filepath.Dir(filepath.Dir(pkgDir)), filepath.Base(filepath.Dir(pkgDir))
	entry, err := os.ReadFile(filepath.Join(tree, "metadata", "md5-cache", category, pkg+"-"+version)) // #nosec G304 -- the tree's own metadata directory
	if err != nil {
		return meta, true
	}
	cache := map[string]string{}
	for _, line := range strings.Split(string(entry), "\n") {
		if k, v, found := strings.Cut(line, "="); found {
			cache[k] = v
		}
	}
	sum := md5.Sum(text) // #nosec G401 -- compared with md5-cache's _md5_, a freshness check
	if cache["_md5_"] == hex.EncodeToString(sum[:]) {
		meta.cache = cache
	}
	return meta, true
}

func (m ebuildMeta) keyworded(arch string) bool {
	if kw, ok := m.cache["KEYWORDS"]; ok {
		return keywordedFor("KEYWORDS=\""+kw+"\"", arch)
	}
	return keywordedFor(m.body, arch)
}

// slot prefers the cached SLOT, which Portage resolved with bash and eclasses,
// over expanding the text.
func (m ebuildMeta) slot(version string) (main, sub string, ok bool) {
	if s := m.cache["SLOT"]; s != "" {
		main, sub, _ = strings.Cut(s, "/")
		return main, sub, true
	}
	return mainSlot(m.body, version)
}

// ebuildMainSlot resolves the main SLOT of pkgDir/pkg-version.ebuild.
func ebuildMainSlot(pkgDir, pkg, version string) (string, bool) {
	meta, ok := readEbuildMeta(pkgDir, pkg, version)
	if !ok {
		return "", false
	}
	slot, _, ok := meta.slot(version)
	return slot, ok
}

// mainSlot resolves an ebuild's SLOT into main slot and sub-slot, expanding
// only what needs no bash: ${PV}, ${PVR} and $(ver_cut ...). Anything else — a
// variable an eclass sets (gcc's ${GCCMAJOR}, llvm's ${LLVM_MAJOR}), a SLOT the
// ebuild never assigns — is unresolved, and an unresolved slot narrows nothing.
func mainSlot(body, version string) (main, sub string, ok bool) {
	m := slotRe.FindStringSubmatch(body)
	if m == nil {
		return "", "", false
	}
	pv := stripRevision(version)
	value := strings.Trim(m[1], `"'`)
	value = strings.NewReplacer("${PVR}", version, "${PV}", pv).Replace(value)
	value = verCutRe.ReplaceAllStringFunc(value, func(call string) string {
		return verCut(pv, verCutRe.FindStringSubmatch(call)[1])
	})
	if value == "" || strings.ContainsAny(value, "$`") {
		return "", "", false
	}
	main, sub, _ = strings.Cut(value, "/")
	return main, sub, true
}

var verCutRe = regexp.MustCompile(`\$\(ver_cut ([0-9]+(?:-[0-9]*)?)\)`)

// verComponentRe is ver_cut's notion of a component: a run of digits or a run
// of letters; everything else separates.
var verComponentRe = regexp.MustCompile(`[0-9]+|[A-Za-z]+`)

// verCut implements ver_cut for the "N", "N-M" and "N-" ranges: the substring
// of pv from the start of component N to the end of component M.
func verCut(pv, spec string) string {
	comps := verComponentRe.FindAllStringIndex(pv, -1)
	if len(comps) == 0 {
		return ""
	}
	from, to := spec, spec
	if a, b, found := strings.Cut(spec, "-"); found {
		from, to = a, b
	}
	start, err := strconv.Atoi(from)
	if err != nil || start < 1 {
		return ""
	}
	end := len(comps)
	if to != "" {
		if end, err = strconv.Atoi(to); err != nil {
			return ""
		}
	}
	if start > len(comps) {
		return ""
	}
	if end > len(comps) {
		end = len(comps)
	}
	if end < start {
		return ""
	}
	return pv[comps[start-1][0]:comps[end-1][1]]
}

// stripRevision turns "1.2-r3" into "1.2", the ${PV} of the ebuild.
func stripRevision(version string) string {
	if i := strings.LastIndex(version, "-r"); i > 0 {
		if _, err := strconv.Atoi(version[i+2:]); err == nil {
			return version[:i]
		}
	}
	return version
}

// maskAtom is one package.mask line about the compared package. An empty op
// masks every version; slot and sub are empty when the line names none.
type maskAtom struct {
	op, version string
	glob        bool
	slot, sub   string
}

// readPackageMask returns the lines of path (a file, or a directory of them)
// that name atom. A line it cannot parse is dropped: an unparsed mask masks
// nothing, which keeps the version a candidate.
func readPackageMask(path, atom string) []maskAtom {
	var texts []string
	if entries, err := os.ReadDir(path); err == nil {
		for _, e := range entries {
			if b, err := os.ReadFile(filepath.Join(path, e.Name())); err == nil { // #nosec G304 -- the ::gentoo tree's own profiles directory
				texts = append(texts, string(b))
			}
		}
	} else if b, err := os.ReadFile(path); err == nil { // #nosec G304 -- the ::gentoo tree's own profiles directory
		texts = append(texts, string(b))
	}

	var out []maskAtom
	for _, text := range texts {
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, atom) {
				continue
			}
			if a, ok := parseMaskAtom(line, atom); ok {
				out = append(out, a)
			}
		}
	}
	return out
}

func parseMaskAtom(line, atom string) (maskAtom, bool) {
	var a maskAtom
	line, _, _ = strings.Cut(line, "::")
	for _, op := range []string{">=", "<=", "=", "~", ">", "<"} {
		if strings.HasPrefix(line, op) {
			a.op, line = op, line[len(op):]
			break
		}
	}
	line, slot, hasSlot := strings.Cut(line, ":")
	if hasSlot {
		a.slot, a.sub, _ = strings.Cut(slot, "/")
	}
	if a.op == "" {
		return a, line == atom
	}
	if !strings.HasPrefix(line, atom+"-") {
		return a, false
	}
	a.version = line[len(atom)+1:]
	if a.op == "=" && strings.HasSuffix(a.version, "*") {
		a.glob, a.version = true, strings.TrimSuffix(a.version, "*")
	}
	if a.version == "" || a.version[0] < '0' || a.version[0] > '9' {
		return a, false
	}
	return a, true
}

// maskedBy reports whether any of masks covers version v in slot/sub. A line
// that names a slot the version's could not be resolved against covers nothing.
func maskedBy(masks []maskAtom, v, slot, sub string, slotOK bool) bool {
	for _, m := range masks {
		if m.slot != "" && (!slotOK || m.slot != slot || (m.sub != "" && m.sub != sub)) {
			continue
		}
		if maskVersionMatches(m, v) {
			return true
		}
	}
	return false
}

func maskVersionMatches(m maskAtom, v string) bool {
	if m.op == "" {
		return true
	}
	if m.glob {
		return strings.HasPrefix(v, m.version)
	}
	if m.op == "~" {
		base := stripRevision(v)
		return ebuild.CompareVersions(base, m.version) == 0
	}
	cmp := ebuild.CompareVersions(v, m.version)
	switch m.op {
	case "=":
		return cmp == 0
	case ">=":
		return cmp >= 0
	case "<=":
		return cmp <= 0
	case ">":
		return cmp > 0
	case "<":
		return cmp < 0
	}
	return false
}
