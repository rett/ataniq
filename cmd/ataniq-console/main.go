package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"ataniq/internal/terminalconsole"
	tea "charm.land/bubbletea/v2"
)

// maxLinger bounds --linger.
const maxLinger = time.Hour

// defaultLinger keeps the fixture session open long enough to try the focus
// and zoom keys on it.
const defaultLinger = 30 * time.Second

const usageLine = "Usage: ataniq-console --demo [--theme dark|light] [--return-key ctrl+KEY] [--recording NAME] [--linger DURATION]"

// config is what the flags select.
type config struct {
	demo    bool
	options terminalconsole.Options
}

// parse reads the flags. On failure it has printed the diagnostic and usage to
// errout and returns the exit code to use; ok is true when the program should
// go on.
func parse(args []string, errout io.Writer) (cfg config, code int, ok bool) {
	flags := flag.NewFlagSet("ataniq-console", flag.ContinueOnError)
	// The flag package includes raw argument text in errors. Do not display it.
	flags.SetOutput(io.Discard)
	demo := flags.Bool("demo", false, "show the read-only fixture cockpit")
	theme := flags.String("theme", "dark", "appearance: dark or light")
	returnKey := flags.String("return-key", terminalconsole.DefaultReturnKey.Spec(), "key that leaves the session pane: ctrl+ and one letter, or ctrl+\\ or ctrl+]")
	recording := flags.String("recording", terminalconsole.DefaultRecording, "fixture recording to replay: "+strings.Join(terminalconsole.Recordings(), ", "))
	linger := flags.Duration("linger", defaultLinger, "keep the fixture session open this long after the recording's last output, such as 30s; 0 ends it as recorded")
	usage := func() {
		fmt.Fprintln(errout, usageLine)
		flags.SetOutput(errout)
		flags.PrintDefaults()
		flags.SetOutput(io.Discard)
	}
	// The flag package calls Usage itself on a parse error. Print it here, once.
	flags.Usage = func() {}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			usage()
			return cfg, 0, false
		}
		fmt.Fprintln(errout, "error: invalid flags")
		usage()
		return cfg, 2, false
	}
	if flags.NArg() != 0 || (*theme != "dark" && *theme != "light") {
		fmt.Fprintln(errout, "error: expected --theme dark or light and no positional arguments")
		usage()
		return cfg, 2, false
	}
	key, err := terminalconsole.ParseReturnKey(*returnKey)
	if err != nil {
		// The error text does not repeat the argument.
		fmt.Fprintln(errout, "error: --return-key: "+err.Error())
		usage()
		return cfg, 2, false
	}
	if !slices.Contains(terminalconsole.Recordings(), *recording) {
		fmt.Fprintln(errout, "error: --recording must be one of: "+strings.Join(terminalconsole.Recordings(), ", "))
		usage()
		return cfg, 2, false
	}
	if *linger < 0 || *linger > maxLinger {
		fmt.Fprintln(errout, "error: --linger must be between 0 and 1h")
		usage()
		return cfg, 2, false
	}
	return config{demo: *demo, options: terminalconsole.Options{Theme: *theme, ReturnKey: key, Recording: *recording, Linger: *linger}}, 0, true
}

func run(args []string, out, errout io.Writer) int {
	cfg, code, ok := parse(args, errout)
	if !ok {
		return code
	}
	if !cfg.demo {
		fmt.Fprintln(errout, "Terminal console backend is unavailable. Use --demo to inspect the read-only fixture.")
		return 1
	}
	model := terminalconsole.NewWithOptions(terminalconsole.Fixture{}, cfg.options)
	defer model.Close()
	program := tea.NewProgram(model, tea.WithOutput(out))
	_, err := program.Run()
	if err != nil {
		program.Kill()
		fmt.Fprintln(errout, "console:", err)
		return 1
	}
	return 0
}
func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
