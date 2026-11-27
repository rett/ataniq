// Command ataniq-home provisions the home side of the checkpoint witness link.
package main

import (
	"fmt"
	"io"
	"os"
)

const usage = "usage: ataniq-home identity-init|pins|check|serve|enroll|observe|status|retire --config FILE"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Geteuid()))
}

func run(args []string, out, diag io.Writer, uid int) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "help") {
		fmt.Fprintln(out, usage)
		return 0
	}
	if len(args) == 3 && args[1] == "--config" {
		switch args[0] {
		case "identity-init", "pins", "check", "serve", "enroll", "observe", "status", "retire":
			fmt.Fprintln(diag, "not built in this release")
			return 2
		}
	}
	fmt.Fprintln(diag, usage)
	return 2
}
