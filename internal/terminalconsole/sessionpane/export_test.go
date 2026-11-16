package sessionpane

import "strings"

// Drained returns the channel the drain goroutine closes when it returns.
func Drained(p *Pane) <-chan struct{} { return p.drained }

// ScreenText returns the emulator's screen as plain text, with trailing
// blank lines removed.
func ScreenText(p *Pane) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.TrimRight(p.emu.String(), "\n")
}
