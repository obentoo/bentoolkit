package distfiles

import (
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

// DropUnverified removes from dir the top-level regular files that manifest
// cannot vouch for and returns their names, plus one FileError per file it
// could not check or remove.
//
// A file the Manifest declares is kept only when its size matches and, if the
// record carries one, its SHA512 too: in a Gentoo repository a distfile name
// stands for one set of bytes, so a mismatch is a truncated or corrupt copy.
// A file the Manifest does not declare is kept when keepUnlisted is true;
// false is for a directory a killed download may have written into, where an
// undeclared file under its final name can be a partial nothing can verify.
// Removed files are fetched again by the next `pkgdev manifest`.
func DropUnverified(dir string, manifest []byte, keepUnlisted bool) ([]string, []FileError) {
	type want struct{ size, sha512 string }
	records := map[string]want{}
	eachManifestDistRecord(manifest, func(_ string, fields []string) {
		if len(fields) < 3 || !isManifestFilename(fields[1]) {
			return
		}
		if _, dup := records[fields[1]]; dup {
			return
		}
		w := want{size: fields[2]}
		for i := 3; i+1 < len(fields); i += 2 {
			if fields[i] == "SHA512" {
				w.sha512 = fields[i+1]
			}
		}
		records[fields[1]] = w
	})

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, []FileError{{Source: dir, Err: fmt.Errorf("reading distdir %s: %w", dir, err)}}
	}
	var dropped []string
	var failures []FileError
	for _, entry := range entries {
		name := entry.Name()
		if !entry.Type().IsRegular() {
			continue
		}
		path := filepath.Join(dir, name)
		w, listed := records[name]
		keep := !listed && keepUnlisted
		if listed {
			ok, err := fileMatches(path, w.size, w.sha512)
			if err != nil {
				failures = append(failures, FileError{Name: name, Source: dir, Err: err})
				continue
			}
			keep = ok
		}
		if keep {
			continue
		}
		if err := os.Remove(path); err != nil {
			failures = append(failures, FileError{Name: name, Source: dir, Err: fmt.Errorf("removing unverified %s: %w", path, err)})
			continue
		}
		dropped = append(dropped, name)
	}
	return dropped, failures
}

// fileMatches reports whether the file at path has the given size and, when
// sha512Hex is not empty, the given SHA512.
func fileMatches(path, size, sha512Hex string) (bool, error) {
	wantSize, err := strconv.ParseInt(size, 10, 64)
	if err != nil {
		return false, nil // an unparsable size vouches for nothing
	}
	f, err := os.Open(path) //nolint:gosec // G304: path is a top-level entry of a distdir this package listed
	if err != nil {
		return false, fmt.Errorf("opening %s to verify it: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	if info.Size() != wantSize {
		return false, nil
	}
	if sha512Hex == "" {
		return true, nil
	}
	h := sha512.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, fmt.Errorf("hashing %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)) == sha512Hex, nil
}
