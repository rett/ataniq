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
		case "identity-init", "pins":
			if args[0] == "identity-init" && uid == 0 {
				fmt.Fprintln(diag, "home_key: identity-init must run as the home account, not root")
				return 1
			}
			c, err := loadConfig(args[2], identityPhase)
			if err == nil {
				if args[0] == "identity-init" {
					err = identityInit(c)
				} else {
					err = printPins(c, out)
				}
			}
			if err != nil {
				fmt.Fprintln(diag, err)
				return 1
			}
			return 0
		case "check", "serve", "enroll", "observe", "status", "retire":
			fmt.Fprintln(diag, "not built in this release")
			return 2
		}
	}
	fmt.Fprintln(diag, usage)
	return 2
}
