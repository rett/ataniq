// Command ah-release makes the authority host's release folder, the one
// ah-switch imports, and checks a built one. See
// deploy/authority-host/README.md, "The release builder".
//
//	ah-release build --out DIR --counter N --commit SHA --go-version V --built-on CLASS --package-snapshot S --bin FILE... --src deploy/authority-host [--components static]
//
// Exit codes: 0 ok, 1 refused (FAIL lines), 2 usage.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
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
`

func run(args []string, stdout, stderr io.Writer) int {
	bad := func(msg string) int {
		fmt.Fprintf(stderr, "ah-release: %s\n%s", msg, usage)
		return exitUsage
	}
	if len(args) == 0 {
		return bad("give a subcommand: build")
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return exitOK
	case "build":
		return build(args[1:], stdout, stderr, bad)
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
