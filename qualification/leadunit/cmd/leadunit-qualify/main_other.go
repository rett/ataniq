//go:build !linux

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "leadunit-qualify: Linux with a systemd user manager is required")
	os.Exit(2)
}
