package vendor

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// maxEntryLine bounds one md5-cache line. A go-module ebuild that still lists
// EGO_SUM carries thousands of SRC_URI tokens on a single line, far beyond the
// 64 KiB bufio.Scanner default.
const maxEntryLine = 16 << 20

// vendorName is the only distfile name a vendor tarball may carry.
var vendorName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*-vendor\.tar\.xz$`)

// errNoSrcURI is wrapped by Classify when the entry has no SRC_URI= line.
var errNoSrcURI = errors.New("no SRC_URI= line")

// Candidate is what Classify found.
type Candidate struct {
	SourceFile string // distfile name of the source archive
	SourceURI  string
	VendorFile string // e.g. govulncheck-1.9.0-vendor.tar.xz
	VendorURI  string // https://distfiles.obentoo.org/govulncheck-1.9.0-vendor.tar.xz
}

// Fetchable is one SRC_URI member: where it is fetched from and the distfile
// name it is stored under.
type Fetchable struct{ URI, Name string }

// Classify reads one md5-cache entry and reports whether it describes a Go
// vendor candidate: INHERIT names go-module as a whole word, and SRC_URI holds
// exactly two fetchables, one of them the only "<name>-vendor.tar.xz" and
// fetched from a host listed in hosts (compared case-insensitively, by the
// host net/url parses, never by substring).
//
// ok=false with a nil error means "not a Go vendor candidate". A read error, a
// missing SRC_URI= line or an unparseable SRC_URI value is returned as an
// error, so an unreadable entry is never mistaken for a refusal.
func Classify(entry io.Reader, hosts []string) (c Candidate, ok bool, err error) {
	inherit, srcURI, err := readEntry(entry)
	if err != nil {
		return Candidate{}, false, err
	}
	if !slices.Contains(strings.Fields(inherit), "go-module") {
		return Candidate{}, false, nil
	}
	fetchables, err := ParseSrcURI(srcURI)
	if err != nil {
		return Candidate{}, false, fmt.Errorf("classify md5-cache entry: SRC_URI: %w", err)
	}
	if len(fetchables) != 2 {
		return Candidate{}, false, nil
	}

	vendorIdx := -1
	for i, f := range fetchables {
		if !vendorName.MatchString(f.Name) {
			continue
		}
		if vendorIdx >= 0 {
			return Candidate{}, false, nil
		}
		vendorIdx = i
	}
	if vendorIdx < 0 {
		return Candidate{}, false, nil
	}
	vend, src := fetchables[vendorIdx], fetchables[1-vendorIdx]
	if !hostAllowed(vend.URI, hosts) {
		return Candidate{}, false, nil
	}
	return Candidate{
		SourceFile: src.Name,
		SourceURI:  src.URI,
		VendorFile: vend.Name,
		VendorURI:  vend.URI,
	}, true, nil
}

// readEntry returns the INHERIT= and SRC_URI= values of an md5-cache entry.
// A missing INHERIT= reads as empty; a missing SRC_URI= is an error.
func readEntry(entry io.Reader) (inherit, srcURI string, err error) {
	sc := bufio.NewScanner(entry)
	sc.Buffer(make([]byte, 0, 64*1024), maxEntryLine)
	haveSrcURI := false
	for sc.Scan() {
		key, value, found := strings.Cut(sc.Text(), "=")
		if !found {
			continue
		}
		switch key {
		case "INHERIT":
			inherit = value
		case "SRC_URI":
			srcURI, haveSrcURI = value, true
		}
	}
	if err := sc.Err(); err != nil {
		return "", "", fmt.Errorf("read md5-cache entry: %w", err)
	}
	if !haveSrcURI {
		return "", "", fmt.Errorf("read md5-cache entry: %w", errNoSrcURI)
	}
	return inherit, srcURI, nil
}

// hostAllowed reports whether uri's host, lower-cased, is one of hosts. The
// host comes from net/url, so userinfo, port and path never take part.
func hostAllowed(uri string, hosts []string) bool {
	u, err := url.Parse(uri)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return false
	}
	return slices.ContainsFunc(hosts, func(h string) bool { return strings.EqualFold(h, host) })
}

// ParseSrcURI flattens SRC_URI tokens: `uri -> name` renames, USE-conditional
// groups `flag? ( ... )` and `|| ( ... )` contribute every member.
//
// A URI that is not renamed is named by its last path segment. A dangling or
// leading arrow, an empty name, and unbalanced parentheses are errors.
func ParseSrcURI(v string) ([]Fetchable, error) {
	tokens := strings.Fields(v)
	var out []Fetchable
	depth := 0
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		switch {
		case tok == "(":
			depth++
			continue
		case tok == ")":
			depth--
			if depth < 0 {
				return nil, fmt.Errorf("unbalanced %q at token %d", tok, i)
			}
			continue
		case tok == "||", strings.HasSuffix(tok, "?"):
			continue
		case tok == "->":
			return nil, fmt.Errorf("%q with no URI before it at token %d", tok, i)
		}

		f := Fetchable{URI: tok, Name: tok[strings.LastIndexByte(tok, '/')+1:]}
		if i+1 < len(tokens) && tokens[i+1] == "->" {
			if i+2 >= len(tokens) {
				return nil, fmt.Errorf("%q after %q has no name", "->", tok)
			}
			f.Name = tokens[i+2]
			i += 2
		}
		if f.Name == "" || isStructural(f.Name) {
			return nil, fmt.Errorf("no distfile name for %q", tok)
		}
		out = append(out, f)
	}
	if depth != 0 {
		return nil, fmt.Errorf("unbalanced parentheses: %d left open", depth)
	}
	return out, nil
}

// isStructural reports whether tok is SRC_URI grouping syntax rather than a
// distfile name.
func isStructural(tok string) bool {
	return tok == "(" || tok == ")" || tok == "||" || tok == "->" || strings.HasSuffix(tok, "?")
}
