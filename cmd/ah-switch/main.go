// Command ah-switch is the one switch of the authority host: it activates a
// release folder under /opt/authority-host/releases, rolls back to the previous
// one, or shows the state. See deploy/authority-host/README.md.
//
//	ah-switch [--root DIR] [--dry] [--mode manual|signed] [--allowed-signers FILE] [--noaicheck PATH] ID
//	ah-switch [--root DIR] [--dry] [--noaicheck PATH] --rollback-to previous
//	ah-switch [--root DIR] status
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

const usage = `usage: ah-switch [--root DIR] [--dry] [--mode manual|signed] [--allowed-signers FILE] [--noaicheck PATH] ID
       ah-switch [--root DIR] [--dry] [--mode ...] [--noaicheck PATH] --rollback-to previous
       ah-switch [--root DIR] status
`

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ah-switch", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	root := fs.String("root", "/", "the folder that holds opt/authority-host and var/lib/authority-host")
	dry := fs.Bool("dry", false, "print every step and what it would do; run nothing, change nothing")
	mode := fs.String("mode", switcher.ModeManual, "manual (SHA256SUMS only) or signed (also SHA256SUMS.sig)")
	signers := fs.String("allowed-signers", "", "the ssh allowed signers file, for --mode signed")
	noai := fs.String("noaicheck", "", "the no-AI check program (default: the release's bin/ah-noaicheck)")
	rollback := fs.String("rollback-to", "", "previous: go back to the release previous points at")
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
		for _, name := range []string{"dry", "mode", "allowed-signers", "noaicheck"} {
			if set[name] {
				return bad("status takes only --root")
			}
		}
		if err := switcher.Status(rootAbs, stdout); err != nil {
			fmt.Fprintf(stderr, "ah-switch: status: %v\n", err)
			return switcher.ExitFailed
		}
		return switcher.ExitOK
	}

	switch {
	case set["rollback-to"] && len(pos) != 0:
		return bad("--rollback-to takes no release id")
	case !set["rollback-to"] && len(pos) != 1:
		return bad("give one release id, --rollback-to previous, or status")
	}
	signersAbs, err := abs(*signers)
	if err != nil {
		return bad("--allowed-signers: %v", err)
	}
	noaiAbs, err := abs(*noai)
	if err != nil {
		return bad("--noaicheck: %v", err)
	}

	s := switcher.New(switcher.Config{
		Root:           rootAbs,
		Dry:            *dry,
		Mode:           *mode,
		AllowedSigners: signersAbs,
		NoAICheck:      noaiAbs,
		Out:            stdout,
		Err:            stderr,
	})
	ctx := context.Background()
	if set["rollback-to"] {
		return s.RollBack(ctx, *rollback)
	}
	return s.Switch(ctx, pos[0])
}
