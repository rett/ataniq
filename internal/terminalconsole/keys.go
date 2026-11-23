package terminalconsole

import (
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// ReturnKey is the key that leaves session focus. It is a Ctrl chord on one
// ASCII key.
//
// The default is Ctrl+\, which Bubble Tea's input decoder reads from both the
// legacy byte 0x1c and the kitty keyboard form CSI 92;5u. In the legacy form
// Ctrl+4 is the same byte, so it returns too; in the kitty form it is
// CSI 52;5u and does not. Ctrl+Enter is never bound.
type ReturnKey struct{ code rune }

// DefaultReturnKey is Ctrl+\.
var DefaultReturnKey = ReturnKey{code: '\\'}

// ErrReturnKey is returned by ParseReturnKey. Its text does not repeat the
// rejected input.
var ErrReturnKey = errors.New("a return key is ctrl+ and one letter, or ctrl+\\ or ctrl+], and not ctrl+c, ctrl+h, ctrl+i, ctrl+j, ctrl+k, ctrl+m or ctrl+z")

// ParseReturnKey parses text such as "ctrl+\" or "ctrl+g". The letters that
// the terminal or the console already uses are refused: C is an interrupt, H,
// I, J and M are backspace, tab and enter, K opens the action list and Z
// suspends.
func ParseReturnKey(s string) (ReturnKey, error) {
	rest, ok := strings.CutPrefix(strings.ToLower(s), "ctrl+")
	if !ok || len(rest) != 1 {
		return ReturnKey{}, ErrReturnKey
	}
	switch c := rune(rest[0]); {
	case c == '\\' || c == ']':
		return ReturnKey{code: c}, nil
	case c >= 'a' && c <= 'z' && !strings.ContainsRune("chijkmz", c):
		return ReturnKey{code: c}, nil
	}
	return ReturnKey{}, ErrReturnKey
}

// Spec returns the key as ParseReturnKey reads it, such as `ctrl+\`.
func (r ReturnKey) Spec() string {
	if r.code == 0 {
		r = DefaultReturnKey
	}
	return "ctrl+" + string(r.code)
}

// String returns the key as the console shows it, such as "Ctrl+\".
func (r ReturnKey) String() string {
	if r.code == 0 {
		r = DefaultReturnKey
	}
	return "Ctrl+" + strings.ToUpper(string(r.code))
}

// lockMods are lock states a terminal may report with a key. With the kitty
// keyboard protocol they come with any chord or function key, and they never
// change which key it is.
const lockMods = tea.ModCapsLock | tea.ModNumLock | tea.ModScrollLock

// withoutLocks returns m without its lock states.
func withoutLocks(m tea.KeyMod) tea.KeyMod { return m &^ lockMods }

// matches reports whether k is the return key: Ctrl with the key's code and
// no other modifier, lock states aside, and no text.
func (r ReturnKey) matches(k tea.KeyPressMsg) bool {
	if r.code == 0 {
		r = DefaultReturnKey
	}
	return withoutLocks(k.Mod) == tea.ModCtrl && k.Code == r.code && k.Text == ""
}

// isLeadsKey reports whether k is the key that opens the leads view: capital
// L, typed as shift and l or as the text "L", with no other modifier but lock
// states.
func isLeadsKey(k tea.KeyPressMsg) bool { return isCapital(k, 'L') }

// isPairKey reports whether k is the key that opens the pair-a-phone view:
// capital P, in the same forms as the leads key.
func isPairKey(k tea.KeyPressMsg) bool { return isCapital(k, 'P') }

// isCapital reports whether k is the capital letter c, typed as shift and the
// small letter or as the text c, with no other modifier but lock states.
func isCapital(k tea.KeyPressMsg, c rune) bool {
	if k.Mod&^(tea.ModShift|lockMods) != 0 {
		return false
	}
	if k.Text != "" {
		return k.Text == string(c)
	}
	return k.Code == c || (k.Code == c+('a'-'A') && k.Mod&tea.ModShift != 0)
}

// opensLeads reports whether the key press opens the leads view. Like g for
// approvals it is a key of console focus: while the session has the focus the
// key goes to the session, and while an approvals screen, the overlay or zoom
// has the keys it is theirs. Below the cockpit's size the single-column pages
// have it, but not over a live session, whose one page has no keys of its
// own.
func (m *Model) opensLeads(k tea.KeyPressMsg) bool {
	if !isLeadsKey(k) || m.apprOpen() || m.leadsOpen() || m.pairOpen() {
		return false
	}
	if !m.layout().cockpit {
		return !m.live.on()
	}
	return m.focus == consoleFocus && !m.zoom && m.overlay == ""
}

// opensPair reports whether the key press opens the pair-a-phone view. It is
// a key of console focus like L: while the session has the focus the key goes
// to the session, and while an approvals screen, the leads view, the overlay
// or zoom has the keys it is theirs. Below the cockpit's size the
// single-column pages have it, but not over a live session. Without a
// pairing backend the key is still taken and says NoPairing.
func (m *Model) opensPair(k tea.KeyPressMsg) bool {
	if !isPairKey(k) || m.apprOpen() || m.leadsOpen() || m.pairOpen() {
		return false
	}
	if !m.layout().cockpit {
		return !m.live.on()
	}
	return m.focus == consoleFocus && !m.zoom && m.overlay == ""
}

// pairHelp is what the help page adds to its approvals line: the key of the
// pair-a-phone view, only when pairing is configured.
func (m *Model) pairHelp() string {
	if m.opts.Pairing == nil {
		return ""
	}
	return "   P: pair a phone"
}
