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
