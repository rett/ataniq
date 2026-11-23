// Command ah-release makes the authority host's release folder, the one
// ah-switch imports, and checks a built one. See
// deploy/authority-host/README.md, "The release builder".
//
//	ah-release build --out DIR --counter N --commit SHA --go-version V --built-on CLASS --package-snapshot S --bin FILE... --src deploy/authority-host [--components static]
//	ah-release check [--no-noaicheck] [--root DIR] [--require-policy] DIR/<id>
//	ah-release baseline --src DIR --units FILE --ports FILE --packages FILE --nft FILE [--out DIR] [--stamp TEXT] [--require-default-deny]
//
// Exit codes: 0 ok, 1 refused or check failed (FAIL lines), 2 usage.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"ataniq/internal/authorityhost/release"
	"ataniq/internal/authorityhost/switcher"
)

const (
	exitOK     = 0
	exitFailed = 1
	exitUsage  = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

const usage = `usage: ah-release build --out DIR --counter N --commit SHA --go-version V --built-on CLASS --package-snapshot S --bin FILE... --src DIR [--components static]
       ah-release check [--no-noaicheck] [--root DIR] [--require-policy] DIR/ID
       ah-release baseline --src DIR --units FILE --ports FILE --packages FILE --nft FILE [--out DIR] [--stamp TEXT] [--require-default-deny]
`

func run(args []string, stdout, stderr io.Writer) int {
	bad := func(msg string) int {
		fmt.Fprintf(stderr, "ah-release: %s\n%s", msg, usage)
		return exitUsage
	}
	if len(args) == 0 {
		return bad("give a subcommand: build, check or baseline")
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return exitOK
	case "build":
		return build(args[1:], stdout, stderr, bad)
	case "check":
		return check(args[1:], stdout, stderr, bad)
	case "baseline":
		return baseline(args[1:], stdout, stderr, bad)
	}
	return bad("unknown subcommand " + switcher.QuoteField(args[0]))
}

// listFlag is a flag that may be given more than once.
type listFlag []string

func (l *listFlag) String() string     { return "" }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

func build(args []string, stdout, stderr io.Writer, bad func(string) int) int {
	fs := flag.NewFlagSet("ah-release build", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	out := fs.String("out", "", "the folder the release folder is made in")
	counter := fs.String("counter", "", "the release counter, 1 or more")
	commit := fs.String("commit", "", "the full commit, lowercase hex")
	goVersion := fs.String("go-version", "", "the Go version the binaries were built with")
	builtOn := fs.String("built-on", "", "the class of the build host, never its name")
	snapshot := fs.String("package-snapshot", "", "the package snapshot the release expects")
	src := fs.String("src", "", "the deployment source folder, deploy/authority-host")
	components := fs.String("components", "", "static: a release with no decision service")
	var bins listFlag
	fs.Var(&bins, "bin", "a binary to ship as bin/<name>; repeat for each")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, usage)
			return exitOK
		}
		return bad(switcher.EscapeLine(err.Error()))
	}
	if fs.NArg() > 0 {
		return bad("build takes no argument, only flags")
	}
	n, err := strconv.ParseUint(*counter, 10, 64)
	if err != nil {
		n = 0 // refused as counter by Build
	}
	id, files, err := release.Build(release.Options{
		Out: *out, Counter: n, Commit: *commit,
		GoVersion: *goVersion, BuiltOn: *builtOn, PackageSnapshot: *snapshot,
		Bins: bins, Src: *src, Components: *components,
	})
	if err != nil {
		var r *release.Refusal
		if !errors.As(err, &r) {
			fmt.Fprintln(stdout, release.Finding{Code: release.CodeIO})
			return exitFailed
		}
		if r.Usage {
			return bad(r.Finding.String()[len("FAIL: "):])
		}
		fmt.Fprintln(stdout, r.Finding)
		return exitFailed
	}
	fmt.Fprintf(stdout, "built release %s (%d files)\n", id, files)
	return exitOK
}

// maxLines is how many FAIL lines check prints.
const maxLines = 50

func check(args []string, stdout, stderr io.Writer, bad func(string) int) int {
	fs := flag.NewFlagSet("ah-release check", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	noRun := fs.Bool("no-noaicheck", false, "do not run the shipped no-AI check (for tests)")
	root := fs.String("root", "", "the --root the no-AI check is given (default: the host it runs on)")
	policy := fs.Bool("require-policy", false, "give the no-AI check --require-policy: the release must carry its allowlists and nft.digest")
	// Flags may come before or after the one folder.
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				fmt.Fprint(stdout, usage)
				return exitOK
			}
			return bad(switcher.EscapeLine(err.Error()))
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if len(pos) != 1 {
		return bad("check takes one release folder")
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if set["root"] && *root == "" {
		return bad("--root needs a folder")
	}
	if *noRun && (set["root"] || set["require-policy"]) {
		return bad("--root and --require-policy are for the no-AI check, which --no-noaicheck skips")
	}
	findings := release.Check(pos[0], release.CheckOptions{NoNoAICheck: *noRun, Root: *root, RequirePolicy: *policy})
	if len(findings) == 0 {
		fmt.Fprintf(stdout, "ok: release %s\n", filepath.Base(filepath.Clean(pos[0])))
		return exitOK
	}
	for i, f := range findings {
		if i == maxLines-1 && len(findings) > maxLines {
			fmt.Fprintf(stdout, "FAIL: more-findings %d more not shown\n", len(findings)-i)
			break
		}
		fmt.Fprintln(stdout, f)
	}
	return exitFailed
}

func baseline(args []string, stdout, stderr io.Writer, bad func(string) int) int {
	fs := flag.NewFlagSet("ah-release baseline", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	src := fs.String("src", "", "the deployment source folder, deploy/authority-host")
	units := fs.String("units", "", "the file of unit names, one per line")
	ports := fs.String("ports", "", "the file of listening TCP ports, one per line")
	packages := fs.String("packages", "", "the output of pacman -Qq")
	nft := fs.String("nft", "", "the output of nft list ruleset")
	out := fs.String("out", "", "the folder made (default: a new folder of the temporary folder)")
	stamp := fs.String("stamp", "", "a text, a date, the headers of the filled files carry")
	strict := fs.Bool("require-default-deny", false, "refuse an nft listing without a policy drop base chain of hook input, output and forward")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, usage)
			return exitOK
		}
		return bad(switcher.EscapeLine(err.Error()))
	}
	if fs.NArg() > 0 {
		return bad("baseline takes no argument, only flags")
	}
	res, err := release.Baseline(release.BaselineOptions{
		Src: *src, Units: *units, Ports: *ports, Packages: *packages, NFT: *nft,
		Out: *out, Stamp: *stamp, RequireDefaultDeny: *strict,
	})
	// A ruleset without a default deny is enrolled, loudly, unless
	// --require-default-deny refuses it; the same words are in nft.digest.
	for _, hook := range res.MissingDefaultDeny {
		fmt.Fprintln(stdout, release.DefaultDenyWarning(hook))
	}
	if err != nil {
		var r *release.Refusal
		if !errors.As(err, &r) {
			fmt.Fprintln(stdout, release.Finding{Code: release.CodeIO})
			return exitFailed
		}
		if r.Usage {
			return bad(r.Finding.String()[len("FAIL: "):])
		}
		fmt.Fprintln(stdout, r.Finding)
		return exitFailed
	}
	fmt.Fprintf(stdout, "filled baseline in %s\n", switcher.QuoteField(res.Dir))
	return exitOK
}
