// Command ah-noaicheck is the authority host's no-AI check. It looks,
// read-only, for an AI agent tool or its credentials on the host and prints
// one line per hit, never a file's content or a variable's value:
//
//	ah-noaicheck [--phase pre|post|full] [--root DIR] [--release DIR] [--require-policy] [--nft-listing FILE] [--json]
//	ah-noaicheck ports [--root DIR] [--release DIR] [--require-policy] [--json]
//	ah-noaicheck nft-digest FILE
//
// It exits 0 when clean, 1 on any hit (an incomplete run included) and 2 on
// a usage error or a read error it cannot continue past. A diagnostic
// writes every path and word that came from outside the program through
// the check's bounded field encoder (noaicheck.Field). See
// deploy/authority-host/README.md.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"ataniq/internal/authorityhost/noaicheck"
)

const (
	exitClean = 0
	exitHit   = 1
	exitError = 2
)

// maxListing bounds the firewall listing read from a file or stdin.
const maxListing = 16 << 20

const usage = `usage: ah-noaicheck [--phase pre|post|full] [--root DIR] [--release DIR] [--require-policy] [--nft-listing FILE] [--json]
       ah-noaicheck ports [--root DIR] [--release DIR] [--require-policy] [--json]
       ah-noaicheck nft-digest FILE

The authority host's no-AI check. It reads the host and release. On a live systemd
witness host it also reads effective address grants with systemctl show. It prints one line per hit ("HIT kind path [detail]"), a "NOTE"
line for what is not a hit (a policy that is not enforced, a masked unit, a
deferred source), "RUN complete" or "RUN incomplete",
and a final "ok" or "FAILED n hit(s)"; it never prints a file's content or
a variable's value.

The first form checks for agent binaries, installers and runtimes, agent
credential folders, provider-key variables in environment files, profiles
and units, agent processes, enabled units not in units.allow, listening
TCP ports not in ports.allow, packages forbidden or not in packages.allow
and, when the release pins share/nft.digest, the firewall rules. "ports"
checks only the listening TCP ports. "nft-digest" prints the digest of the
normalised listing in FILE ("-" for stdin), the value of share/nft.digest.

  --phase PHASE       full (the default): the host as it is, every check;
                      pre (before a switch): also the release's own etc/
                      (its units, the variables its caddy files refer
                      to), without the listening ports, the firewall
                      digest and the packages.allow check of installed
                      packages (forbidden packages are checked);
                      post (after a switch): pre's checks with the ports
                      and the digest
  --root DIR          check the tree beneath DIR instead of / (default /)
  --release DIR       the release folder whose share/ holds the lists
                      (default: the folder above this program's bin/)
  --require-policy    a release policy left unset is a hit ("HIT policy"):
                      an empty units.allow, packages.allow or ports.allow,
                      or no share/nft.digest; the switch always passes it
  --nft-listing FILE  the output of "nft list ruleset" ("-" for stdin);
                      not read in the pre phase
  --json              write the report as one JSON object

A list, listing or nft-digest FILE that is not a regular file (a FIFO, a
device, a folder, a link) is not read or waited on: it is "HIT incomplete
input <name>" and the run exits 1.

Exit status: 0 clean, 1 any hit, 2 a usage or read error.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fail := func(err error) int {
		fmt.Fprintf(stderr, "ah-noaicheck: %s\n", diag(err))
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
		if code, ok := inputHit(err, "", false, stdout, stderr); ok {
			return code
		}
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
	var listingFile, phase *string
	if mode == "check" {
		listingFile = fs.String("nft-listing", "", "")
		phase = fs.String("phase", noaicheck.PhaseFull, "")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, usage)
			return exitClean
		}
		return usageErr(noaicheck.Field(err.Error()))
	}
	if fs.NArg() > 0 {
		return usageErr("unexpected argument " + noaicheck.Field(fs.Arg(0)))
	}
	if *root == "" {
		return usageErr("--root is empty")
	}
	if phase != nil {
		switch *phase {
		case noaicheck.PhasePre, noaicheck.PhasePost, noaicheck.PhaseFull:
		default:
			return usageErr("--phase is pre, post or full, not " + noaicheck.Field(*phase))
		}
		if *phase == noaicheck.PhasePre && *listingFile != "" {
			return usageErr("--nft-listing is not read in the pre phase")
		}
	}
	if *release == "" {
		dir, err := ownRelease()
		if err != nil {
			return fail(fmt.Errorf("no release folder: pass --release: %w", err))
		}
		*release = dir
	}
	runPhase := ""
	if phase != nil && mode == "check" {
		runPhase = *phase
	}
	lists, err := noaicheck.LoadLists(*release)
	if code, ok := inputHit(err, runPhase, *asJSON, stdout, stderr); ok {
		return code
	}
	if err != nil {
		return fail(fmt.Errorf("the release's lists: %w", err))
	}
	var listing []byte
	if listingFile != nil && *listingFile != "" {
		listing, err = readListing(*listingFile, stdin)
		if code, ok := inputHit(err, runPhase, *asJSON, stdout, stderr); ok {
			return code
		}
		if err != nil {
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
	} else if report, err = c.RunPhase(*phase, *release, listing); err != nil {
		return fail(fmt.Errorf("the release folder: %w", err))
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

// diag is err for a diagnostic line: the path of a filesystem error through
// noaicheck.Field, and any other text as it is when it is printable ASCII,
// otherwise through noaicheck.Field too, so that nothing from outside the
// program reaches the terminal raw or unbounded.
func diag(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		prefix := strings.TrimSuffix(err.Error(), pe.Error())
		return text(prefix) + pe.Op + " " + noaicheck.Field(pe.Path) + ": " + text(pe.Err.Error())
	}
	var le *os.LinkError
	if errors.As(err, &le) {
		prefix := strings.TrimSuffix(err.Error(), le.Error())
		return text(prefix) + le.Op + " " + noaicheck.Field(le.Old) + " " + noaicheck.Field(le.New) + ": " + text(le.Err.Error())
	}
	return text(err.Error())
}

// text is s when it is printable ASCII of at most noaicheck.MaxField
// bytes, otherwise noaicheck.Field(s).
func text(s string) string {
	if len(s) > noaicheck.MaxField {
		return noaicheck.Field(s)
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] >= 0x7f {
			return noaicheck.Field(s)
		}
	}
	return s
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

// inputHit reports a named input that is not a regular file (err a
// *noaicheck.InputError): the report "HIT incomplete input <name>", in the
// phase the run would have had, and the exit status 1, with ok true. Any
// other err is not its to report: ok false.
func inputHit(err error, phase string, asJSON bool, stdout, stderr io.Writer) (int, bool) {
	var ie *noaicheck.InputError
	if !errors.As(err, &ie) {
		return 0, false
	}
	r := noaicheck.Report{Hits: []noaicheck.Hit{ie.Hit()}, Phase: phase}
	write := r.WriteText
	if asJSON {
		write = r.WriteJSON
	}
	if err := write(stdout); err != nil {
		fmt.Fprintf(stderr, "ah-noaicheck: %s\n", diag(err))
		return exitError, true
	}
	return exitHit, true
}

// readListing reads a firewall listing from path, or from stdin for "-",
// the streaming interface. A named file is opened with noaicheck.OpenInput:
// one that is not a regular file (a FIFO, a device, a folder, a link) is
// never read or waited on but a *noaicheck.InputError. An empty file is an
// empty listing (whose digest differs), not a missing one.
func readListing(path string, stdin io.Reader) ([]byte, error) {
	var r io.Reader = stdin
	if path != "-" {
		f, err := noaicheck.OpenInput(path)
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
