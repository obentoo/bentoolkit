// Package icons holds the tray icon as SNI pixmaps. The pixmaps
// are PNGs pre-rendered from misc/tray/icons/*.svg by `make tray-icons` and
// embedded, so the Go build needs no SVG library.
package icons

import (
	"bytes"
	"embed"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"slices"
	"sync"

	"github.com/obentoo/bentoolkit/internal/desktop/sni"
)

// Variant selects one of the three icon states.
type Variant int

const (
	// Plain is the icon with no badge.
	Plain Variant = iota
	// Unread adds an orange dot: unread notices, none critical.
	Unread
	// Critical adds a red dot: an unread notice is critical.
	Critical
)

// files names each variant's PNGs: <name>-<size>.png, as `make tray-icons`
// writes them.
var files = map[Variant]string{
	Plain:    "bentoo-tray",
	Unread:   "bentoo-tray-unread",
	Critical: "bentoo-tray-critical",
}

// sizes are the pixmap edges, in pixels, every variant is rendered at. Keep
// equal to TRAY_ICON_SIZES in the Makefile.
var sizes = []int32{16, 22, 24, 32, 48}

//go:embed png/*.png
var pngs embed.FS

// decoded holds every variant's pixmaps, decoded on first use. The PNGs are
// embedded at build time, so a decode failure is a programming error (a
// missing or corrupt asset), not a runtime condition: it panics.
var decoded = sync.OnceValue(func() map[Variant][]sni.Pixmap {
	all := make(map[Variant][]sni.Pixmap, len(files))
	for v, name := range files {
		px := make([]sni.Pixmap, 0, len(sizes))
		for _, size := range sizes {
			p, err := load(fmt.Sprintf("png/%s-%d.png", name, size), size)
			if err != nil {
				panic(fmt.Sprintf("icons: embedded tray icon: %v (re-run `make tray-icons`)", err))
			}
			px = append(px, p)
		}
		all[v] = px
	}
	return all
})

// Pixmaps returns the variant's icon at 16, 22, 24, 32 and 48 px as ARGB32
// pixmaps, or nil for an unknown variant. The pixel data is shared between
// calls: callers must not modify it.
func Pixmaps(variant Variant) []sni.Pixmap {
	return slices.Clone(decoded()[variant])
}

// load decodes one embedded PNG, checks it is size x size, and converts it to
// an SNI pixmap.
func load(path string, size int32) (sni.Pixmap, error) {
	raw, err := pngs.ReadFile(path)
	if err != nil {
		return sni.Pixmap{}, fmt.Errorf("read %s: %w", path, err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return sni.Pixmap{}, fmt.Errorf("decode %s: %w", path, err)
	}
	b := img.Bounds()
	if b.Dx() != int(size) || b.Dy() != int(size) {
		return sni.Pixmap{}, fmt.Errorf("%s is %dx%d, want %dx%d", path, b.Dx(), b.Dy(), size, size)
	}
	return sni.Pixmap{Width: size, Height: size, Data: argb(toNRGBA(img))}, nil
}

// toNRGBA returns img as non-premultiplied RGBA, converting when the decoder
// produced another model (paletted, grey, 16-bit).
func toNRGBA(img image.Image) *image.NRGBA {
	if n, ok := img.(*image.NRGBA); ok {
		return n
	}
	b := img.Bounds()
	n := image.NewNRGBA(b)
	draw.Draw(n, b, img, b.Min, draw.Src)
	return n
}

// argb reorders each R,G,B,A pixel to A,R,G,B — the network byte order SNI
// expects — without premultiplying.
func argb(img *image.NRGBA) []byte {
	b := img.Bounds()
	out := make([]byte, 0, b.Dx()*b.Dy()*4)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			i := img.PixOffset(x, y)
			r, g, bl, a := img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3]
			out = append(out, a, r, g, bl)
		}
	}
	return out
}
