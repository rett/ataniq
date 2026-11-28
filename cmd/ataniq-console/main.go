package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"ataniq/internal/approval/demo"
	"ataniq/internal/leadattach"
	"ataniq/internal/terminalconsole"
	"ataniq/internal/terminalconsole/approvals"
	tea "charm.land/bubbletea/v2"
)

// The sample backend satisfies the console's one view of approvals. The
// console imports neither it nor any other backend: this command hands it one
// as a value in the options, with --demo only.
var _ approvals.Backend = (*demo.Backend)(nil)

// maxLinger bounds --linger.
const maxLinger = time.Hour

// defaultLinger keeps the fixture session open long enough to try the focus
// and zoom keys on it.
const defaultLinger = 30 * time.Second

const usageLine = "Usage: ataniq-console --demo [--theme dark|light] [--return-key ctrl+KEY] [--recording NAME] [--linger DURATION] [--no-bell]\n" +
	"       ataniq-console --attach DIR --ticket-file FILE [--viewer] [--shift-enter esc-cr|lf|csi-u|backslash-cr] [--theme dark|light] [--return-key ctrl+KEY]\n" +
	"       ataniq-console --daemon DIR [--scratch-writer] [--theme dark|light] [--no-bell]\n" +
	"--demo, --attach and --daemon exclude each other."

// clientName is the name the console gives the holder, which shows it to the
// other pane when the writer changes.
const clientName = "ataniq-console"

// attachSocketName is the holder's attach socket in its runtime folder (the
// name is the holder's; this command may not import the service that owns it).
const attachSocketName = "attach.sock"

// maxSocketPath is the longest path a unix socket address holds: sun_path is
// 108 bytes with the terminating NUL.
const maxSocketPath = 107

// shiftEnters are the values of --shift-enter, in the order the help lists
// them. The first is the default.
var shiftEnters = []leadattach.ShiftEnter{
	leadattach.ShiftEnterESCCR, leadattach.ShiftEnterLF, leadattach.ShiftEnterCSIu, leadattach.ShiftEnterBackslashCR,
}

// attachConfig is what --attach selects.
type attachConfig struct {
	// socket is the holder's attach socket, ticketFile the file the single-use
	// tickets are read from.
	socket, ticketFile string
	viewer             bool
	shiftEnter         leadattach.ShiftEnter
	// tickets, when set, is the daemon-backed source of a viewer attach made
	// from the leads view, and the attach pins expectLead and expectEpoch,
	// the lead and epoch of the selected row. With --attach it is nil: the
	// ticket file is the one source.
	tickets     leadattach.TicketSource
	expectLead  string
	expectEpoch uint64
	// writerFallback, when set on a writer attach made from the leads view
	// (--scratch-writer), is where the viewer tickets come from once the writer
	// role is lost: the attach then stays a viewer for good (see
	// leadattach.Config.ReconnectAsViewer) and never asks for a writer ticket
	// again. With --attach it is nil: the ticket file is the one source.
	writerFallback leadattach.TicketSource
}

// config is what the flags select.
type config struct {
	demo bool
	// attach is nil unless --attach was given.
	attach *attachConfig
	// daemon is the folder of the daemon's socket; empty unless --daemon was
	// given.
	daemon string
	// scratchWriter is --daemon's --scratch-writer: the leads view offers the
	// writer attach for scratch leads. Without it the view is viewer-only.
	scratchWriter bool
	options       terminalconsole.Options
}

// parse reads the flags. On failure it has printed the diagnostic and usage to
// errout and returns the exit code to use; ok is true when the program should
// go on.
func parse(args []string, errout io.Writer) (cfg config, code int, ok bool) {
	flags := flag.NewFlagSet("ataniq-console", flag.ContinueOnError)
	// The flag package includes raw argument text in errors. Do not display it.
	flags.SetOutput(io.Discard)
	demo := flags.Bool("demo", false, "show the cockpit with sample cards and a recorded session; a decision on a sample card changes nothing")
	theme := flags.String("theme", "dark", "appearance: dark or light")
	returnKey := flags.String("return-key", "", "key that leaves the session pane: ctrl+ and one letter, or ctrl+\\ or ctrl+] (default ctrl+\\)")
	recording := flags.String("recording", terminalconsole.DefaultRecording, "fixture recording to replay: "+strings.Join(terminalconsole.Recordings(), ", "))
	linger := flags.Duration("linger", defaultLinger, "keep the fixture session open this long after the recording's end, such as 30s; 0 ends it as recorded")
	attach := flags.String("attach", "", "show a live scratch session: the holder's runtime folder, which holds attach.sock (provisional, for a scratch agent only)")
	ticketFile := flags.String("ticket-file", "", "with --attach: a 0600 file the single-use attach ticket is read from each time one is needed")
	viewer := flags.Bool("viewer", false, "with --attach: ask for the read-only place; the ticket decides which place the holder grants")
	shiftEnter := flags.String("shift-enter", shiftEnters[0].String(), "with --attach: what Shift+Enter sends: "+strings.Join(shiftEnterNames(), ", "))
	noBell := flags.Bool("no-bell", false, "with --demo or --daemon: do not ring the terminal's bell when a card arrives while you are away (the bell is on by default)")
	scratchWriter := flags.Bool("scratch-writer", false, "with --daemon: offer the writer attach (key w) for scratch leads in the leads view; without it the leads view is viewer-only and never asks the daemon for a writer ticket (the managed directory never offers a writer)")
	daemon := flags.String("daemon", "", "show the daemon's leads, its approvals when it serves the owner channel, and its lead control (key L) when it has one: the folder that holds the daemon's socket (provisional; not with --demo or --attach)")
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
	// The default is not a flag default: the flag package prints a string default
	// quoted, as "ctrl+\\", which the flag then refuses.
	given := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { given[f.Name] = true })
	if !given["return-key"] {
		*returnKey = terminalconsole.DefaultReturnKey.Spec()
	}
	key, err := terminalconsole.ParseReturnKey(*returnKey)
	if err != nil {
		// The error text does not repeat the argument.
		fmt.Fprintln(errout, "error: --return-key: "+err.Error())
		usage()
		return cfg, 2, false
	}
	usageError := func(msg string) (config, int, bool) {
		fmt.Fprintln(errout, "error: "+msg)
		usage()
		return config{}, 2, false
	}
	if !slices.Contains(terminalconsole.Recordings(), *recording) {
		return usageError("--recording must be one of: " + strings.Join(terminalconsole.Recordings(), ", "))
	}
	if *linger < 0 || *linger > maxLinger {
		return usageError("--linger must be between 0 and 1h")
	}
	if given["scratch-writer"] && !given["daemon"] {
		return usageError("--scratch-writer needs --daemon")
	}
	if given["no-bell"] && !*demo && !given["daemon"] {
		// The bell rings for a card, and only the sample backend of --demo
		// and the daemon's approvals have cards.
		return usageError("--no-bell needs --demo or --daemon")
	}
	cfg = config{demo: *demo, options: terminalconsole.Options{Theme: *theme, ReturnKey: key, Recording: *recording, Linger: *linger, BellOff: *noBell}}
	if given["daemon"] {
		// The daemon's view replaces the fixture and attaches to nothing: no
		// flag of either applies to it. The messages do not repeat the folder.
		switch {
		case *demo || given["recording"] || given["linger"]:
			return usageError("--daemon cannot be used with --demo, --recording or --linger")
		case given["attach"] || given["ticket-file"] || given["viewer"] || given["shift-enter"]:
			return usageError("--daemon cannot be used with --attach, --ticket-file, --viewer or --shift-enter")
		case given["return-key"]:
			// The return key leaves the session pane, and this view has none.
			return usageError("--daemon cannot be used with --return-key: its view has no session focus to leave")
		case *daemon == "":
			return usageError("--daemon needs a path")
		case strings.ContainsRune(*daemon, 0):
			return usageError("--daemon needs a path without a NUL byte")
		}
		cfg.daemon, cfg.scratchWriter = *daemon, *scratchWriter
		return cfg, 0, true
	}
	if !given["attach"] && !given["ticket-file"] {
		if given["viewer"] || given["shift-enter"] {
			return usageError("--viewer and --shift-enter need --attach")
		}
		return cfg, 0, true
	}
	// A live session replaces the fixture: nothing of the fixture's flags
	// applies to it.
	switch {
	case !given["attach"] || !given["ticket-file"]:
		return usageError("--attach and --ticket-file are used together")
	case *demo || given["recording"] || given["linger"]:
		return usageError("--attach cannot be used with --demo, --recording or --linger")
	case *attach == "" || *ticketFile == "":
		return usageError("--attach and --ticket-file need a path")
	case strings.ContainsRune(*attach, 0) || strings.ContainsRune(*ticketFile, 0):
		return usageError("--attach and --ticket-file need a path without a NUL byte")
	}
	socket := filepath.Join(*attach, attachSocketName)
	if len(socket) > maxSocketPath {
		return usageError("--attach: the socket path is " + strconv.Itoa(len(socket)) + " bytes; a unix socket address holds at most " + strconv.Itoa(maxSocketPath))
	}
	ac := attachConfig{socket: socket, ticketFile: *ticketFile, viewer: *viewer}
	i := slices.IndexFunc(shiftEnters, func(e leadattach.ShiftEnter) bool { return e.String() == *shiftEnter })
	if i < 0 {
		return usageError("--shift-enter must be one of: " + strings.Join(shiftEnterNames(), ", "))
	}
	ac.shiftEnter = shiftEnters[i]
	cfg.attach = &ac
	return cfg, 0, true
}

func shiftEnterNames() []string {
	var names []string
	for _, e := range shiftEnters {
		names = append(names, e.String())
	}
	return names
}

// attachModel returns the console for a live session: the cockpit with the
// attach client as its source. The command line and the tests both make it
// here. It is the one place this command names the client; the client owns
// the socket.
func attachModel(cfg config) *terminalconsole.Model {
	a := cfg.attach
	mode := leadattach.ModeWriter
	if a.viewer {
		mode = leadattach.ModeViewer
	}
	o := cfg.options
	if a.tickets != nil {
		o.Live = viewerLive(a, leadattach.Config{
			Socket:     a.socket,
			Name:       clientName,
			ShiftEnter: a.shiftEnter,
		})
		return terminalconsole.NewWithOptions(nil, o)
	}
	o.Live = leadattach.New(fileTicketConfig(a, mode))
	return terminalconsole.NewWithOptions(nil, o)
}

// fileTicketConfig is the attach client's configuration for an attach whose
// ticket comes from a file: --attach, and a writer made from the leads view.
// The presence check keeps the console from dialling the holder while the
// ticket file is empty or refused, and gives the status the reason of a
// refusal. With a writerFallback the writer that loses its role is a viewer
// for good, with viewer tickets from the daemon.
func fileTicketConfig(a *attachConfig, mode leadattach.Mode) leadattach.Config {
	tickets, ready := leadattach.FileTicketSource(a.ticketFile)
	lc := leadattach.Config{
		Socket:      a.socket,
		Mode:        mode,
		Name:        clientName,
		Tickets:     tickets,
		TicketReady: ready,
		ShiftEnter:  a.shiftEnter,
	}
	if a.writerFallback != nil && mode == leadattach.ModeWriter {
		lc.ReconnectAsViewer = true
		lc.ViewerTickets = a.writerFallback
	}
	return lc
}

// viewerLive returns the attach client of a viewer attach with a ticket
// source of its own: it asks a.tickets before each connection, so that a
// viewer that the holder dropped connects again with a new ticket, and it stops
// for the owner to select the lead again when the source says the selection is
// stale. The lead and epoch of the selected row are its pin.
func viewerLive(a *attachConfig, c leadattach.Config) *leadattach.Client {
	c.ExpectLead, c.ExpectEpoch = a.expectLead, a.expectEpoch
	return leadattach.NewViewer(c, a.tickets)
}

// demoModel returns the console for --demo: the fixture cockpit with the
// sample backend as its approvals. A decision on a sample card changes the
// backend's memory and nothing else, and the header says so once the console
// has read the sample cards.
func demoModel(cfg config) *terminalconsole.Model {
	o := cfg.options
	o.Backend = demo.New(time.Now)
	return terminalconsole.NewWithOptions(terminalconsole.Fixture{}, o)
}

// runProgram returns the runner of consoles on the terminal: a Bubble Tea
// program that writes to out. With openLeads it sends the key that opens the
// leads view, which the program takes once it runs.
func runProgram(out io.Writer) consoleRunner {
	return func(m *terminalconsole.Model, openLeads bool) error {
		program := tea.NewProgram(m, tea.WithOutput(out))
		if openLeads {
			// Send returns when the program takes the message or has ended.
			go program.Send(openLeadsKey())
		}
		_, err := program.Run()
		if err != nil {
			program.Kill()
		}
		return err
	}
}

func run(args []string, out, errout io.Writer) int {
	cfg, code, ok := parse(args, errout)
	if !ok {
		return code
	}
	var model *terminalconsole.Model
	switch {
	case cfg.attach != nil:
		model = attachModel(cfg)
	case cfg.daemon != "":
		return runDaemon(cfg, out, errout)
	case cfg.demo:
		model = demoModel(cfg)
	default:
		fmt.Fprintln(errout, "Terminal console backend is unavailable. Use --demo to inspect sample cards; a decision on them changes nothing.")
		return 1
	}
	// Close ends the attach client's connection with a Detach, so that the
	// holder sees a clean detach. Quitting from the console closes it first;
	// a second Close does nothing.
	defer model.Close()
	if err := runProgram(out)(model, false); err != nil {
		fmt.Fprintln(errout, "console:", err)
		return 1
	}
	return 0
}

// runDaemon runs --daemon: the daemon's console, and the attaches the leads
// view asks for, which the loop runs one kind at a time. See daemonLoop.
func runDaemon(cfg config, out, errout io.Writer) int {
	rig, err := newDaemonRig(cfg, newLeadsClient)
	if err != nil {
		fmt.Fprintln(errout, "error:", err)
		return 1
	}
	defer rig.Close()
	run := runProgram(out)
	// A viewer attach asks the daemon for its tickets through the lead client;
	// with no lead control there is no attach to carry out.
	var tickets viewerTicketer
	if rig.leads != nil {
		tickets = rig.leads.c
	}
	return daemonLoop{
		rig:    rig,
		run:    run,
		attach: map[attachKind]attachRunner{attachTerminal: terminalAttach(cfg, run, tickets)},
		errout: errout,
	}.loop()
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
