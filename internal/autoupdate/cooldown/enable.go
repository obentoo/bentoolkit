package cooldown

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/obentoo/bentoolkit/internal/common/fileutil"
)

// DefaultDays is the release delay `--cooldown` writes when it is given no
// value.
const DefaultDays = 7

// EnableOutcome says what Enable did to the overlay.
type EnableOutcome int

const (
	// EnableUnchanged: the block already carried the requested days.
	EnableUnchanged EnableOutcome = iota
	// EnableCreated: the overlay had no block; an empty one was appended.
	EnableCreated
	// EnableRetuned: the block existed with other days; days= was rewritten.
	EnableRetuned
)

// Enable switches the cooldown on for overlayRoot with a release delay of
// days, which is what makes it apply to every host that syncs the overlay:
// the block lives in the overlay's own profiles/package.mask. An overlay
// without a block gets an empty one appended (the file and profiles/ are
// created when missing), and every byte already in the file is kept. A block
// with other days has its BEGIN line rewritten; its entries are kept. It
// returns the previous days when the block existed, 0 otherwise.
//
// Callers own the lock, as for Save.
func Enable(overlayRoot string, days int) (EnableOutcome, int, error) {
	if days < 1 || days > MaxDays {
		return EnableUnchanged, 0, fmt.Errorf("cooldown days=%d outside 1..%d", days, MaxDays)
	}
	b, err := Load(overlayRoot)
	if err != nil {
		return EnableUnchanged, 0, err
	}
	if b != nil {
		if b.Days == days {
			return EnableUnchanged, b.Days, nil
		}
		prev := b.Days
		b.Days = days
		if err := b.Save(overlayRoot); err != nil {
			return EnableUnchanged, prev, err
		}
		return EnableRetuned, prev, nil
	}
	if err := appendEmptyBlock(overlayRoot, days); err != nil {
		return EnableUnchanged, 0, err
	}
	// A Load+Save round trip derives .autoupdate/cooldown.unmask, so a host
	// that linked it never reads a missing file.
	b, err = Load(overlayRoot)
	if err != nil {
		return EnableUnchanged, 0, err
	}
	if b == nil {
		return EnableUnchanged, 0, fmt.Errorf("cooldown block written to %s but not read back", filepath.Join(overlayRoot, MaskRel))
	}
	if err := b.Save(overlayRoot); err != nil {
		return EnableUnchanged, 0, err
	}
	return EnableCreated, 0, nil
}

// appendEmptyBlock writes an empty block of days at the end of
// profiles/package.mask, separated from existing content by a blank line.
func appendEmptyBlock(overlayRoot string, days int) error {
	path := filepath.Join(overlayRoot, MaskRel)
	data, err := os.ReadFile(path) // #nosec G304 -- the overlay's own profiles/package.mask
	perm := fs.FileMode(0o644)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { // #nosec G301 -- the overlay's profiles directory, world-readable like the tree
			return fmt.Errorf("creating the directory of %s: %w", path, err)
		}
	case err != nil:
		return fmt.Errorf("reading %s: %w", path, err)
	default:
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("reading the mode of %s: %w", path, err)
		}
		perm = info.Mode().Perm()
	}
	out := append([]byte(nil), data...)
	if len(out) > 0 {
		if out[len(out)-1] != '\n' {
			out = append(out, '\n')
		}
		out = append(out, '\n')
	}
	out = append(out, (&Block{Days: days}).render()...)
	if err := fileutil.WriteFileAtomic(path, out, perm); err != nil {
		return fmt.Errorf("enabling the cooldown in %s: %w", path, err)
	}
	return nil
}
