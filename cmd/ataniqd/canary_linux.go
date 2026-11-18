//go:build linux && ataniq_canary

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"ataniq/internal/clientapi"
	"ataniq/internal/leadcanary"
	"ataniq/internal/leaddaemon"
)

// This file is canary mode of the command: a build with the ataniq_canary tag.
// canary_off_linux.go has the same names for every other build.

// canaryUsage is the part of the usage text that only a tagged build has.
const canaryUsage = `
This build has the ataniq_canary tag. Without --canary it is the read-only
skeleton above; with it, the daemon serves the canary operations for the one
role of CONFIG (see internal/leadcanary).

       ataniqd run --canary CONFIG
       ataniqd canary define|start|stop --config CONFIG
       ataniqd canary ticket --config CONFIG --attach-role writer|viewer
       ataniqd canary inject --config CONFIG --message-id ID --body-file FILE
       ataniqd --version

  run --canary CONFIG   the roots are the config's: --state-dir and
                        --runtime-dir are refused. The daemon refuses to start
                        when the config, a root or the marker is not right.
  canary OP             calls the operation canary.OP of the daemon in the
                        config's runtime root and prints the result as one
                        JSON line. inject waits up to 30 s, the others up to 60 s.
  --version             print the name, the version and the build tag
Exit codes of canary: 0 done; 2 usage; 10 the config; 20 no daemon; 21
unauthorized; 22 protocol error; 30 refused; 31 unavailable; 1 anything else.
`

// canaryTag is what the version output names.
const canaryTag = leaddaemon.CanaryTag

// canaryOps maps the command's words to the daemon's operations.
var canaryOps = map[string]string{
	"define": leaddaemon.OpCanaryDefine,
	"start":  leaddaemon.OpCanaryStart,
	"ticket": leaddaemon.OpCanaryTicket,
	"inject": leaddaemon.OpCanaryInject,
	"stop":   leaddaemon.OpCanaryStop,
}

// canaryFlags are the flags of run that only a tagged build has.
type canaryFlags struct {
	set  bool
	path string
}

func (c *canaryFlags) define(fs *flag.FlagSet) {
	fs.Func("canary", "canary config file; puts the daemon in canary mode", func(s string) error {
		c.set, c.path = true, s
		return nil
	})
}

// canaryRun reads the canary config when --canary was given and sets the roots
// and the canary parts of cfg. It returns applied true, or a code to end with.
func (a *app) canaryRun(c *canaryFlags, stateDir, runtimeDir string, cfg *leaddaemon.Config) (applied bool, code int) {
	if !c.set {
		return false, -1
	}
	if c.path == "" {
		fmt.Fprint(a.stderr, "ataniqd run: --canary needs a config file\n")
		return false, leaddaemon.ExitUsage
	}
	if stateDir != "" || runtimeDir != "" {
		fmt.Fprint(a.stderr, "ataniqd run: --state-dir and --runtime-dir are refused with --canary: the roots are the config's\n")
		return false, leaddaemon.ExitUsage
	}
	cc, err := leadcanary.ReadConfig(c.path)
	if err != nil {
		fmt.Fprintf(a.stderr, "ataniqd: %s\n", leaddaemon.SafeText(err.Error()))
		return false, leaddaemon.ExitConfig
	}
	cfg.StateDir, cfg.RuntimeDir = cc.StateRoot, cc.RuntimeRoot
	cfg.Canary = &leaddaemon.Canary{Config: cc, Getenv: a.getenv}
	return true, -1
}

// canaryCommand handles "--version" and "canary ...".
func (a *app) canaryCommand(args []string) (code int, ok bool) {
	switch {
	case len(args) == 1 && args[0] == "--version":
		fmt.Fprintf(a.stdout, "ataniqd %s (build tag %s)\n", version, canaryTag)
		return leaddaemon.ExitOK, true
	case len(args) > 0 && args[0] == "canary":
		return a.canaryCmd(args[1:]), true
	}
	return 0, false
}

// canaryCmd is "ataniqd canary OP --config CONFIG [...]".
func (a *app) canaryCmd(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(a.stderr, usage)
		return leaddaemon.ExitUsage
	}
	word := args[0]
	op, known := canaryOps[word]
	if !known {
		fmt.Fprintf(a.stderr, "ataniqd canary: unknown operation %q\n", leaddaemon.SafeText(word))
		fmt.Fprint(a.stderr, usage)
		return leaddaemon.ExitUsage
	}
	var config, attach, messageID, bodyFile string
	if code := a.flags("canary "+word, args[1:], func(fs *flag.FlagSet) {
		fs.StringVar(&config, "config", "", "canary config file")
		switch word {
		case "ticket":
			fs.StringVar(&attach, "attach-role", "", "writer or viewer")
		case "inject":
			fs.StringVar(&messageID, "message-id", "", "message id")
			fs.StringVar(&bodyFile, "body-file", "", "file that holds the body of the message")
		}
	}); code >= 0 {
		return code
	}
	usageErr := func(format string, args ...any) int {
		fmt.Fprintf(a.stderr, "ataniqd canary %s: "+format+"\n", append([]any{word}, args...)...)
		return leaddaemon.ExitUsage
	}
	switch {
	case config == "":
		return usageErr("--config is required")
	case word == "ticket" && attach != "writer" && attach != "viewer":
		return usageErr("--attach-role is required: writer or viewer")
	case word == "inject" && (messageID == "" || bodyFile == ""):
		return usageErr("--message-id and --body-file are required")
	}
	call := leaddaemon.CanaryCall{Op: op, AttachRole: attach, MessageID: messageID}
	if word == "inject" {
		body, err := readBody(bodyFile)
		if err != nil {
			return usageErr("--body-file: %s", leaddaemon.SafeText(err.Error()))
		}
		call.Body = body
	}
	cc, err := leadcanary.ReadConfig(config)
	if err != nil {
		fmt.Fprintf(a.stderr, "ataniqd: %s\n", leaddaemon.SafeText(err.Error()))
		return leaddaemon.ExitConfig
	}
	call.Socket = clientapi.SocketPath(cc.RuntimeRoot)
	call.Role = cc.Role.String()
	return leaddaemon.RunCanary(context.Background(), call, a.stdout, a.stderr)
}

// readBody reads the body of a message from a regular file, at most one byte
// more than the admission accepts: a longer file is read no further, and the
// daemon refuses it.
func readBody(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", errors.New("cannot be opened")
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return "", errors.New("is not a regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, leadcanary.MaxBodyBytes+1))
	if err != nil {
		return "", errors.New("cannot be read")
	}
	return string(b), nil
}
