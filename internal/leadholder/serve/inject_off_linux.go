//go:build linux && !ataniq_canary

package serve

import "ataniq/internal/leadholder/wire"

// InjectConfig is the injection's seam in Config. In a release build it is
// empty: there is no deliverer to give, so the control server can only answer
// an inject request with disabled. The build with the ataniq_canary tag
// (inject_canary_linux.go) has the deliverer.
type InjectConfig struct{}

// injectState is the injection's state in a Server: none in a release build.
type injectState struct{}

// inject answers an inject request: always disabled, as the session's stub
// logs it. The request is not looked at, and its text least of all: nothing of
// it reaches the event log, a counter or any other output.
func (s *Server) inject(wire.Request) wire.Response { return s.injectDisabled() }
