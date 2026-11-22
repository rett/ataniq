// Command ah-switch is the one switch of the authority host: it activates a
// release folder under /opt/authority-host/releases, rolls back to the previous
// one, or shows the state. See deploy/authority-host/README.md.
//
//	ah-switch [--root DIR [--allow-exec]] [--dry] [--mode manual|signed] [--noaicheck PATH] [--from INCOMING_DIR] ID
//	ah-switch [--root DIR [--allow-exec]] [--dry] [--mode manual|signed] [--noaicheck PATH] --rollback-to previous
//	ah-switch [--root DIR] status
//	ah-switch [--root DIR [--allow-exec]] [--dry] recover --counter N
//	ah-switch [--root DIR [--allow-exec]] [--dry] recover --package-state SNAPSHOT
//	ah-switch [--root DIR [--allow-exec]] [--dry] recover --services-stopped
//	ah-switch [--root DIR [--allow-exec]] [--dry] recover --discard-journal TXN
//	ah-switch [--root DIR] [--dry] inhibit-clear --asked WORDS
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
       ah-switch [--root DIR [--allow-exec]] [--dry] recover --package-state SNAPSHOT
       ah-switch [--root DIR [--allow-exec]] [--dry] recover --services-stopped
       ah-switch [--root DIR [--allow-exec]] [--dry] recover --discard-journal TXN
       ah-switch [--root DIR] [--dry] inhibit-clear --asked WORDS
`

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ah-switch", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	root := fs.String("root", "/", "the folder that holds opt/authority-host and var/lib/authority-host")
	dry := fs.Bool("dry", false, "print every step and what it would do; run nothing, change nothing")
	allowExec := fs.Bool("allow-exec", false, "with --root other than /: run the release's programs all the same (an isolated test runner only)")
	mode := fs.String("mode", "", "manual (SHA256SUMS only) or signed (also SHA256SUMS.sig); default and minimum: the switch policy's")
	noai := fs.String("noaicheck", "", "the no-AI check program instead of the release's bin/ah-noaicheck: an absolute path on the host (below --root), root-owned and protected, bound by its SHA-256")
	rollback := fs.String("rollback-to", "", "previous: go back to the release previous points at")
	from := fs.String("from", "", "import the release from this incoming folder into releases/ID first")
	counter := fs.Uint64("counter", 0, "recover: the highest counter this host ever switched to")
	stopped := fs.Bool("services-stopped", false, "recover: the decision services are stopped (settles a switch whose stop commands failed)")
	pkgState := fs.String("package-state", "", "recover: the package snapshot the host is at, after a package step that did not finish")
	discard := fs.String("discard-journal", "", "recover: set aside the journal of this transaction (as status shows it), which does not match the host")
	asked := fs.String("asked", "", "inhibit-clear: what the owner asked or saw before removing the decision inhibit (recorded with the time)")
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
			// The flag package's message repeats what was typed.
			return bad("%s", switcher.EscapeLine(err.Error()))
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
	if *root == "" {
		return bad("--root needs a folder")
	}
	if err != nil {
		return bad("--root %s: %s", switcher.QuoteField(*root), switcher.EscapeLine(err.Error()))
	}

	if len(pos) == 1 && pos[0] == "status" && !set["rollback-to"] {
		for _, name := range []string{"dry", "mode", "noaicheck", "from", "allow-exec", "counter", "services-stopped", "package-state", "discard-journal", "asked"} {
			if set[name] {
				return bad("status takes only --root")
			}
		}
		return switcher.New(switcher.Config{Root: rootAbs, Out: stdout, Err: stderr}).Status()
	}
	if len(pos) == 1 && pos[0] == "inhibit-clear" && !set["rollback-to"] {
		for _, name := range []string{"mode", "noaicheck", "from", "allow-exec", "counter", "services-stopped", "package-state", "discard-journal"} {
			if set[name] {
				return bad("inhibit-clear takes only --root, --dry and --asked WORDS")
			}
		}
		if !set["asked"] {
			return bad("inhibit-clear needs --asked WORDS: what the owner asked or saw")
		}
		return switcher.New(switcher.Config{Root: rootAbs, Dry: *dry, Out: stdout, Err: stderr}).InhibitClear(context.Background(), *asked)
	}
	if set["asked"] {
		return bad("--asked is only for inhibit-clear")
	}
	if len(pos) == 1 && pos[0] == "recover" && !set["rollback-to"] {
		for _, name := range []string{"mode", "noaicheck", "from"} {
			if set[name] {
				return bad("recover takes only --root, --allow-exec, --dry and one of --counter N, --package-state SNAPSHOT, --services-stopped and --discard-journal TXN")
			}
		}
		forms := 0
		for _, name := range []string{"counter", "services-stopped", "package-state", "discard-journal"} {
			if set[name] {
				forms++
			}
		}
		if forms != 1 {
			return bad("recover needs one of --counter N, --package-state SNAPSHOT, --services-stopped and --discard-journal TXN")
		}
		if set["discard-journal"] && *discard == "" {
			return bad("--discard-journal needs the id of a transaction")
		}
		o := switcher.RecoverOptions{
			Counter: *counter, SetCounter: set["counter"],
			ServicesStopped: set["services-stopped"] && *stopped,
			PackageState:    *pkgState, SetPackageState: set["package-state"],
			DiscardJournal: *discard,
		}
		if set["services-stopped"] && !*stopped {
			return bad("--services-stopped takes no value")
		}
		return switcher.New(switcher.Config{Root: rootAbs, Dry: *dry, AllowExec: *allowExec, Out: stdout, Err: stderr}).Recover(context.Background(), o)
	}
	if set["counter"] || set["services-stopped"] || set["package-state"] || set["discard-journal"] {
		return bad("--counter, --package-state, --services-stopped and --discard-journal are only for recover")
	}

	switch {
	case set["rollback-to"] && len(pos) != 0:
		return bad("--rollback-to takes no release id")
	case set["rollback-to"] && set["from"]:
		return bad("--rollback-to takes no --from")
	case set["from"] && *from == "":
		return bad("--from needs a folder")
	case !set["rollback-to"] && len(pos) != 1:
		return bad("give one release id, --rollback-to previous, status, recover or inhibit-clear")
	}
	// A path on the host, below --root: never resolved against the
	// working folder.
	if *noai != "" && !filepath.IsAbs(*noai) {
		return bad("--noaicheck %s is not an absolute path", switcher.QuoteField(*noai))
	}
	fromAbs, err := abs(*from)
	if err != nil {
		return bad("--from %s: %s", switcher.QuoteField(*from), switcher.EscapeLine(err.Error()))
	}

	s := switcher.New(switcher.Config{
		Root:      rootAbs,
		Dry:       *dry,
		AllowExec: *allowExec,
		Mode:      *mode,
		NoAICheck: *noai,
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
