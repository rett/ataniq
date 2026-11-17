package command

import (
	"errors"
	"fmt"
	"time"
)

// Limits are the caps and time limits of a Machine.
type Limits struct {
	// MaxTextBytes is the most bytes in one whole text, header included
	// (design 1.3: 4 KiB).
	MaxTextBytes int
	// MaxQueueMessages is the most commands a target's queue may hold
	// (16). A command holds a place from admission until it is delivered,
	// refused or resolved; an uncertain command keeps its place.
	MaxQueueMessages int
	// MaxQueueBytes is the most bytes of text a target's queue may hold,
	// counting the message being admitted (64 KiB).
	MaxQueueBytes int
	// MaxPerWindow is the most messages admitted from one sender in
	// RateWindow (6 a minute).
	MaxPerWindow int
	// RateWindow is the width of the sliding rate window.
	RateWindow time.Duration
	// ConfirmTimeout is how long after Enter a submitted command waits for
	// evidence before it becomes uncertain (30 s).
	ConfirmTimeout time.Duration
	// QueueTimeLimit is how long after admission a queued head may wait for
	// an idle lead before it is refused as not idle. The design gives no
	// number, so there is no default: the caller must set it.
	QueueTimeLimit time.Duration
}

// DefaultLimits returns the design's caps and the 30 s confirmation limit.
// QueueTimeLimit is zero and must be set before the limits are used.
func DefaultLimits() Limits {
	return Limits{
		MaxTextBytes:     4096,
		MaxQueueMessages: 16,
		MaxQueueBytes:    64 << 10,
		MaxPerWindow:     6,
		RateWindow:       time.Minute,
		ConfirmTimeout:   30 * time.Second,
	}
}

// Validate returns an error naming every field that is not positive.
func (l Limits) Validate() error {
	var errs []error
	check := func(name string, ok bool) {
		if !ok {
			errs = append(errs, fmt.Errorf("command: limit %s must be positive", name))
		}
	}
	check("MaxTextBytes", l.MaxTextBytes > 0)
	check("MaxQueueMessages", l.MaxQueueMessages > 0)
	check("MaxQueueBytes", l.MaxQueueBytes > 0)
	check("MaxPerWindow", l.MaxPerWindow > 0)
	check("RateWindow", l.RateWindow > 0)
	check("ConfirmTimeout", l.ConfirmTimeout > 0)
	check("QueueTimeLimit", l.QueueTimeLimit > 0)
	return errors.Join(errs...)
}
