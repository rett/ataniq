//go:build !linux

package boundedexec

import (
	"context"
	"os"
)

// stopSignals is the list that [StopSignals] returns where there is no
// process group: interrupt alone.
func stopSignals() []os.Signal { return []os.Signal{os.Interrupt} }

func run(ctx context.Context, c Cmd) (Result, error) {
	return Result{Exit: -1}, ErrUnsupported
}
