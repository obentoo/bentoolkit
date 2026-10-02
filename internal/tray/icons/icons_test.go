package icons_test

import (
	"bytes"
	"encoding/xml"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/obentoo/bentoolkit/internal/desktop/sni"
	"github.com/obentoo/bentoolkit/internal/tray/icons"
)

var variants = map[string]icons.Variant{
	"plain":    icons.Plain,
	"unread":   icons.Unread,
	"critical": icons.Critical,
}

// TestPixmaps_FiveSquareARGBSizes is R8.6: each variant at 16, 22, 24, 32 and
// 48 px, four bytes per pixel.
func TestPixmaps_FiveSquareARGBSizes(t *testing.T) {
	for name, v := range variants {
		px := icons.Pixmaps(v)
		var sizes []int32
		for _, p := range px {
			if p.Width != p.Height {
				t.Errorf("%s: %dx%d pixmap is not square", name, p.Width, p.Height)
			}
			if len(p.Data) != int(p.Width*p.Height*4) {
				t.Errorf("%s %dpx: %d bytes, want %d (ARGB32)", name, p.Width, len(p.Data), p.Width*p.Height*4)
			}
			sizes = append(sizes, p.Width)
		}
		slices.Sort(sizes)
		if !slices.Equal(sizes, []int32{16, 22, 24, 32, 48}) {
			t.Errorf("%s sizes = %v, want [16 22 24 32 48]", name, sizes)
		}
	}
}

func pixmapAt(t *testing.T, v icons.Variant, size int32) sni.Pixmap {
	t.Helper()
	for _, p := range icons.Pixmaps(v) {
		if p.Width == size {
			return p
		}
	}
	t.Fatalf("no %dpx pixmap", size)
	return sni.Pixmap{}
}

// countOpaque counts fully opaque pixels whose colour, read as A,R,G,B in
// network byte order, satisfies match. Reading the bytes in that order is what
// makes this a byte-order test: an R,G,B,A buffer reads as the wrong colour.
func countOpaque(p sni.Pixmap, match func(r, g, b byte) bool) int {
	n := 0
	for i := 0; i+3 < len(p.Data); i += 4 {
		a, r, g, b := p.Data[i], p.Data[i+1], p.Data[i+2], p.Data[i+3]
		if a == 0xff && match(r, g, b) {
			n++
		}
	}
	return n
}

func orange(r, g, b byte) bool { return r >= 0xe0 && g >= 0x80 && g <= 0xb8 && b <= 0x40 }
func red(r, g, b byte) bool    { return r >= 0xc0 && g <= 0x50 && b <= 0x50 }

// TestPixmaps_DotVariantsCarryTheirDot is R8.4/R8.7 at the pixel level: the
// unread icon adds an orange dot and the critical icon a red one, compared
// with the plain art at the same size. Checked at 32 and 48 px, where the dot
// has a solid interior.
func TestPixmaps_DotVariantsCarryTheirDot(t *testing.T) {
	for _, size := range []int32{32, 48} {
		plain, unread, critical := pixmapAt(t, icons.Plain, size), pixmapAt(t, icons.Unread, size), pixmapAt(t, icons.Critical, size)
		minDot := int(size) / 2 // a dot of ~30% of the width has far more pixels than this
		if got := countOpaque(unread, orange) - countOpaque(plain, orange); got < minDot {
			t.Errorf("%dpx: the unread icon has %d more opaque orange pixels than the plain one, want >= %d (missing dot, or bytes not A,R,G,B)", size, got, minDot)
		}
		if got := countOpaque(critical, red) - countOpaque(plain, red); got < minDot {
			t.Errorf("%dpx: the critical icon has %d more opaque red pixels than the plain one, want >= %d (missing dot, or bytes not A,R,G,B)", size, got, minDot)
		}
		// Hostile: the dots must not be swapped or shared.
		if got := countOpaque(critical, orange) - countOpaque(plain, orange); got >= minDot {
			t.Errorf("%dpx: the critical icon carries an orange dot", size)
		}
		if got := countOpaque(unread, red) - countOpaque(plain, red); got >= minDot {
			t.Errorf("%dpx: the unread icon carries a red dot", size)
		}
	}
}

// TestPixmaps_VariantsAreDistinct: three variants that decode to the same
// image would make every icon state look alike.
func TestPixmaps_VariantsAreDistinct(t *testing.T) {
	p, u, c := pixmapAt(t, icons.Plain, 48), pixmapAt(t, icons.Unread, 48), pixmapAt(t, icons.Critical, 48)
	if bytes.Equal(p.Data, u.Data) || bytes.Equal(p.Data, c.Data) || bytes.Equal(u.Data, c.Data) {
		t.Error("two icon variants are byte-identical at 48px")
	}
}

// TestSVGSources_ShipInThreeVariants is R12.4: the scalable sources exist and
// are SVG documents.
func TestSVGSources_ShipInThreeVariants(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "misc", "tray", "icons")
	for _, name := range []string{"bentoo-tray.svg", "bentoo-tray-unread.svg", "bentoo-tray-critical.svg"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		var root struct{ XMLName xml.Name }
		if err := xml.Unmarshal(raw, &root); err != nil || root.XMLName.Local != "svg" {
			t.Errorf("%s is not an SVG document (root %q, err %v)", name, root.XMLName.Local, err)
		}
	}
}
