// Command ah-noaicheck is the authority host's no-AI check. It looks,
// read-only, for an AI agent tool or its credentials on the host and prints
// one line per hit, never a file's content or a variable's value:
//
//	ah-noaicheck [--root DIR] [--release DIR] [--require-policy] [--nft-listing FILE] [--json]
//	ah-noaicheck ports [--root DIR] [--release DIR] [--require-policy] [--json]
//	ah-noaicheck nft-digest FILE
//
// It exits 0 when clean, 1 on any hit (an incomplete run included) and 2 on
// a usage error or a read error it cannot continue past. See
// deploy/authority-host/README.md.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"ataniq/internal/authorityhost/noaicheck"
)

const (
	exitClean = 0
	exitHit   = 1
	exitError = 2
)

// maxListing bounds the firewall listing read from a file or stdin.
const maxListing = 16 << 20

const usage = `usage: ah-noaicheck [--root DIR] [--release DIR] [--require-policy] [--nft-listing FILE] [--json]
       ah-noaicheck ports [--root DIR] [--release DIR] [--require-policy] [--json]
       ah-noaicheck nft-digest FILE

The authority host's no-AI check. It reads only: it writes nothing and runs
no program. It prints one line per hit ("HIT kind path [detail]"), a "NOTE"
line for a policy that is not enforced, "RUN complete" or "RUN incomplete",
and a final "ok" or "FAILED n hit(s)"; it never prints a file's content or
a variable's value.

The first form checks for agent binaries, installers and runtimes, agent
credential folders, provider-key variables in environment files, profiles
and units, agent processes, enabled units not in units.allow, listening
TCP ports not in ports.allow, packages forbidden or not in packages.allow
and, when the release pins share/nft.digest, the firewall rules. "ports"
checks only the listening TCP ports. "nft-digest" prints the digest of the
normalised listing in FILE ("-" for stdin), the value of share/nft.digest.

  --root DIR          check the tree beneath DIR instead of / (default /)
  --release DIR       the release folder whose share/ holds the lists
                      (default: the folder above this program's bin/)
  --require-policy    a release policy left unset is a hit ("HIT policy"):
                      an empty units.allow, packages.allow or ports.allow,
                      or no share/nft.digest; the switch always passes it
  --nft-listing FILE  the output of "nft list ruleset" ("-" for stdin)
  --json              write the report as one JSON object

Exit status: 0 clean, 1 any hit, 2 a usage or read error.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fail := func(err error) int {
		fmt.Fprintf(stderr, "ah-noaicheck: %v\n", err)
		return exitError
	}
	usageErr := func(msg string) int {
		fmt.Fprintf(stderr, "ah-noaicheck: %s\n%s", msg, usage)
		return exitError
	}
	mode := "check"
	if len(args) > 0 && (args[0] == "ports" || args[0] == "nft-digest") {
		mode, args = args[0], args[1:]
	}
	if mode == "nft-digest" {
		if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
			fmt.Fprint(stdout, usage)
			return exitClean
		}
		if len(args) != 1 {
			return usageErr("nft-digest takes one FILE")
		}
		listing, err := readListing(args[0], stdin)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintln(stdout, noaicheck.NFTDigest(listing))
		return exitClean
	}

	fs := flag.NewFlagSet("ah-noaicheck", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	root := fs.String("root", "/", "")
	release := fs.String("release", "", "")
	asJSON := fs.Bool("json", false, "")
	requirePolicy := fs.Bool("require-policy", false, "")
	var listingFile *string
	if mode == "check" {
		listingFile = fs.String("nft-listing", "", "")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, usage)
			return exitClean
		}
		return usageErr(err.Error())
	}
	if fs.NArg() > 0 {
		return usageErr("unexpected argument " + fmt.Sprintf("%q", fs.Arg(0)))
	}
	if *root == "" {
		return usageErr("--root is empty")
	}
	if *release == "" {
		dir, err := ownRelease()
		if err != nil {
			return fail(fmt.Errorf("no release folder: pass --release: %w", err))
		}
		*release = dir
	}
	lists, err := noaicheck.LoadLists(*release)
	if err != nil {
		return fail(fmt.Errorf("the release's lists: %w", err))
	}
	var listing []byte
	if listingFile != nil && *listingFile != "" {
		if listing, err = readListing(*listingFile, stdin); err != nil {
			return fail(err)
		}
	}
	c, err := noaicheck.Open(*root, lists)
	if err != nil {
		return fail(err)
	}
	defer c.Close()
	c.RequirePolicy = *requirePolicy
	var report noaicheck.Report
	if mode == "ports" {
		c.CheckPorts()
		report = c.Report()
	} else {
		report = c.RunAll(listing)
	}
	write := report.WriteText
	if *asJSON {
		write = report.WriteJSON
	}
	if err := write(stdout); err != nil {
		return fail(err)
	}
	if len(report.Hits) > 0 {
		return exitHit
	}
	return exitClean
}

// ownRelease is the release folder this program was started from: the
// folder above the one holding the executable (releases/<id>/bin/).
func ownRelease() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", err
	}
	return filepath.Dir(filepath.Dir(exe)), nil
}

// readListing reads a firewall listing from path, or from stdin for "-". An
// empty file is an empty listing (whose digest differs), not a missing one.
func readListing(path string, stdin io.Reader) ([]byte, error) {
	var r io.Reader = stdin
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		r = f
	}
	data, err := io.ReadAll(io.LimitReader(r, maxListing+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxListing {
		return nil, fmt.Errorf("the nft listing is larger than %d bytes", maxListing)
	}
	if data == nil {
		data = []byte{} // nil means no listing to the check
	}
	return data, nil
}
