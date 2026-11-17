//go:build !linux

package gitenv

import "errors"

// runOnTerminal has no implementation outside Linux. The pager case then
// fails with this message instead of the package failing to compile.
func runOnTerminal(argv, env []string, dir string) (string, error) {
	return "", errors.New("gitenv: pseudo-terminal test unsupported on this system")
}
