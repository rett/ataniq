//go:build !(linux && amd64)

// Command qualify is the opt-in lead sandbox qualification harness. It
// builds for Linux/amd64 only; on any other platform this stub prints a
// message and exits.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "qualify: the lead sandbox harness builds for Linux/amd64 only; nothing was started")
	os.Exit(2)
}
