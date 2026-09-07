// Command icongen cuts the app icons out of art/bulls-and-bears-logo.png.
//
// The logo is the one drawing of Bulls and Bears: the bull and the bear in
// their circle, with the wordmark underneath. Icons are a square crop of the
// circle alone, taken from the same source so the two always match. Run it
// after the logo changes:
//
//	make icons
//
// It lives in its own module so the server does not carry an image-scaling
// dependency it never uses.
package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
)

// tile is the colour behind the circle on an icon. The logo is drawn on white
// paper; the paper is painted over with this before cropping, so the icons'
// corners belong to the app rather than to the page it was drawn on. It is the
// PWA's dark background, so the tile reads the same as the app opening.
var tile = color.RGBA{R: 0x0b, G: 0x0b, B: 0x0f, A: 0xff}

// focus is where the icons look: the centre of the circle, in the logo's own
// pixels. Each icon is a square around it.
var focus = image.Point{X: 628, Y: 472}

// The corner radius of an iOS-style tile, as a fraction of its side.
const tileRadius = 0.2237

// logoSize is the side of logo.png, the whole drawing on a transparent
// background: the app throws it over a blurred screen when the header's mark
// is double-tapped, and nothing larger than this is ever shown.
const logoSize = 800

// targets are the files written. Each shape suits a different consumer:
// browsers take the rounded tile as-is, iOS masks a full square itself, and
// Android's maskable icons are cropped wider so a circular launcher mask
// still keeps the whole circle inside its safe zone.
var targets = []struct {
	name    string
	size    int
	box     int // side of the square cut from the logo, in its pixels
	rounded bool
	// inset is how much of the tile the crop fills; the rest is tile colour
	// around it. The wordmark sits just under the circle, so a wider crop
	// would take the top of it — padding is how the maskable icon gets its
	// margin instead.
	inset float64
}{
	{"icon-192.png", 192, 800, true, 1},
	{"icon-512.png", 512, 800, true, 1},
	{"apple-touch-icon.png", 180, 800, false, 1},
	{"icon-512-maskable.png", 512, 800, false, 0.78},
}

func main() {
	in := flag.String("in", "bulls-and-bears-logo.png", "the logo to cut icons from")
	out := flag.String("out", ".", "directory to write the PNGs into")
	flag.Parse()

	if err := run(*in, *out); err != nil {
		fmt.Fprintln(os.Stderr, "icongen:", err)
		os.Exit(1)
	}
}

func run(in, out string) error {
	f, err := os.Open(in)
	if err != nil {
		return err
	}
	logo, err := png.Decode(f)
	f.Close()
	if err != nil {
		return fmt.Errorf("%s: %w", in, err)
	}

	if err := write(filepath.Join(out, "logo.png"), Scale(Matte(logo, color.RGBA{}), logoSize)); err != nil {
		return err
	}
	fmt.Println("wrote", filepath.Join(out, "logo.png"))

	matted := Matte(logo, tile)
	for _, t := range targets {
		icon := Scale(CropSquare(matted, focus, t.box, tile), int(float64(t.size)*t.inset+0.5))
		if t.inset < 1 {
			icon = Pad(icon, t.size, tile)
		}
		if t.rounded {
			RoundCorners(icon, tileRadius)
		}
		path := filepath.Join(out, t.name)
		if err := write(path, icon); err != nil {
			return err
		}
		fmt.Println("wrote", path)
	}
	return nil
}

func write(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
