package terminalconsole

import (
	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"ataniq/internal/terminalconsole/pairview"
)

// The pairing view's own types, under the console's names, so that the
// command that adapts the daemon's client needs no import of the view's
// package. See package pairview.
type (
	// PairingBackend is where a phone is paired: the daemon. See
	// Options.Pairing.
	PairingBackend = pairview.Backend
	// PairOffer is the one-time code a start makes, and when it ends.
	PairOffer = pairview.Offer
	// PairState is the state of the daemon's pairing.
	PairState = pairview.State
	// PairPaired is the key a confirm added.
	PairPaired = pairview.Paired
	// PairPhase is where the daemon's pairing is.
	PairPhase = pairview.Phase
)

// The phases of a pairing.
const (
	PairNone    = pairview.None
	PairWaiting = pairview.Waiting
	PairPending = pairview.Pending
	PairExpired = pairview.Expired
)

// PairWordCount is the number of words of a fingerprint in a [PairState]: the
// view's own copy of seatpair.FingerprintWords, which a test in tests/ holds
// equal.
const PairWordCount = pairview.WordCount

// NoPairing is what the P key says when the console has no pairing
// configured.
const NoPairing = "pairing is not configured"

// WithPairing returns o with a pairing backend and the origin of the
// approval app set: the address, such as "https://app.example", that the QR
// of the pair-a-phone view holds with the one-time code in its fragment.
// With a backend P opens the view; an origin that is not https with a host
// name, no port but 443 and no path (see pairview.ValidOrigin) makes the view
// say "pairing origin not configured" and draw nothing else. Without one P
// shows NoPairing and does nothing. See package pairview.
func (o Options) WithPairing(b PairingBackend, origin string) Options {
	o.Pairing, o.PairingOrigin = b, origin
	return o
}

// pairState is the console's part of the pair-a-phone view.
type pairState struct {
	// view is the open view, nil while it is closed.
	view *pairview.Model
}

// pairOpen reports whether the pair-a-phone view has the keys.
func (m *Model) pairOpen() bool { return m.pv.view != nil }

func (m *Model) pairColors() pairview.Colors {
	p := paletteFor(m.theme)
	return pairview.Colors{Fg: p.fg, Bg: p.bg, Dim: p.dim, Accent: p.accent, Warn: p.warn}
}

// openPair opens the pair-a-phone view. It closes the arrival strip. Without
// a backend it only says that pairing is not configured.
func (m *Model) openPair() tea.Cmd {
	m.ap.strip.Dismiss()
	m.sel = selection{}
	m.overlay = ""
	if m.opts.Pairing == nil {
		m.notice = NoPairing
		return nil
	}
	m.pv.view = pairview.New(pairview.Options{
		Backend: m.opts.Pairing,
		Origin:  m.opts.PairingOrigin,
		Colors:  m.pairColors(),
		After:   m.afterFn,
		Now:     m.clock,
	})
	w, h := m.apprSize()
	m.pv.view.SetSize(w, h)
	return m.pv.view.Init()
}

// resizePair gives an open view the size of the window.
func (m *Model) resizePair() {
	if m.pv.view != nil {
		w, h := m.apprSize()
		m.pv.view.SetSize(w, h)
	}
}

// pairUpdate hands msg to the open view and carries out what it asks of the
// console. Closing drops the view: the daemon's pairing is left as it is, and
// the answers still on their way find no view.
func (m *Model) pairUpdate(msg tea.Msg) tea.Cmd {
	if m.pv.view == nil {
		return nil
	}
	cmd, act := m.pv.view.Update(msg)
	switch act.Kind {
	case pairview.Close:
		m.pv.view = nil
	case pairview.Quit:
		return tea.Batch(cmd, m.quit())
	}
	return cmd
}

// pairView draws the open view over the whole window.
func (m *Model) pairView() tea.View {
	w, h := m.apprSize()
	buf := uv.NewBuffer(w, h)
	p := &painter{buf: buf, pal: paletteFor(m.theme), graphemes: m.graphemes}
	p.fill(buf.Bounds())
	m.pv.view.Draw(buf, buf.Bounds())
	return m.newView(buf.Render())
}
