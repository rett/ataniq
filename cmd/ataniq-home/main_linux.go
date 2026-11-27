// Command ataniq-home provisions the home side of the checkpoint witness link.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
)

const usage = "usage: ataniq-home identity-init|pins|check|serve|enroll|observe|status|retire --config FILE"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Geteuid()))
}

func run(args []string, out, diag io.Writer, uid int) int {
	if len(args) > 0 {
		switch args[0] {
		case "help", "--help":
			if len(args) == 1 {
				fmt.Fprintln(out, usage)
				return 0
			}
		case "serve", "enroll", "observe", "status", "retire":
			fmt.Fprintln(diag, "not built in this release")
			return 2
		case "identity-init", "pins", "check":
			if len(args) == 3 && args[1] == "--config" {
				if err := execute(args[0], args[2], out, uid); err != nil {
					fmt.Fprintln(diag, err)
					return 1
				}
				return 0
			}
		}
	}
	fmt.Fprintln(diag, usage)
	return 2
}

func execute(op, path string, out io.Writer, uid int) error {
	if op == "identity-init" && uid == 0 {
		return errors.New("home_key: identity-init requires the unprivileged home account (refusing root)")
	}
	level := identityPhase
	if op == "check" {
		level = fullPhase
	}
	c, err := loadConfig(path, level)
	if err != nil {
		return err
	}
	switch op {
	case "identity-init":
		return identityInit(c)
	case "pins":
		return printPins(c, out)
	default:
		if err := check(c); err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, "home check ok")
		return err
	}
}
