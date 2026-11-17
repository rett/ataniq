package sessionpane_test

import (
	"testing"

	"ataniq/internal/terminalconsole/sessionpane"
	"ataniq/internal/terminalconsole/sessionreplay"
)

// The tests use the replay package's synthetic fixtures. Only the tests do:
// the pane itself never imports the replay.

func fixture(t testing.TB, name string) *sessionreplay.Recording {
	t.Helper()
	rec, err := sessionreplay.LoadFixture(name)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

// apply gives one recorded event to the pane, as the cockpit will.
func apply(p *sessionpane.Pane, e sessionreplay.Event) {
	switch e.Kind {
	case sessionreplay.KindOutput:
		p.Write(e.Data)
	case sessionreplay.KindResize:
		p.Resize(e.Cols, e.Rows)
	case sessionreplay.KindEnd:
		p.End(e.Status, e.StatusKnown)
	}
}

// output returns the recording's output bytes joined, without its end.
func output(rec *sessionreplay.Recording) []byte {
	var b []byte
	for _, e := range rec.Events {
		if e.Kind == sessionreplay.KindOutput {
			b = append(b, e.Data...)
		}
	}
	return b
}

// play makes a pane at the recording's size and applies every event.
func play(t *testing.T, rec *sessionreplay.Recording, o sessionpane.Options) *sessionpane.Pane {
	t.Helper()
	p := newPane(t, rec.Cols, rec.Rows, o)
	for _, e := range rec.Events {
		apply(p, e)
	}
	return p
}
