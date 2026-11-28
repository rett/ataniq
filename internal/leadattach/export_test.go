package leadattach

import "ataniq/internal/leadholder/wire"

// TargetMatches and PinTarget let a test of the external package feed the
// lifetime pin a Welcome that the wire decoder would never hand it (epoch 0,
// an empty lead): the pin must treat those values like any other.
func (c *Client) TargetMatches(w wire.Welcome) bool { return c.targetMatches(w) }

// PinTarget completes the pin from w, as the handshake does after the role
// check.
func (c *Client) PinTarget(w wire.Welcome) { c.pinTarget(w) }
