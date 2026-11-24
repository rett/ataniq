//go:build linux && ataniq_canary

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"syscall"

	"ataniq/internal/clientapi"
	"ataniq/internal/leadcanary"
	"ataniq/internal/leaddaemon"
	"ataniq/internal/leadwire"
)

// This file is canary mode of the command: a build with the ataniq_canary tag.
// canary_off_linux.go has the same names for every other build.

// canaryUsage is the part of the usage text that only a tagged build has. The
// text above it is the release build's, byte for byte, and says that the build
// is read-only and that no flag changes that; this part opens by saying where
// that holds in this build.
const canaryUsage = `
In this build (tag ataniq_canary) the text above holds only without --canary.
With run --canary the daemon is not read-only: it serves the canary operations
for the one role of CONFIG, which define, start and stop a lead and send it a
message (see internal/leadcanary).

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

With run --scratch the daemon serves the lead control plane for the leads of the
scratch CONFIG (see internal/leadcanary): the operations lead.list, lead.define,
lead.start, lead.stop, lead.ticket and lead.inject, which the console calls. It
serves no canary operation.

       ataniqd run --scratch CONFIG

  run --scratch CONFIG  the roots are the config's: --state-dir, --runtime-dir
                        and --canary are refused. The daemon refuses to start
                        when the config, a root or the root marker (written by
                        scratch-init) is not right, or when the command table
                        or the record of a lead cannot be read.

Under --scratch the desk also switches each lead's written replies (its chat),
read only: off or read, off for every lead whenever the daemon starts. The
seat reads only the leads whose chat is read. Sending is not built.

       ataniqd chat list --config CONFIG
       ataniqd chat set KEY off|read --config CONFIG
       ataniqd chat purge KEY --config CONFIG

  chat list             prints the chat of every lead of the scratch CONFIG
                        as one JSON line: the switch, whether it is stopped,
                        and counts, never a reply's text.
  chat set KEY off|read sets the switch of the lead KEY at once and prints its
                        row. read sets the lead's reply folder aside, unread,
                        in its quarantine and makes a new one; it needs one of
                        the quarantine's four names free (exit 31 otherwise).
  chat purge KEY        removes what the lead's quarantine holds, in bounded
                        steps (run it again while "left" is not 0), and prints
                        the counts. A folder the daemon cannot open is left
                        for the owner to delete by hand.
Exit codes of canary and chat: 0 done; 2 usage; 10 the config; 20 no daemon;
21 unauthorized; 22 protocol error; 30 refused; 31 unavailable; 1 anything
else.
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
	// scratchSet and scratch are --scratch: the scratch config file.
	scratchSet bool
	scratch    string
}

func (c *canaryFlags) define(fs *flag.FlagSet) {
	fs.Func("canary", "canary config file; puts the daemon in canary mode", func(s string) error {
		c.set, c.path = true, s
		return nil
	})
	fs.Func("scratch", "scratch config file; puts the daemon in scratch mode, the lead control plane", func(s string) error {
		c.scratchSet, c.scratch = true, s
		return nil
	})
}

// canaryRun reads the canary config when --canary was given and sets the roots
// and the canary parts of cfg. It returns applied true, or a code to end with.
func (a *app) canaryRun(c *canaryFlags, stateDir, runtimeDir string, cfg *leaddaemon.Config) (applied bool, code int) {
	if c.scratchSet {
		return a.scratchRun(c, stateDir, runtimeDir, cfg)
	}
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

// scratchRun reads the scratch config when --scratch was given and sets the
// roots and the scratch part of cfg. --scratch excludes --canary and the root
// flags: the roots are the config's, and the daemon serves the lead operations
// or the canary's, never both.
func (a *app) scratchRun(c *canaryFlags, stateDir, runtimeDir string, cfg *leaddaemon.Config) (applied bool, code int) {
	switch {
	case c.scratch == "":
		fmt.Fprint(a.stderr, "ataniqd run: --scratch needs a config file\n")
		return false, leaddaemon.ExitUsage
	case c.set:
		fmt.Fprint(a.stderr, "ataniqd run: --scratch and --canary exclude each other\n")
		return false, leaddaemon.ExitUsage
	case stateDir != "" || runtimeDir != "":
		fmt.Fprint(a.stderr, "ataniqd run: --state-dir and --runtime-dir are refused with --scratch: the roots are the config's\n")
		return false, leaddaemon.ExitUsage
	}
	sc, err := leadcanary.ReadScratchConfig(c.scratch)
	if err != nil {
		fmt.Fprintf(a.stderr, "ataniqd: %s\n", leaddaemon.SafeText(err.Error()))
		return false, leaddaemon.ExitConfig
	}
	cfg.StateDir, cfg.RuntimeDir = sc.StateRoot, sc.RuntimeRoot
	cfg.Scratch = &leaddaemon.Scratch{Config: sc, Getenv: a.getenv}
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
	case len(args) > 0 && args[0] == "chat":
		return a.chatCmd(args[1:]), true
	}
	return 0, false
}

// chatCmd is "ataniqd chat list --config CONFIG", "ataniqd chat set KEY
// off|read --config CONFIG" and "ataniqd chat purge KEY --config CONFIG": the
// desk's switch of the scratch leads' chat and the purge of a lead's
// quarantine.
func (a *app) chatCmd(args []string) int {
	usageErr := func(format string, args ...any) int {
		fmt.Fprintf(a.stderr, "ataniqd chat: "+format+"\n", args...)
		fmt.Fprint(a.stderr, usage)
		return leaddaemon.ExitUsage
	}
	if len(args) == 0 {
		return usageErr("list, set or purge is required")
	}
	call := leaddaemon.ChatCall{}
	rest := args[1:]
	switch args[0] {
	case "list":
		call.Op = leadwire.OpChatList
	case "set":
		if len(rest) < 2 {
			return usageErr("set needs a lead's key and off or read")
		}
		call.Op, call.Key, call.Chat = leadwire.OpChatSet, rest[0], rest[1]
		rest = rest[2:]
		switch call.Chat {
		case leadwire.ChatOff, leadwire.ChatRead:
		case leadwire.ChatSend:
			return usageErr("send is not built; the switch is off or read")
		default:
			return usageErr("the switch is off or read")
		}
	case "purge":
		if len(rest) < 1 || rest[0] == "" || rest[0][0] == '-' {
			return usageErr("purge needs a lead's key")
		}
		call.Op, call.Key = leadwire.OpChatPurge, rest[0]
		rest = rest[1:]
	default:
		return usageErr("unknown operation %q", leaddaemon.SafeText(args[0]))
	}
	var config string
	if code := a.flags("chat "+args[0], rest, func(fs *flag.FlagSet) {
		fs.StringVar(&config, "config", "", "scratch config file")
	}); code >= 0 {
		return code
	}
	if config == "" {
		return usageErr("--config is required")
	}
	sc, err := leadcanary.ReadScratchConfig(config)
	if err != nil {
		fmt.Fprintf(a.stderr, "ataniqd: %s\n", leaddaemon.SafeText(err.Error()))
		return leaddaemon.ExitConfig
	}
	call.Socket = clientapi.SocketPath(sc.RuntimeRoot)
	return leaddaemon.RunChat(context.Background(), call, a.stdout, a.stderr)
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
// daemon refuses it as too long.
//
// The file is opened without blocking, so that a FIFO with no writer (or a
// device) returns at once, and it is judged by its handle before anything is
// read: only a regular file is read.
func readBody(path string) (string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", errors.New("cannot be opened")
	}
	defer f.Close()
	fi, err := f.Stat() // fstat of the handle, not of the name
	if err != nil || !fi.Mode().IsRegular() {
		return "", errors.New("is not a regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxBodyRead))
	if err != nil {
		return "", errors.New("cannot be read")
	}
	return string(b), nil
}

// maxBodyRead is how much of a body file is read: one byte more than the
// admission accepts, which is enough for the daemon to see that it is too long.
const maxBodyRead = leadcanary.MaxBodyBytes + 1
