package qr

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
)

// The quiet zone is the light margin a scanner needs around a code.
const (
	// DefaultQuiet is the quiet zone, in modules, the standard asks for.
	DefaultQuiet = 4
	// MinQuiet is the smallest quiet zone the renderers draw; scanners read
	// a code with two modules of margin, most of them not with less.
	MinQuiet = 2
	// MaxScale is the largest scale PNG draws.
	MaxScale = 64
)

// quietOf turns the argument of a renderer into a quiet zone: zero and
// below mean DefaultQuiet, and anything below MinQuiet is MinQuiet.
func quietOf(quiet int) int {
	if quiet <= 0 {
		return DefaultQuiet
	}
	return max(quiet, MinQuiet)
}

// HalfBlocks renders the code as rows of text, two rows of modules to a row
// of text, with a quiet zone of quiet modules on every side (see DefaultQuiet
// and MinQuiet for the values: zero or less means 4, one means 2). Each
// module is one column wide; each text row stands for two module rows with
// the characters " " (both light), "▀" (upper dark), "▄" (lower dark) and "█"
// (both dark). When the module rows are odd in number the last text row has
// a light lower half.
//
// A dark module is the foreground of the character and a light module is
// its background, so the console must draw these rows in a black foreground
// on a white background, whatever its theme: a light foreground on a dark
// background is the inverse code, which many scanners do not read, and the
// quiet zone must be white. A terminal cell is about twice as high as it is
// wide, so the two half rows make the modules square.
func (c *Code) HalfBlocks(quiet int) []string {
	q := quietOf(quiet)
	n := c.Size() + 2*q
	dark := func(x, y int) bool { return c.Module(x-q, y-q) }
	rows := make([]string, 0, (n+1)/2)
	var b strings.Builder
	for y := 0; y < n; y += 2 {
		b.Reset()
		for x := 0; x < n; x++ {
			top, bottom := dark(x, y), y+1 < n && dark(x, y+1)
			switch {
			case top && bottom:
				b.WriteString("█")
			case top:
				b.WriteString("▀")
			case bottom:
				b.WriteString("▄")
			default:
				b.WriteByte(' ')
			}
		}
		rows = append(rows, b.String())
	}
	return rows
}

// PNG renders the code as a black and white PNG image, each module a square
// of scale by scale pixels (less than 1 is 1, more than MaxScale is
// MaxScale) with a quiet zone of quiet modules on every side, as HalfBlocks
// takes it. The same code always gives the same bytes.
func (c *Code) PNG(scale, quiet int) []byte {
	scale = min(max(scale, 1), MaxScale)
	q := quietOf(quiet)
	n := c.Size() + 2*q
	img := image.NewPaletted(image.Rect(0, 0, n*scale, n*scale),
		color.Palette{color.White, color.Black})
	for y := 0; y < n*scale; y++ {
		row := img.Pix[y*img.Stride : y*img.Stride+n*scale]
		for x := range row {
			if c.Module(x/scale-q, y/scale-q) {
				row[x] = 1
			}
		}
	}
	var buf bytes.Buffer
	// Encoding into memory fails only when the image is invalid, and this
	// one is not.
	if err := png.Encode(&buf, img); err != nil {
		panic("qr: " + err.Error())
	}
	return buf.Bytes()
}
