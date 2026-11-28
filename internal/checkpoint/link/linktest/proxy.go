// Package linktest supplies deterministic frame faults for witness qualification.
package linktest

import (
	"io"
	"sync/atomic"

	"ataniq/internal/checkpoint/link/internal/frame"
)

// Proxy forwards one checkpoint exchange between two streams. The caller
// supplies authenticated TLS streams and owns their deadlines and closure.
// A Proxy may be shared by concurrent connections on both witness endpoints.
type Proxy struct{ drop atomic.Bool }

// DropNextReply drops the next complete mutation receipt. Refusals and reads
// leave the fault armed. Arm it before submitting the mutation under test.
func (p *Proxy) DropNextReply() { p.drop.Store(true) }

// Forward returns after the exchange. Closing both connections on return
// makes a dropped reply appear exactly like a disconnect after commit.
func (p *Proxy) Forward(client, witness io.ReadWriter) error {
	req, err := frame.ReadMessage(client)
	if err != nil {
		return err
	}
	if err := req.WriteToStream(witness); err != nil {
		return err
	}
	if req.Kind == frame.Idle {
		return nil
	}
	first, err := frame.ReadMessage(witness)
	if err != nil {
		return err
	}
	if first.Kind == frame.Receipt && p.drop.CompareAndSwap(true, false) {
		return nil
	}
	if err := first.WriteToStream(client); err != nil {
		return err
	}
	if first.Kind == frame.View {
		for _, kind := range []frame.Kind{frame.Fresh, frame.Prior} {
			m, err := frame.ReadMessage(witness)
			if err != nil {
				return err
			}
			if m.Kind != kind {
				return frame.ErrProtocol
			}
			if err := m.WriteToStream(client); err != nil {
				return err
			}
		}
	}
	return nil
}
