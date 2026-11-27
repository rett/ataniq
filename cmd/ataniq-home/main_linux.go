// Command ataniq-home serves witness polls and provisions the home ledger.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
)

const usage = `usage: ataniq-home identity-init|pins|check|status|retire --config FILE
       ataniq-home observe --config FILE [--events N] [--archive FILE]
       ataniq-home serve --config FILE [--check]
       ataniq-home enroll prepare|resume --config FILE --out FILE
       ataniq-home enroll finalize --config FILE --prepared FILE --receipt FILE`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Geteuid()))
}

type command struct {
	op, configPath, output, prepared, receipt string
	check                                     bool
	events                                    int
	archive                                   string
}

func parseCommand(args []string) (command, bool) {
	c := command{events: 3}
	var count string
	if len(args) == 0 {
		return c, false
	}
	c.op, args = args[0], args[1:]
	files := map[string]*string{"--config": &c.configPath}
	switch c.op {
	case "identity-init", "pins", "check", "serve", "status", "retire":
	case "observe":
		files["--events"], files["--archive"] = &count, &c.archive
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
	delete(files, "--events")
	delete(files, "--archive")
	if count != "" {
		n, err := strconv.Atoi(count)
		if err != nil || strconv.Itoa(n) != count || n < 0 || n > 16 {
			return c, false
		}
		c.events = n
	}
	return c, len(files) == 0
}

func run(args []string, out, diag io.Writer, uid int) int {
	if len(args) == 1 && (args[0] == "help" || args[0] == "--help") {
		fmt.Fprintln(out, usage)
		return 0
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
	case "identity-init", "pins", "status", "retire":
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
	case "retire":
		return retire(c, out)
	}
	// Only an external binding directory can carry an installation marker.
	// Existing pre-enrollment preflights can run without a usable binding.
	if cmd.op != "check" && bindingOutsideState(c) {
		dir, err := lockInstallation(c.Binding, false)
		if err != nil {
			return err
		}
		defer dir.Close()
	}
	if cmd.op != "check" {
		retired, err := isRetired(c)
		if err != nil {
			return err
		}
		if retired {
			return errRetired
		}
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
	if cmd.op == "observe" {
		return observeCommand(cmd, c, pair, ep, out)
	}
	return listenAndServe(c.Listen, ep, out, net.Listen)
}
