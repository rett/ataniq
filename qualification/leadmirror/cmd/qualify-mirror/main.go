package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"ataniq/internal/exchange/boundedexec"
	"ataniq/internal/exchange/gitenv"
	"ataniq/qualification/leadmirror"
)

// Exit statuses.
const (
	exitOK      = 0
	exitRefused = 2 // the arguments, the config, the template or the output file are refused
	exitFailed  = 1 // the run could not be completed or the output could not be written
)

// stopContext returns a context that ends on every signal that
// boundedexec.StopSignals lists: interrupt, terminate and hang-up. A sandboxed
// child is in a process group of its own, so a signal to this command does
// not reach it. It is killed, and its scratch folders are removed, only when
// the run returns, and a run returns early only when its context ends.
//
// An interrupt or a hang-up that was ignored when the command started (under
// nohup, for example) stays ignored: the Go runtime keeps an inherited ignore
// for those two signals only, signal.Ignored reports it, and the signal is
// left out here. It neither kills the command nor cancels the run. A
// terminate that was ignored at start is different. The runtime installs its
// own handler for it whatever the command inherited, signal.Ignored does not
// report it, and it cancels the run like any other terminate.
func stopContext() (context.Context, context.CancelFunc) {
	sigs := notIgnored(boundedexec.StopSignals(), signal.Ignored)
	if len(sigs) == 0 {
		// NotifyContext with no signals would catch every signal.
		return context.WithCancel(context.Background())
	}
	return signal.NotifyContext(context.Background(), sigs...)
}

// notIgnored returns the signals of all for which ignored is false.
func notIgnored(all []os.Signal, ignored func(os.Signal) bool) []os.Signal {
	var out []os.Signal
	for _, s := range all {
		if !ignored(s) {
			out = append(out, s)
		}
	}
	return out
}

func main() {
	ctx, stop := stopContext()
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run is main without the process: it returns the exit status.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("qualify-mirror", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configPath := fs.String("config", "", "the JSON config file (required)")
	outPath := fs.String("out", "", "the report file, which must not exist (required)")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *configPath == "" || *outPath == "" {
		fmt.Fprintln(stderr, "usage: qualify-mirror --config FILE --out FILE")
		return exitRefused
	}
	cfg, err := leadmirror.ReadConfig(*configPath)
	if err != nil {
		fmt.Fprintln(stderr, "qualify-mirror: refused:", err)
		return exitRefused
	}
	tmpl, digest, err := leadmirror.LoadTemplate(cfg.Template)
	if err != nil {
		fmt.Fprintln(stderr, "qualify-mirror: refused:", err)
		return exitRefused
	}
	out, err := checkOut(*outPath, cfg.StateRoot)
	if err != nil {
		fmt.Fprintln(stderr, "qualify-mirror: refused:", err)
		return exitRefused
	}
	git, err := gitenv.Find()
	if err != nil {
		fmt.Fprintln(stderr, "qualify-mirror:", err)
		return exitFailed
	}
	rep, err := leadmirror.Run(ctx, leadmirror.Options{
		StateRoot: cfg.StateRoot, Role: cfg.Role, Project: cfg.Project,
		Planner: tmpl, Git: git, TemplateDigest: digest,
	})
	if rep == nil {
		fmt.Fprintln(stderr, "qualify-mirror: refused:", err)
		return exitRefused
	}
	// A run that was interrupted still has the facts it made: write them,
	// then say that the run did not finish.
	if werr := writeReport(out, rep); werr != nil {
		fmt.Fprintln(stderr, "qualify-mirror: writing the report:", werr)
		return exitFailed
	}
	if err != nil {
		fmt.Fprintln(stderr, "qualify-mirror: the run did not finish; the report holds the facts so far:", err)
		return exitFailed
	}
	fmt.Fprintln(stdout, "qualify-mirror: report written; it holds facts only, and a person reads them")
	return exitOK
}

// checkOut checks the output path before anything runs: absolute after
// cleaning, its folder exists, it does not exist, and it is not inside the
// scratch state root, which a sandbox may read. The folder of the output file
// is resolved through its symlinks first, so a link into the state root is
// refused too.
func checkOut(path, stateRoot string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(abs); err == nil {
		return "", errors.New("the output file exists; give a new name")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if fi, err := os.Stat(filepath.Dir(abs)); err != nil || !fi.IsDir() {
		return "", errors.New("the folder of the output file does not exist")
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", err
	}
	real := filepath.Join(dir, filepath.Base(abs))
	for _, root := range rootNames(stateRoot) {
		for _, p := range []string{abs, real} {
			if p == root || strings.HasPrefix(p, root+"/") {
				return "", errors.New("the output file must not be inside the scratch state root")
			}
		}
	}
	return abs, nil
}

// rootNames returns the names of the scratch state root to compare with: the
// text as given, and the root with the symlinks of its parent folders
// resolved when that folder exists. The root itself is not a symlink; the
// library refuses that.
func rootNames(stateRoot string) []string {
	names := []string{stateRoot}
	if dir, err := filepath.EvalSymlinks(filepath.Dir(stateRoot)); err == nil {
		if real := filepath.Join(dir, filepath.Base(stateRoot)); real != stateRoot {
			names = append(names, real)
		}
	}
	return names
}

// writeReport writes the report as indented JSON to a new file with mode
// 0600. It never replaces a file.
func writeReport(path string, rep *leadmirror.Report) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(rep); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(buf.Bytes())
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}
