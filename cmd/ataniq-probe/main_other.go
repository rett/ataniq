//go:build !linux

package main

import (
	"fmt"
	"io"
)

// execute is the whole command where it is not Linux: it says so and exits.
// The modes need namespaces, descriptors and a controlling terminal as Linux
// gives them.
func execute(args []string, stderr io.Writer) int {
	fmt.Fprintln(stderr, "ataniq-probe: this command runs on Linux only; nothing was done")
	return exitSetup
}
