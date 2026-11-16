package sessionreplay_test

import (
	"strings"
	"testing"
	"time"

	"ataniq/internal/terminalconsole/sessionreplay"
)

func sample() *sessionreplay.Recording {
	return &sessionreplay.Recording{Cols: 100, Rows: 30, Events: []sessionreplay.Event{
		{At: 0, Kind: sessionreplay.KindOutput, Data: []byte("a")},
		{At: 250 * time.Millisecond, Kind: sessionreplay.KindResize, Cols: 80, Rows: 24},
		{At: 250 * time.Millisecond, Kind: sessionreplay.KindOutput, Data: []byte("\x1b[")},
		{At: 900 * time.Millisecond, Kind: sessionreplay.KindEnd, Status: 3, StatusKnown: true},
	}}
}

func TestValidateAcceptsSample(t *testing.T) {
	if err := sample().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejects(t *testing.T) {
	big := strings.Repeat("x", sessionreplay.MaxChunk+1)
	cases := map[string]func(r *sessionreplay.Recording){
		"zero columns":     func(r *sessionreplay.Recording) { r.Cols = 0 },
		"too many columns": func(r *sessionreplay.Recording) { r.Cols = sessionreplay.MaxCols + 1 },
		"too many rows":    func(r *sessionreplay.Recording) { r.Rows = sessionreplay.MaxRows + 1 },
		"no events":        func(r *sessionreplay.Recording) { r.Events = nil },
		"no end":           func(r *sessionreplay.Recording) { r.Events = r.Events[:3] },
		"end not last": func(r *sessionreplay.Recording) {
			r.Events[2].Kind, r.Events[2].Data, r.Events[2].Status = sessionreplay.KindEnd, nil, 0
		},
		"time goes back":      func(r *sessionreplay.Recording) { r.Events[2].At = time.Millisecond },
		"negative time":       func(r *sessionreplay.Recording) { r.Events[0].At = -time.Millisecond },
		"time past the limit": func(r *sessionreplay.Recording) { r.Events[3].At = sessionreplay.MaxAt + time.Millisecond },
		"fractional time":     func(r *sessionreplay.Recording) { r.Events[0].At = 1500 * time.Microsecond },
		"empty chunk":         func(r *sessionreplay.Recording) { r.Events[0].Data = nil },
		"chunk too large":     func(r *sessionreplay.Recording) { r.Events[0].Data = []byte(big) },
		"output with status":  func(r *sessionreplay.Recording) { r.Events[0].Status = 1 },
		"resize to zero":      func(r *sessionreplay.Recording) { r.Events[1].Rows = 0 },
		"resize with data":    func(r *sessionreplay.Recording) { r.Events[1].Data = []byte("x") },
		"status over 255":     func(r *sessionreplay.Recording) { r.Events[3].Status = 256 },
		"negative status":     func(r *sessionreplay.Recording) { r.Events[3].Status = -1 },
		"status not known":    func(r *sessionreplay.Recording) { r.Events[3].StatusKnown = false },
		"output status known": func(r *sessionreplay.Recording) { r.Events[0].StatusKnown = true },
		"resize status known": func(r *sessionreplay.Recording) { r.Events[1].StatusKnown = true },
		"end with data":       func(r *sessionreplay.Recording) { r.Events[3].Data = []byte("x") },
		"unknown kind":        func(r *sessionreplay.Recording) { r.Events[0].Kind = 0 },
	}
	for name, mutate := range cases {
		r := sample()
		mutate(r)
		if err := r.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	var nilRec *sessionreplay.Recording
	if nilRec.Validate() == nil {
		t.Error("nil recording accepted")
	}
}

func TestTooManyEvents(t *testing.T) {
	r := &sessionreplay.Recording{Cols: 1, Rows: 1}
	for i := 0; i <= sessionreplay.MaxEvents; i++ {
		r.Events = append(r.Events, sessionreplay.Event{Kind: sessionreplay.KindOutput, Data: []byte("x")})
	}
	r.Events = append(r.Events, sessionreplay.Event{Kind: sessionreplay.KindEnd})
	if r.Validate() == nil {
		t.Fatal("accepted")
	}
}
