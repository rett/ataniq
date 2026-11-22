// Command ah-switch is the one switch of the authority host: it activates a
// release folder under /opt/authority-host/releases, rolls back to the previous
// one, or shows the state. See deploy/authority-host/README.md.
//
//	ah-switch [--root DIR [--allow-exec]] [--dry] [--mode manual|signed] [--noaicheck PATH] [--from INCOMING_DIR] ID
//	ah-switch [--root DIR [--allow-exec]] [--dry] [--mode manual|signed] [--noaicheck PATH] --rollback-to previous
//	ah-switch [--root DIR] status
//	ah-switch [--root DIR [--allow-exec]] [--dry] recover --counter N
//
// Exit codes: 0 ok, 1 failed (the outcome is recorded), 2 usage, 3 locked.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"ataniq/internal/authorityhost/switcher"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

const usage = `usage: ah-switch [--root DIR [--allow-exec]] [--dry] [--mode manual|signed] [--noaicheck PATH] [--from INCOMING_DIR] ID
       ah-switch [--root DIR [--allow-exec]] [--dry] [--mode ...] [--noaicheck PATH] --rollback-to previous
       ah-switch [--root DIR] status
       ah-switch [--root DIR [--allow-exec]] [--dry] recover --counter N
`

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ah-switch", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	root := fs.String("root", "/", "the folder that holds opt/authority-host and var/lib/authority-host")
	dry := fs.Bool("dry", false, "print every step and what it would do; run nothing, change nothing")
	allowExec := fs.Bool("allow-exec", false, "with --root other than /: run the release's programs all the same (an isolated test runner only)")
	mode := fs.String("mode", "", "manual (SHA256SUMS only) or signed (also SHA256SUMS.sig); default and minimum: the switch policy's")
	noai := fs.String("noaicheck", "", "the no-AI check program (default: the release's bin/ah-noaicheck)")
	rollback := fs.String("rollback-to", "", "previous: go back to the release previous points at")
	from := fs.String("from", "", "import the release from this incoming folder into releases/ID first")
	counter := fs.Uint64("counter", 0, "recover: the highest counter this host ever switched to")
	bad := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "ah-switch: "+format+"\n", a...)
		fmt.Fprint(stderr, usage)
		return switcher.ExitUsage
	}

	// Flags may come before or after the one argument.
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				fmt.Fprint(stdout, usage)
				fs.SetOutput(stdout)
				fs.PrintDefaults()
				return switcher.ExitOK
			}
			return bad("%v", err)
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	abs := func(p string) (string, error) {
		if p == "" {
			return "", nil
		}
		return filepath.Abs(p)
	}
	rootAbs, err := abs(*root)
	if err != nil || *root == "" {
		return bad("--root %q: %v", *root, err)
	}

	if len(pos) == 1 && pos[0] == "status" && !set["rollback-to"] {
		for _, name := range []string{"dry", "mode", "noaicheck", "from", "allow-exec", "counter"} {
			if set[name] {
				return bad("status takes only --root")
			}
		}
		return switcher.New(switcher.Config{Root: rootAbs, Out: stdout, Err: stderr}).Status()
	}
	if len(pos) == 1 && pos[0] == "recover" && !set["rollback-to"] {
		for _, name := range []string{"mode", "noaicheck", "from"} {
			if set[name] {
				return bad("recover takes only --root, --allow-exec, --dry and --counter")
			}
		}
		if !set["counter"] {
			return bad("recover needs --counter N")
		}
		return switcher.New(switcher.Config{Root: rootAbs, Dry: *dry, AllowExec: *allowExec, Out: stdout, Err: stderr}).Recover(context.Background(), *counter)
	}
	if set["counter"] {
		return bad("--counter is only for recover")
	}

	switch {
	case set["rollback-to"] && len(pos) != 0:
		return bad("--rollback-to takes no release id")
	case set["rollback-to"] && set["from"]:
		return bad("--rollback-to takes no --from")
	case set["from"] && *from == "":
		return bad("--from needs a folder")
	case !set["rollback-to"] && len(pos) != 1:
		return bad("give one release id, --rollback-to previous, or status")
	}
	noaiAbs, err := abs(*noai)
	if err != nil {
		return bad("--noaicheck: %v", err)
	}
	fromAbs, err := abs(*from)
	if err != nil {
		return bad("--from: %v", err)
	}

	s := switcher.New(switcher.Config{
		Root:      rootAbs,
		Dry:       *dry,
		AllowExec: *allowExec,
		Mode:      *mode,
		NoAICheck: noaiAbs,
		From:      fromAbs,
		Out:       stdout,
		Err:       stderr,
	})
	ctx := context.Background()
	if set["rollback-to"] {
		return s.RollBack(ctx, *rollback)
	}
	return s.Switch(ctx, pos[0])
}
