//go:build !linux

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "ataniqd: this command runs on Linux only")
	os.Exit(1)
}
