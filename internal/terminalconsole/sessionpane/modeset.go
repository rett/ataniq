package sessionpane

import (
	"github.com/charmbracelet/x/ansi"
)

// ModeSet is the set of terminal modes that are set, from the emulator's
// fixed table of recorded modes: the numbers of the DEC private modes
// (CSI ? n h) and of the ANSI modes (CSI n h), each in ascending order. A
// list with no mode is nil.
type ModeSet struct {
	DEC, ANSI []int
}

// Has reports whether the DEC private mode n (ansiMode false) or the ANSI
// mode n is in the set.
func (m ModeSet) Has(n int, ansiMode bool) bool {
	list := m.DEC
	if ansiMode {
		list = m.ANSI
	}
	for _, v := range list {
		if v == n {
			return true
		}
	}
	return false
}

// ModeSet returns the modes that are set, including 2026 (synchronized
// output) as the pane follows it. Unlike Modes, it is the emulator's state
// whether or not the session has ended.
func (p *Pane) ModeSet() ModeSet {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.modeSet()
}

func (p *Pane) modeSet() ModeSet {
	var set ModeSet
	has2026 := false
	for _, ms := range p.emu.ModeSettings() {
		if !ms.Setting.IsSet() {
			continue
		}
		switch m := ms.Mode.(type) {
		case ansi.DECMode:
			set.DEC = append(set.DEC, int(m))
			has2026 = has2026 || m == ansi.ModeSynchronizedOutput
		case ansi.ANSIMode:
			set.ANSI = append(set.ANSI, int(m))
		}
	}
	// The pane follows mode 2026 through the emulator's callback, so the two
	// agree; the union keeps the pane's own, which answers DECRQM, whatever
	// happens.
	if p.syncOut.on && !has2026 {
		set.DEC = insertSorted(set.DEC, int(ansi.ModeSynchronizedOutput))
	}
	return set
}

func insertSorted(list []int, n int) []int {
	i := 0
	for i < len(list) && list[i] < n {
		i++
	}
	list = append(list, 0)
	copy(list[i+1:], list[i:])
	list[i] = n
	return list
}

// Quiescent reports whether the parser is at its ground state: no escape
// sequence, no control string and no UTF-8 character is half received. An
// open grapheme cluster does not make it false: a snapshot carries it. A
// snapshot can be taken only when the pane is quiescent.
func (p *Pane) Quiescent() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.emu.Quiescent()
}
