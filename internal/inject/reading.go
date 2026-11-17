package inject

import (
	"fmt"
	"strings"
	"time"

	"ataniq/internal/inject/idle"
	"ataniq/internal/inject/osc7501"
)

// Bounds on what a Reading carries, so that a journal line or a notice made
// from it is small whatever the agent drew.
const (
	maxSignals     = 8
	maxMarks       = 8
	maxToken       = 64
	maxReadingText = 1024
)

// ReportReading is the OSC 7501 status as a check read it. It holds the state
// and the two short tokens of the report and leaves out its msg and title,
// which are free text the agent chose.
type ReportReading struct {
	// Has is true when a valid report has arrived.
	Has bool
	// Stale is true when an unreadable payload came after the last valid
	// report: the report is then not the agent's state.
	Stale bool
	// State, App and Kind are the report's fields. App and Kind are tokens
	// of letters, digits, "-" and "_", cut to 64 bytes.
	State osc7501.State
	App   string
	Kind  string
}

// ScreenReading is what the screen recogniser saw, without the text of the
// composer, which is the owner's or the agent's and may be private.
type ScreenReading struct {
	Known            bool
	Composer         bool
	ComposerEmpty    bool
	CursorInComposer bool
	// HeaderHeld is true when the composer holds the header line. The first
	// check does not look for it and leaves it false.
	HeaderHeld     bool
	Dialog         bool
	DialogMarks    []string
	BackgroundTask bool
	Working        bool
	Placeholder    bool
	PastedMarker   bool
	StateHidden    bool
	// Mode is the permission mode the footer shows (claude only), a short
	// name from the recogniser's closed list.
	Mode string
}

// Reading is what one check read, as plain data for the journal and for the
// operator. It is bounded and carries no free text of the agent beyond what
// package idle allows in a Signal (a fixed word and at most a short token).
type Reading struct {
	// State is the classification of package idle.
	State idle.State
	// Signals are the signals that said so, in the fixed order of idle.
	Signals []idle.Signal
	// Term, OwnerQuiet and ScreenQuiet are what the view said.
	Term        TermState
	OwnerQuiet  time.Duration
	ScreenQuiet time.Duration
	Report      ReportReading
	Screen      ScreenReading
	// Failed lists the conditions of the check that did not hold, each a
	// fixed phrase. It is empty when the check passed.
	Failed []string
}

// Passed reports whether every condition of the check held.
func (r *Reading) Passed() bool { return r != nil && len(r.Failed) == 0 }

// newReading makes the reading of one view and its classification.
func newReading(v View, cl idle.Classification, rep osc7501.Status, header string) *Reading {
	r := &Reading{
		State:       cl.State,
		Term:        v.Term(),
		OwnerQuiet:  v.OwnerQuiet(),
		ScreenQuiet: v.ScreenQuiet(),
		Report: ReportReading{
			Has:   rep.Has,
			Stale: rep.Stale,
			State: rep.Report.State,
			App:   clipToken(rep.Report.App),
			Kind:  clipToken(rep.Report.Kind),
		},
		Screen: ScreenReading{
			Known:            cl.Screen.Known,
			Composer:         cl.Screen.Composer,
			ComposerEmpty:    cl.Screen.ComposerEmpty,
			CursorInComposer: cl.Screen.CursorInComposer,
			HeaderHeld:       header != "" && cl.Screen.Holds(header),
			Dialog:           cl.Screen.Dialog,
			BackgroundTask:   cl.Screen.BackgroundTask,
			Working:          cl.Screen.Working,
			Placeholder:      cl.Screen.Placeholder,
			PastedMarker:     cl.Screen.PastedMarker,
			StateHidden:      cl.Screen.StateHidden,
			Mode:             clipToken(cl.Screen.Mode),
		},
	}
	for i, m := range cl.Screen.DialogMarks {
		if i == maxMarks {
			break
		}
		r.Screen.DialogMarks = append(r.Screen.DialogMarks, clipToken(m))
	}
	for i, s := range cl.Signals {
		if i == maxSignals {
			break
		}
		r.Signals = append(r.Signals, idle.Signal{Source: clipToken(s.Source), Says: clipToken(s.Says), Detail: clipText(s.Detail, maxToken+80)})
	}
	return r
}

// fail adds a failed condition. The phrases are fixed by the fence.
func (r *Reading) fail(why string) { r.Failed = append(r.Failed, why) }

// String renders the reading on one line, from fixed words and the bounded
// tokens above.
func (r *Reading) String() string {
	if r == nil {
		return "not read"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "state %s", r.State)
	if len(r.Signals) > 0 {
		b.WriteString(" (")
		for i, s := range r.Signals {
			if i > 0 {
				b.WriteString("; ")
			}
			fmt.Fprintf(&b, "%s %s", s.Source, s.Says)
			if s.Detail != "" {
				fmt.Fprintf(&b, " [%s]", s.Detail)
			}
		}
		b.WriteString(")")
	}
	fmt.Fprintf(&b, ", parser ground %t, frame open %t, bracketed paste %t", r.Term.ParserGround, r.Term.FrameOpen, r.Term.BracketedPaste)
	fmt.Fprintf(&b, ", owner quiet %s, screen quiet %s", r.OwnerQuiet.Round(time.Millisecond), r.ScreenQuiet.Round(time.Millisecond))
	fmt.Fprintf(&b, ", composer %t empty %t cursor-in %t header %t marker %t", r.Screen.Composer, r.Screen.ComposerEmpty, r.Screen.CursorInComposer, r.Screen.HeaderHeld, r.Screen.PastedMarker)
	if len(r.Failed) > 0 {
		fmt.Fprintf(&b, ", failed: %s", strings.Join(r.Failed, ", "))
	}
	return b.String()
}

// Readings renders the result as one bounded line for a journal or a notice:
// the outcome, what was written, the detail and both readings. It holds fixed
// words and the short tokens of the readings, never the message or a line the
// agent drew, and it is at most 1 KiB of printable ASCII.
func (r Result) Readings() string {
	s := fmt.Sprintf("%s, written %s", r.Outcome, r.Written)
	if r.Detail != "" {
		s += ": " + r.Detail
	}
	s += "; before: " + r.Before.String() + "; after: " + r.After.String()
	return clipText(s, maxReadingText)
}

// clipToken cuts a short token of outside text to maxToken bytes of printable
// ASCII.
func clipToken(s string) string { return clipText(s, maxToken) }

// clipText keeps printable ASCII only (anything else becomes '?') and at most
// max bytes.
func clipText(s string, max int) string {
	var b strings.Builder
	for i := 0; i < len(s) && b.Len() < max; i++ {
		c := s[i]
		if c < 0x20 || c > 0x7e {
			c = '?'
		}
		b.WriteByte(c)
	}
	return b.String()
}
