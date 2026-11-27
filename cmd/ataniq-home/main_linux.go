// Command ataniq-home serves witness polls and provisions the home ledger.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
)

const usage = `usage: ataniq-home identity-init|pins|check|status|observe|retire --config FILE
       ataniq-home serve --config FILE [--check]
       ataniq-home enroll prepare|resume --config FILE --out FILE
       ataniq-home enroll finalize --config FILE --prepared FILE --receipt FILE`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Geteuid()))
}

type command struct {
	op, configPath, output, prepared, receipt string
	check                                     bool
}

func parseCommand(args []string) (command, bool) {
	var c command
	if len(args) == 0 {
		return c, false
	}
	c.op, args = args[0], args[1:]
	files := map[string]*string{"--config": &c.configPath}
	switch c.op {
	case "identity-init", "pins", "check", "serve", "status":
	case "enroll":
		if len(args) == 0 {
			return c, false
		}
		switch args[0] {
		case "prepare", "resume":
			files["--out"] = &c.output
		case "finalize":
			files["--prepared"], files["--receipt"] = &c.prepared, &c.receipt
		default:
			return c, false
		}
		c.op, args = args[0], args[1:]
	default:
		return c, false
	}
	for len(args) > 0 {
		name := args[0]
		args = args[1:]
		if name == "--check" && c.op == "serve" && !c.check {
			c.check = true
			continue
		}
		dest := files[name]
		if dest == nil || len(args) == 0 || args[0] == "" || strings.HasPrefix(args[0], "--") {
			return c, false
		}
		*dest, args = args[0], args[1:]
		delete(files, name)
	}
	return c, len(files) == 0
}

func run(args []string, out, diag io.Writer, uid int) int {
	if len(args) == 1 && (args[0] == "help" || args[0] == "--help") {
		fmt.Fprintln(out, usage)
		return 0
	}
	if len(args) > 0 && (args[0] == "observe" || args[0] == "retire") {
		fmt.Fprintln(diag, "not built in this release")
		return 2
	}
	cmd, ok := parseCommand(args)
	if !ok {
		fmt.Fprintln(diag, usage)
		return 2
	}
	if err := execute(cmd, out, uid); err != nil {
		fmt.Fprintln(diag, err)
		return 1
	}
	return 0
}

func execute(cmd command, out io.Writer, uid int) error {
	if cmd.op == "identity-init" && uid == 0 {
		return errors.New("home_key: identity-init requires the unprivileged home account (refusing root)")
	}
	level := fullPhase
	switch cmd.op {
	case "identity-init", "pins", "status":
		level = identityPhase
	}
	c, err := loadConfig(cmd.configPath, level)
	if err != nil {
		return err
	}
	switch cmd.op {
	case "identity-init":
		return identityInit(c)
	case "pins":
		return printPins(c, out)
	case "status":
		return status(c, out)
	}
	pair, err := loadPairing(c)
	if err != nil {
		return err
	}
	defer clear(pair.home.key)
	switch cmd.op {
	case "prepare", "resume", "finalize":
		return enroll(context.Background(), cmd, c, pair, out)
	}
	ep, err := homeEndpoint(c, pair)
	if err != nil {
		return err
	}
	if cmd.op == "check" || cmd.check {
		_, err = fmt.Fprintln(out, "home check ok")
		return err
	}
	return listenAndServe(c.Listen, ep, out, net.Listen)
}
