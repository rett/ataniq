//go:build !linux

package assert

import (
	"errors"
	"time"
)

// Canary is the target of assertion 3. It is implemented on Linux only: on
// another system NewCanary fails with errors.ErrUnsupported.
type Canary struct {
	TCP      string
	TCP2     string
	Abstract string
}

// NewCanary fails with errors.ErrUnsupported.
func NewCanary() (*Canary, error) { return nil, errors.ErrUnsupported }

// Hits is always 0.
func (c *Canary) Hits() int64 { return 0 }

// TCPHits is always 0.
func (c *Canary) TCPHits() int64 { return 0 }

// TCP2Hits is always 0.
func (c *Canary) TCP2Hits() int64 { return 0 }

// AbstractHits is always 0.
func (c *Canary) AbstractHits() int64 { return 0 }

// AcceptErrors is always 0.
func (c *Canary) AcceptErrors() int64 { return 0 }

// Stopped is always true: nothing runs.
func (c *Canary) Stopped() bool { return true }

// Settle fails with errors.ErrUnsupported.
func (c *Canary) Settle(time.Duration) error { return errors.ErrUnsupported }

// DefaultSettleTimeout is how long CheckCanary waits for the canary to settle.
const DefaultSettleTimeout = 5 * time.Second

// Close does nothing.
func (c *Canary) Close() error { return nil }
