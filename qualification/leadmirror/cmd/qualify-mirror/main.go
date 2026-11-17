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

	"ataniq/internal/exchange/gitenv"
	"ataniq/qualification/leadmirror"
)

// Exit statuses.
const (
	exitOK      = 0
	exitRefused = 2 // the arguments, the config, the template or the output file are refused
	exitFailed  = 1 // the run could not be completed or the output could not be written
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
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
	if err != nil {
		fmt.Fprintln(stderr, "qualify-mirror: the run did not finish:", err)
		return exitFailed
	}
	if err := writeReport(out, rep); err != nil {
		fmt.Fprintln(stderr, "qualify-mirror: writing the report:", err)
		return exitFailed
	}
	fmt.Fprintln(stdout, "qualify-mirror: report written; it holds facts only, and a person reads them")
	return exitOK
}

// checkOut checks the output path before anything runs: absolute after
// cleaning, its folder exists, it does not exist, and it is not inside the
// scratch state root, which a sandbox may read.
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
	if abs == stateRoot || strings.HasPrefix(abs, stateRoot+"/") {
		return "", errors.New("the output file must not be inside the scratch state root")
	}
	return abs, nil
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
