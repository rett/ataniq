//go:build !linux

package boundedexec

import "context"

func run(ctx context.Context, c Cmd) (Result, error) {
	return Result{Exit: -1}, ErrUnsupported
}
