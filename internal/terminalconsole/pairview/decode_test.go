package pairview

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestTheDrawnCellsDecodeToThePairingAddress renders the cells the view
// draws to an image, as a terminal would show them (a dark module is the
// foreground of a half block, a light one its background) and reads the
// image back with zbarimg. It is the one test that handles the code as text,
// and its code is synthetic. It skips without zbarimg.
func TestTheDrawnCellsDecodeToThePairingAddress(t *testing.T) {
	zbar, err := exec.LookPath("zbarimg")
	if err != nil {
		t.Skip("zbarimg is not installed")
	}
	for name, colors := range map[string]Colors{"dark": darkColors(), "light": lightColors()} {
		b := &fakeBackend{}
		r := newRig(t, b, testOrigin, 80, 24)
		r.m.o.Colors = colors
		r.start()
		const w, h, scale = 80, 24, 6
		buf := draw(r.m, w, h)
		img := image.NewGray(image.Rect(0, 0, w*scale, h*2*scale))
		for i := range img.Pix {
			img.Pix[i] = 0xff
		}
		fill := func(x, half int, dark bool) {
			if !dark {
				return
			}
			for dy := 0; dy < scale; dy++ {
				for dx := 0; dx < scale; dx++ {
					img.SetGray(x*scale+dx, half*scale+dy, color.Gray{Y: 0})
				}
			}
		}
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				c := buf.CellAt(x, y)
				// Only the QR's own cells (black on white) are part of the image.
				if c == nil || c.Style.Fg != qrDark || c.Style.Bg != qrLight {
					continue
				}
				fill(x, 2*y, strings.ContainsAny(c.Content, "█▀"))
				fill(x, 2*y+1, strings.ContainsAny(c.Content, "█▄"))
			}
		}
		var png2 bytes.Buffer
		if err := png.Encode(&png2, img); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "qr.png")
		if err := os.WriteFile(path, png2.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(zbar, "--raw", "-q", path).Output()
		if err != nil {
			t.Fatalf("%s: zbarimg: %v", name, err)
		}
		want := testOrigin + "/pair/#" + synthCode
		if got := strings.TrimSpace(string(out)); got != want {
			t.Errorf("%s: the QR reads %q, want %q", name, got, want)
		}
	}
}
