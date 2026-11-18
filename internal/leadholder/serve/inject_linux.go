//go:build linux && ataniq_canary

package serve

import (
	"context"

	"ataniq/internal/leadholder/wire"
)

// Deliverer delivers one message to the lead and says what became of it. It
// returns the wire's result, so this package knows nothing of how the message
// is written. The context ends at [InjectTimeout] or when the service closes;
// a deliverer must give up at the end of it.
type Deliverer interface {
	Deliver(ctx context.Context, messageID, text string) wire.InjectResult
}

// InjectConfig is the injection's seam in Config. A nil Deliverer keeps the
// gate closed: the control server answers every inject request with disabled,
// as a release build does.
type InjectConfig struct{ Deliverer Deliverer }

// injectState is the injection's state in a Server.
type injectState struct{}

// inject answers an inject request. In this step the gate is always closed.
func (s *Server) inject(wire.Request) wire.Response { return s.injectDisabled() }
