//go:build !linux

package assert

import "errors"

// Canary is the target of assertion 3. It is implemented on Linux only: on
// another system NewCanary fails with errors.ErrUnsupported.
type Canary struct {
	TCP      string
	Abstract string
}

// NewCanary fails with errors.ErrUnsupported.
func NewCanary() (*Canary, error) { return nil, errors.ErrUnsupported }

// Hits is always 0.
func (c *Canary) Hits() int64 { return 0 }

// TCPHits is always 0.
func (c *Canary) TCPHits() int64 { return 0 }

// AbstractHits is always 0.
func (c *Canary) AbstractHits() int64 { return 0 }

// Close does nothing.
func (c *Canary) Close() error { return nil }
