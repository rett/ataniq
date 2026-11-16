package sessionreplay

import (
	"errors"
	"fmt"
	"time"
)

// Format identifies a recording and Version is the format version this
// package reads and writes.
const (
	Format  = "ataniq-session-replay"
	Version = 1
)

// Limits applied to every recording, whether built by a program or parsed.
const (
	MaxCols   = 500
	MaxRows   = 200
	MaxChunk  = 64 << 10
	MaxEvents = 100000
	MaxAt     = 24 * time.Hour
	MaxBytes  = 8 << 20
)

// Kind is the kind of an Event.
type Kind uint8

// The kinds of event. The zero Kind is invalid.
const (
	KindOutput Kind = iota + 1
	KindResize
	KindEnd
)

// String returns the name the format uses for the kind.
func (k Kind) String() string {
	switch k {
	case KindOutput:
		return "out"
	case KindResize:
		return "resize"
	case KindEnd:
		return "end"
	}
	return fmt.Sprintf("kind(%d)", uint8(k))
}

// Event is one record of a recording. At is the time since the start of the
// session, in whole milliseconds. Data is set for KindOutput, Cols and Rows
// for KindResize and Status and StatusKnown for KindEnd; the other fields stay
// zero. A KindEnd with StatusKnown false is a session that ended with no known
// exit status, and its Status is zero.
type Event struct {
	At          time.Duration
	Kind        Kind
	Data        []byte
	Cols        int
	Rows        int
	Status      int
	StatusKnown bool
}

// Recording is a whole session: the starting terminal size and the events in
// time order, the last of which is the only KindEnd.
type Recording struct {
	Cols   int
	Rows   int
	Events []Event
}

func checkSize(cols, rows int) error {
	if cols < 1 || cols > MaxCols || rows < 1 || rows > MaxRows {
		return fmt.Errorf("size %dx%d outside 1x1 to %dx%d", cols, rows, MaxCols, MaxRows)
	}
	return nil
}

// Validate reports the first way r breaks the rules of the format.
func (r *Recording) Validate() error {
	if r == nil {
		return errors.New("nil recording")
	}
	if err := checkSize(r.Cols, r.Rows); err != nil {
		return fmt.Errorf("header: %w", err)
	}
	if len(r.Events) == 0 {
		return errors.New("no end record")
	}
	if len(r.Events) > MaxEvents {
		return fmt.Errorf("more than %d records", MaxEvents)
	}
	var last time.Duration
	for i, e := range r.Events {
		if err := e.validate(); err != nil {
			return fmt.Errorf("record %d: %w", i+1, err)
		}
		if e.At < last {
			return fmt.Errorf("record %d: time %d ms before the previous record", i+1, e.At.Milliseconds())
		}
		last = e.At
		if (e.Kind == KindEnd) != (i == len(r.Events)-1) {
			if e.Kind == KindEnd {
				return fmt.Errorf("record %d: end record is not last", i+1)
			}
			return errors.New("last record is not an end record")
		}
	}
	return nil
}

func (e Event) validate() error {
	if e.At < 0 || e.At > MaxAt {
		return fmt.Errorf("time outside 0 to %d ms", MaxAt.Milliseconds())
	}
	if e.At%time.Millisecond != 0 {
		return errors.New("time is not a whole number of milliseconds")
	}
	switch e.Kind {
	case KindOutput:
		if len(e.Data) == 0 || len(e.Data) > MaxChunk {
			return fmt.Errorf("output chunk of %d bytes outside 1 to %d", len(e.Data), MaxChunk)
		}
		if e.Cols != 0 || e.Rows != 0 || e.Status != 0 || e.StatusKnown {
			return errors.New("output record with resize or status fields")
		}
	case KindResize:
		if err := checkSize(e.Cols, e.Rows); err != nil {
			return err
		}
		if len(e.Data) != 0 || e.Status != 0 || e.StatusKnown {
			return errors.New("resize record with data or status")
		}
	case KindEnd:
		if !e.StatusKnown && e.Status != 0 {
			return fmt.Errorf("exit status %d without StatusKnown", e.Status)
		}
		if e.Status < 0 || e.Status > 255 {
			return fmt.Errorf("exit status %d outside 0 to 255", e.Status)
		}
		if len(e.Data) != 0 || e.Cols != 0 || e.Rows != 0 {
			return errors.New("end record with data or resize fields")
		}
	default:
		return fmt.Errorf("unknown kind %d", uint8(e.Kind))
	}
	return nil
}

func (r *Recording) clone() *Recording {
	c := &Recording{Cols: r.Cols, Rows: r.Rows, Events: make([]Event, len(r.Events))}
	for i, e := range r.Events {
		e.Data = append([]byte(nil), e.Data...)
		c.Events[i] = e
	}
	return c
}
