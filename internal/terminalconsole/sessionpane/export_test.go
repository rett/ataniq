package sessionpane

import (
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
)

// Drained returns the channel the drain goroutine closes when it returns.
func Drained(p *Pane) <-chan struct{} { return p.drained }

// ScreenText returns the emulator's screen as plain text, with trailing
// blank lines removed.
func ScreenText(p *Pane) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.TrimRight(p.emu.String(), "\n")
}

// CleanTitle is cleanTitle, for tests.
func CleanTitle(b []byte) string { return cleanTitle(b) }

// ScrollbackLen returns how many lines the emulator's main screen keeps in
// its scrollback.
func ScrollbackLen(p *Pane) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.emu.ScrollbackLen()
}

// SetCell puts c in the emulator's screen at x, y, as no output can: the
// emulator stores no control character in a cell, so a test of what Text
// does with one sets it here.
func SetCell(p *Pane, x, y int, c *uv.Cell) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.emu.SetCell(x, y, c)
}
