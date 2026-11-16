package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"ataniq/internal/terminalconsole"
	tea "charm.land/bubbletea/v2"
)

func run(args []string, out, errout io.Writer) int {
	flags := flag.NewFlagSet("ataniq-console", flag.ContinueOnError)
	// The flag package includes raw argument text in errors. Do not display it.
	flags.SetOutput(io.Discard)
	demo := flags.Bool("demo", false, "show the read-only fixture cockpit")
	theme := flags.String("theme", "dark", "appearance: dark or light")
	usage := func() {
		fmt.Fprintln(errout, "Usage: ataniq-console --demo [--theme dark|light]")
		flags.SetOutput(errout)
		flags.PrintDefaults()
		flags.SetOutput(io.Discard)
	}
	// The flag package calls Usage itself on a parse error. Print it here, once.
	flags.Usage = func() {}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			usage()
			return 0
		}
		fmt.Fprintln(errout, "error: invalid flags")
		usage()
		return 2
	}
	if flags.NArg() != 0 || (*theme != "dark" && *theme != "light") {
		fmt.Fprintln(errout, "error: expected --theme dark or light and no positional arguments")
		usage()
		return 2
	}
	if !*demo {
		fmt.Fprintln(errout, "Terminal console backend is unavailable. Use --demo to inspect the read-only fixture.")
		return 1
	}
	program := tea.NewProgram(terminalconsole.New(terminalconsole.Fixture{}, *theme), tea.WithOutput(out))
	_, err := program.Run()
	if err != nil {
		program.Kill()
		fmt.Fprintln(errout, "console:", err)
		return 1
	}
	return 0
}
func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
