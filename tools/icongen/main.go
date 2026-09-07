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

// focus is where the icons look: the middle of the two animals, in the
// logo's own pixels. Each icon is a square around it.
var focus = image.Point{X: 640, Y: 460}

// wordmarkTop is the row the wordmark starts on. The icons are the animals
// alone — a wordmark is mush at 30 px — so everything from here down is
// painted over with the page colour before the crop.
const wordmarkTop = 800

// The corner radius of an iOS-style tile, as a fraction of its side.
const tileRadius = 0.2237

// logoSize is the side of logo.png, the whole drawing as a rounded tile: the
// app throws it over a blurred screen when the header's mark is double-tapped,
// and nothing larger than this is ever shown. The drawing is on black paper
// with glows that fade into it, so the paper stays — cutting it out would
// leave a dark halo — and the tile's corners are rounded instead.
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
	{"icon-192.png", 192, 1100, true, 1},
	{"icon-512.png", 512, 1100, true, 1},
	{"apple-touch-icon.png", 180, 1100, false, 1},
	{"icon-512-maskable.png", 512, 1100, false, 0.78},
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

	full := Scale(logo, logoSize)
	RoundCorners(full, tileRadius/2)
	if err := write(filepath.Join(out, "logo.png"), full); err != nil {
		return err
	}
	fmt.Println("wrote", filepath.Join(out, "logo.png"))

	// The page colour is whatever the drawing's corner is: the padding around
	// a crop and the erased wordmark band must be seamless with it.
	tile := color.RGBAModel.Convert(logo.At(logo.Bounds().Min.X, logo.Bounds().Min.Y)).(color.RGBA)
	animals := Erase(logo, image.Rect(logo.Bounds().Min.X, wordmarkTop, logo.Bounds().Max.X, logo.Bounds().Max.Y), tile)
	for _, t := range targets {
		icon := Scale(CropSquare(animals, focus, t.box, tile), int(float64(t.size)*t.inset+0.5))
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
