package main

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"ataniq/internal/consoleclient"
	"ataniq/internal/terminalconsole"
)

func TestDaemonFlagSelectsTheDaemonView(t *testing.T) {
	var errout bytes.Buffer
	cfg, code, ok := parse([]string{"--daemon", "/run/daemon"}, &errout)
	if !ok || code != 0 || errout.Len() != 0 || cfg.daemon != "/run/daemon" || cfg.demo || cfg.attach != nil {
		t.Fatal(ok, code, errout.String(), cfg)
	}
	if cfg.options.Theme != "dark" {
		t.Fatalf("%+v", cfg.options)
	}
	// The folder is passed on as it was given, and the theme applies.
	cfg, code, ok = parse([]string{"--theme", "light", "--daemon", "relative/folder/"}, &errout)
	if !ok || code != 0 || cfg.daemon != "relative/folder/" || cfg.options.Theme != "light" {
		t.Fatal(ok, code, errout.String(), cfg)
	}
	// Without --daemon there is no daemon's view.
	cfg, _, ok = parse([]string{"--demo"}, &errout)
	if !ok || cfg.daemon != "" {
		t.Fatal(ok, cfg)
	}
	if errout.Len() != 0 {
		t.Fatal(errout.String())
	}
}

func TestDaemonRefusals(t *testing.T) {
	notFixture := "--daemon cannot be used with --demo, --recording or --linger"
	notAttach := "--daemon cannot be used with --attach, --ticket-file, --viewer or --shift-enter"
	notReturnKey := "--daemon cannot be used with --return-key: its view has no session focus to leave"
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--daemon", "d", "--demo"}, notFixture},
		{[]string{"--demo", "--daemon", "d"}, notFixture},
		{[]string{"--daemon", "d", "--recording", "title.jsonl"}, notFixture},
		{[]string{"--daemon", "d", "--linger", "0"}, notFixture},
		{[]string{"--daemon", "d", "--attach", "h", "--ticket-file", "t"}, notAttach},
		{[]string{"--attach", "h", "--ticket-file", "t", "--daemon", "d"}, notAttach},
		{[]string{"--daemon", "d", "--attach", "h"}, notAttach},
		{[]string{"--daemon", "d", "--ticket-file", "t"}, notAttach},
		{[]string{"--daemon", "d", "--viewer"}, notAttach},
		{[]string{"--daemon", "d", "--shift-enter", "lf"}, notAttach},
		// --demo is named first when both other modes are given.
		{[]string{"--daemon", "d", "--demo", "--attach", "h", "--ticket-file", "t"}, notFixture},
		{[]string{"--daemon", ""}, "--daemon needs a path"},
		{[]string{"--daemon", "d\x00e"}, "--daemon needs a path without a NUL byte"},
		{[]string{"--daemon"}, "invalid flags"},
		{[]string{"--daemon", "d", "extra"}, "positional"},
		{[]string{"--daemon", "d", "--theme", "blue"}, "dark or light"},
		// A return key that is not valid is refused as such, before the mode.
		{[]string{"--daemon", "d", "--return-key", "ctrl+c"}, "--return-key: "},
		{[]string{"--daemon", "d", "--return-key", "ctrl+g"}, notReturnKey},
		{[]string{"--return-key", "ctrl+]", "--daemon", "d"}, notReturnKey},
		// The other modes are named before the return key.
		{[]string{"--daemon", "d", "--demo", "--return-key", "ctrl+g"}, notFixture},
		{[]string{"--daemon", "d", "--viewer", "--return-key", "ctrl+g"}, notAttach},
	} {
		var out, err bytes.Buffer
		if got := run(tc.args, &out, &err); got != 2 || !strings.Contains(err.String(), tc.want) || strings.Count(err.String(), "Usage:") != 1 {
			t.Fatalf("%q: %d %s", tc.args, got, err.String())
		}
		if out.Len() != 0 {
			t.Fatalf("%q: wrote %q to the terminal", tc.args, out.String())
		}
	}
}

func TestDaemonDiagnosticsDoNotEchoArguments(t *testing.T) {
	secret := "\x1b]52;c;payload\a"
	long := "/" + strings.Repeat("d", 200) + secret
	for _, args := range [][]string{
		{"--daemon", long, "--demo"},
		{"--daemon", secret, "--attach", secret, "--ticket-file", secret},
		{"--daemon", secret, "--viewer"},
		{"--daemon", secret, "--linger", secret},
		{"--daemon", long, "--return-key", "ctrl+g"},
		{"--daemon", secret, "--return-key", "ctrl+" + secret},
		{"--daemon", long + "\x00"},
		{"--daemon", secret, long},
		{"--daemon" + secret},
		{"--daemon", secret, "--bad" + secret},
	} {
		var out, err bytes.Buffer
		if code := run(args, &out, &err); code != 2 {
			t.Fatalf("%q: %d", args, code)
		}
		if strings.ContainsAny(err.String(), "\x1b\a") || strings.Contains(err.String(), "payload") || strings.Contains(err.String(), "ddddd") {
			t.Fatalf("%q: %s", args, err.String())
		}
	}
}

func TestHelpDescribesTheDaemonFlag(t *testing.T) {
	var out, err bytes.Buffer
	if code := run([]string{"--help"}, &out, &err); code != 0 {
		t.Fatal(code)
	}
	for _, want := range []string{
		"ataniq-console --daemon DIR [--theme dark|light]",
		"--demo, --attach and --daemon exclude each other.",
		"-daemon", "the folder that holds the daemon's socket", "read-only", "provisional", "not with --demo or --attach",
	} {
		if !strings.Contains(err.String(), want) {
			t.Fatalf("the help does not say %q:\n%s", want, err.String())
		}
	}
}

// sampleView is a view as consoleclient returns one, with neutral names.
func sampleView() consoleclient.View {
	started := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	return consoleclient.View{
		Daemon:        consoleclient.Daemon{Version: "v9", PID: 4242, StartedAt: started, ReconcileCompleted: true},
		HasSupervisor: true,
		Supervisor:    consoleclient.Entry{Kind: consoleclient.KindSupervisor, Name: "supervisor", State: consoleclient.EntryReady},
		Projects: []consoleclient.Project{
			{Name: "alpha", HasLead: true,
				Lead: consoleclient.Entry{
					Kind: consoleclient.KindLead, Name: "alpha/lead", Project: "alpha", State: consoleclient.EntryDegraded,
					Desired: "running", Reason: "probe-failed", Detail: "no answer to the probe", Since: started.Add(time.Hour),
					Epoch: 7, Manager: "manager-a", Provider: "provider-a", Model: "model-a", Effort: "effort-a",
				},
				Coordinators: []consoleclient.Entry{
					{Kind: consoleclient.KindCoordinator, Name: "alpha/coordinator/one", Project: "alpha", Feature: "feature-one", State: consoleclient.EntryStalled},
					{Kind: consoleclient.KindCoordinator, Name: "alpha/coordinator/two", Project: "alpha", Feature: "feature-two", State: consoleclient.EntryReady},
				}},
			{Name: "beta", Coordinators: []consoleclient.Entry{
				{Kind: consoleclient.KindCoordinator, Name: "beta/coordinator/one", Project: "beta", State: consoleclient.EntryUnknown},
			}},
		},
		LeftOut: 3,
	}
}

func TestDaemonProjectionMapsAView(t *testing.T) {
	p := daemonProjection(consoleclient.Result{State: consoleclient.StateOK, View: sampleView()})
	if p.Problem != "" || p.NextStep != "" {
		t.Fatal(p.Problem, p.NextStep)
	}
	facts := map[string]string{}
	for _, f := range p.Facts {
		facts[f.Name] = f.State
	}
	for name, want := range map[string]string{
		"Daemon version": "v9", "Daemon process": "pid 4242", "Daemon started": "2001-02-03 04:05:06Z",
		"Last reconcile": "completed", "Supervisor": "ready", "Entries left out of this view": "3",
	} {
		if facts[name] != want {
			t.Fatalf("fact %q is %q, want %q", name, facts[name], want)
		}
	}
	if len(p.Projects) != 2 || p.Projects[0].Name != "alpha" || p.Projects[1].Name != "beta" {
		t.Fatalf("%+v", p.Projects)
	}
	lead := p.Projects[0].Leads[0]
	if len(p.Projects[0].Leads) != 1 || lead.Name != "alpha/lead" || lead.State != "degraded" {
		t.Fatalf("%+v", lead)
	}
	if got := strings.Join(lead.Subagents, "|"); got != "coordinator feature-one · stalled|coordinator feature-two · ready" {
		t.Fatal(got)
	}
	s := lead.Session
	want := terminalconsole.Session{
		Provider:   "provider-a / model-a / effort-a (requested)",
		Runtime:    "manager-a",
		Authority:  "None (read-only view)",
		ID:         "epoch 7",
		Attachment: "None: the daemon's view attaches to no session",
		Binding:    "degraded (probe-failed): no answer to the probe",
		Execution:  "desired running, since 2001-02-03 05:05:06Z",
	}
	if s != want {
		t.Fatalf("%+v", s)
	}
	// A project that is here for its coordinators alone says it has no lead.
	orphan := p.Projects[1].Leads
	if len(orphan) != 1 || orphan[0].Name != "no lead listed" || orphan[0].State != "" ||
		strings.Join(orphan[0].Subagents, "|") != "coordinator unknown · unknown" {
		t.Fatalf("%+v", orphan)
	}
}

func TestDaemonProjectionOfAnEmptyView(t *testing.T) {
	p := daemonProjection(consoleclient.Result{State: consoleclient.StateOK})
	facts := map[string]string{}
	for _, f := range p.Facts {
		facts[f.Name] = f.State
	}
	for name, want := range map[string]string{
		"Daemon version": "unknown", "Daemon process": "unknown", "Daemon started": "unknown",
		"Last reconcile": "not completed", "Supervisor": "none listed",
	} {
		if facts[name] != want {
			t.Fatalf("fact %q is %q, want %q", name, facts[name], want)
		}
	}
	if _, ok := facts["Entries left out of this view"]; ok || len(p.Projects) != 0 || p.Problem != "" {
		t.Fatalf("%+v", p)
	}
}

func TestDaemonProjectionOfAStateWithoutAView(t *testing.T) {
	for _, state := range []consoleclient.State{
		consoleclient.StateNoDaemon, consoleclient.StateSlow, consoleclient.StateOtherProtocol,
		consoleclient.StateRefused, consoleclient.StateBadAnswer, consoleclient.StateUnavailable,
	} {
		// A view that came with the state is not shown.
		p := daemonProjection(consoleclient.Result{State: state, Text: "what is the matter", NextStep: "what to do", View: sampleView()})
		if p.Problem != "what is the matter" || p.NextStep != "what to do" || len(p.Projects) != 0 || len(p.Facts) != 0 {
			t.Fatalf("%s: %+v", state, p)
		}
	}
	// A state this command does not know, with no text, still says something.
	p := daemonProjection(consoleclient.Result{State: "new-state"})
	if p.Problem != "the daemon could not be asked" || len(p.Projects) != 0 {
		t.Fatalf("%+v", p)
	}
}

func TestEveryDaemonControlIsUnavailableWithItsReason(t *testing.T) {
	for _, r := range []consoleclient.Result{
		{State: consoleclient.StateOK, View: sampleView()},
		{State: consoleclient.StateNoDaemon, Text: "no daemon", NextStep: "start it"},
	} {
		p := daemonProjection(r)
		if len(p.Controls) != 6 || p.ControlUnavailable == "" {
			t.Fatalf("%+v", p.Controls)
		}
		attach := false
		for _, c := range p.Controls {
			if c.Name == "" || c.Reason == "" || c.Key == "" {
				t.Fatalf("%+v", c)
			}
			attach = attach || c.Key == "a"
			if strings.Contains(c.Name, "pending gate") && !strings.Contains(c.Reason, noOwnerChannel) {
				t.Fatalf("%+v", c)
			}
		}
		if !attach || !strings.Contains(p.Gate.State, noOwnerChannel) {
			t.Fatalf("%+v", p)
		}
	}
}

// viewOf draws a model at a window size and returns the text of the screen.
func viewOf(m *terminalconsole.Model, w, h int) string {
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return ansi.Strip(m.View().Content)
}

func TestTheDaemonViewBeforeTheFirstAnswer(t *testing.T) {
	cfg, _, ok := parse([]string{"--daemon", t.TempDir()}, &bytes.Buffer{})
	if !ok {
		t.Fatal("parse")
	}
	asked := 0
	m := daemonModelWith(cfg, func(context.Context, string) consoleclient.Result {
		asked++
		return consoleclient.Result{}
	})
	defer m.Close()
	v := viewOf(m, 140, 45)
	header := strings.Split(v, "\n")[0]
	if !strings.Contains(header, "the daemon's view, read-only") || strings.Contains(v, "fixture") || strings.Contains(v, "FIXTURE") {
		t.Fatal(v)
	}
	if !strings.Contains(v, "the daemon has not been") || !strings.Contains(v, "NO SESSION TERMINAL") {
		t.Fatal(v)
	}
	// Making and drawing the model asks nothing: the daemon is asked from a
	// command.
	if asked != 0 {
		t.Fatal("the daemon was asked outside a command")
	}
	if v := viewOf(m, 70, 20); !strings.Contains(v, "ATANIQ / the daemon's view, read-only") || !strings.Contains(v, "the daemon has not been asked yet") {
		t.Fatal(v)
	}
}

// TestTheDaemonSourceWithNoDaemon asks a folder that holds no socket,
// through consoleclient itself: the answer is the plain state and its next
// step, never an error.
func TestTheDaemonSourceWithNoDaemon(t *testing.T) {
	src := daemonSource{dir: t.TempDir(), read: consoleclient.Read}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, err := src.Refresh(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p.Problem != "no daemon answers at the socket" || p.NextStep != "start the daemon with: ataniqd run" || len(p.Projects) != 0 {
		t.Fatalf("%+v", p)
	}
	m := terminalconsole.NewWithOptions(sourceOf(p), terminalconsole.Options{Theme: "dark", Label: daemonLabel})
	defer m.Close()
	v := viewOf(m, 70, 20)
	for _, want := range []string{"no daemon answers at the socket", "Next step: start the daemon with: ataniqd run"} {
		if !strings.Contains(v, want) {
			t.Fatalf("the view does not say %q:\n%s", want, v)
		}
	}
	for _, bad := range []string{"error", "connect:", "dial", "no such file"} {
		if strings.Contains(v, bad) {
			t.Fatalf("the view says %q:\n%s", bad, v)
		}
	}
}

// sourceOf is a source that always gives p.
type sourceOf terminalconsole.Projection

func (s sourceOf) Snapshot() terminalconsole.Projection { return terminalconsole.Projection(s) }

// hostile is the set of strings a peer could send to mislead: control
// sequences, direction overrides, a 1 MiB line, and text shaped like the key
// hints and the cockpit's own words.
var hostile = []string{
	"x\x1b]52;c;payload\a\x1b[2J\x1b[31m",
	"\u009b31mcsi\u202eoverride\u2066isolate\u200bwidth",
	"line\none\r\ntwo\ttab",
	strings.Repeat("z", 1<<20),
	"q quit · Enter approve · a attach",
	"approve", "a-then-code", "develop", "ACTIONS (all unavailable)", "ATANIQ / DEMO FIXTURE",
}

func TestDaemonViewShowsHostileStringsAsPlainText(t *testing.T) {
	for i, h := range hostile {
		e := consoleclient.Entry{
			Kind: consoleclient.KindLead, Name: h, Project: h, Feature: h, Parent: h, State: consoleclient.EntryState(h),
			Desired: h, Reason: h, Detail: h, Manager: h, Provider: h, Model: h, Effort: h,
		}
		view := consoleclient.View{
			Daemon: consoleclient.Daemon{Version: h}, HasSupervisor: true, Supervisor: e,
			Projects: []consoleclient.Project{
				{Name: h, HasLead: true, Lead: e, Coordinators: []consoleclient.Entry{e, e}},
				// Two projects and two leads with one name.
				{Name: h, HasLead: true, Lead: e},
			},
		}
		for _, r := range []consoleclient.Result{
			{State: consoleclient.StateOK, View: view},
			{State: consoleclient.StateNoDaemon, Text: h, NextStep: h},
		} {
			m := terminalconsole.NewWithOptions(sourceOf(daemonProjection(r)), terminalconsole.Options{Theme: "dark", Label: daemonLabel})
			for _, size := range [][2]int{{140, 45}, {100, 30}, {70, 20}} {
				m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
				for _, key := range []tea.KeyPressMsg{{}, {Code: 'k', Mod: tea.ModCtrl}, {Code: tea.KeyEscape}, {Code: '?', Text: "?"}, {Code: tea.KeyEscape}, {Code: 'a', Text: "a"}} {
					if key.Code != 0 {
						m.Update(key)
					}
					raw := m.View().Content
					// The console's own styling is SGR only: no OSC, no
					// other CSI, no other control character than the line
					// break.
					rest := raw
					for {
						at := strings.IndexByte(rest, 0x1b)
						if at < 0 {
							break
						}
						end := strings.IndexByte(rest[at:], 'm')
						if !strings.HasPrefix(rest[at:], "\x1b[") || end < 0 || strings.Trim(rest[at+2:at+end], "0123456789;:") != "" {
							t.Fatalf("hostile %d, %dx%d: a sequence that is not SGR at %q", i, size[0], size[1], rest[at:min(len(rest), at+24)])
						}
						rest = rest[at+end+1:]
					}
					text := ansi.Strip(raw)
					for _, c := range text {
						if c == '\n' {
							continue
						}
						if c < 0x20 || c == 0x7f || (c >= 0x80 && c < 0xa0) || c == 0x202e || c == 0x2066 || c == 0x200b {
							t.Fatalf("hostile %d, %dx%d: the screen holds %U", i, size[0], size[1], c)
						}
					}
					if strings.Contains(text, "payload\a") || len(text) > 1<<17 {
						t.Fatalf("hostile %d, %dx%d: %d bytes", i, size[0], size[1], len(text))
					}
					// The header is the console's own, whatever the data says.
					if first := strings.Split(text, "\n")[0]; !strings.Contains(first, "the daemon's view, read-only") || strings.Contains(first, "DEMO FIXTURE") {
						t.Fatalf("hostile %d, %dx%d: the header is %q", i, size[0], size[1], first)
					}
				}
			}
			m.Close()
		}
	}
}

// fakeDaemon answers like consoleclient.Read, with what the test set last.
type fakeDaemon struct {
	mu    sync.Mutex
	r     consoleclient.Result
	asked int
	dirs  []string
}

func (f *fakeDaemon) read(_ context.Context, dir string) consoleclient.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked++
	f.dirs = append(f.dirs, dir)
	return f.r
}

func (f *fakeDaemon) set(r consoleclient.Result) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.r = r
	return f.asked
}

func (f *fakeDaemon) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.asked
}

// viewDriver carries out a model's commands as the Bubble Tea program would,
// without a terminal: each command runs on a goroutine of its own and its
// message comes back to the test, which gives it to the model.
type viewDriver struct {
	t    *testing.T
	m    *terminalconsole.Model
	msgs chan tea.Msg
	done chan struct{}
}

func newViewDriver(t *testing.T, m *terminalconsole.Model) *viewDriver {
	d := &viewDriver{t: t, m: m, msgs: make(chan tea.Msg, 64), done: make(chan struct{})}
	t.Cleanup(func() {
		close(d.done)
		m.Close()
	})
	return d
}

func (d *viewDriver) start(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() {
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				d.start(c)
			}
			return
		}
		select {
		case d.msgs <- msg:
		case <-d.done:
		}
	}()
}

// send gives the model a message and starts the commands it returns.
func (d *viewDriver) send(msg tea.Msg) {
	_, cmd := d.m.Update(msg)
	d.start(cmd)
}

// until carries messages to the model until its screen satisfies cond, and
// returns that screen.
func (d *viewDriver) until(what string, cond func(view string) bool) string {
	d.t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		view := ansi.Strip(d.m.View().Content)
		if cond(view) {
			return view
		}
		select {
		case msg := <-d.msgs:
			d.send(msg)
		case <-deadline:
			d.t.Fatalf("timed out waiting for %s:\n%s", what, view)
		}
	}
}

// TestTheDaemonViewFollowsTheDaemon runs the model that the command line
// makes against a daemon that changes between ticks: no daemon, a slow one,
// another protocol, then leads that change, then no daemon again. The
// cockpit shows each plain state with its next step where the leads would
// be, keeps asking, and shows the leads when they come.
func TestTheDaemonViewFollowsTheDaemon(t *testing.T) {
	dir := t.TempDir()
	cfg, _, ok := parse([]string{"--daemon", dir}, &bytes.Buffer{})
	if !ok {
		t.Fatal("parse")
	}
	cfg.options.Refresh = 5 * time.Millisecond
	daemon := &fakeDaemon{r: consoleclient.Result{State: consoleclient.StateNoDaemon, Text: "no daemon answers", NextStep: "start the daemon"}}
	d := newViewDriver(t, daemonModelWith(cfg, daemon.read))
	d.send(tea.WindowSizeMsg{Width: 70, Height: 20})

	plain := func(view string) {
		t.Helper()
		// "as of" is not in the list: on a loaded machine an answer can be
		// two periods late, and the header then rightly says so.
		for _, bad := range []string{"error", "Error", "panic", "goroutine", "fixture"} {
			if strings.Contains(view, bad) {
				t.Fatalf("the view says %q:\n%s", bad, view)
			}
		}
		if !strings.Contains(view, "ATANIQ / the daemon's view, read-only") {
			t.Fatalf("the header does not say whose view it is:\n%s", view)
		}
	}
	for _, r := range []consoleclient.Result{
		{State: consoleclient.StateNoDaemon, Text: "no daemon answers", NextStep: "start the daemon"},
		{State: consoleclient.StateSlow, Text: "the daemon was slow", NextStep: "check the daemon"},
		{State: consoleclient.StateOtherProtocol, Text: "another protocol", NextStep: "use one version"},
	} {
		asked := daemon.set(r)
		view := d.until(string(r.State), func(v string) bool {
			return strings.Contains(v, r.Text) && strings.Contains(v, "Next step: "+r.NextStep) && strings.Contains(v, "Asking again every 5ms.")
		})
		plain(view)
		// It keeps trying on the tick while the state lasts.
		d.until("more questions", func(string) bool { return daemon.count() >= asked+3 })
		plain(ansi.Strip(d.m.View().Content))
	}

	// The daemon answers: its leads stand where the state was.
	view := sampleView()
	daemon.set(consoleclient.Result{State: consoleclient.StateOK, View: view})
	screen := d.until("the leads", func(v string) bool { return strings.Contains(v, "alpha") && strings.Contains(v, "beta") })
	plain(screen)
	if strings.Contains(screen, "Next step") || strings.Contains(screen, "another protocol") {
		t.Fatal(screen)
	}
	// Open alpha: its lead and the lead's state.
	d.send(tea.KeyPressMsg{Code: tea.KeyEnter})
	screen = d.until("alpha's lead", func(v string) bool { return strings.Contains(v, "alpha/lead · degraded") })
	plain(screen)
	if !strings.Contains(screen, "coordinator feature-one · stalled") {
		t.Fatal(screen)
	}
	// The lead's state changes between ticks, and the page stays open on it.
	view.Projects[0].Lead.State = consoleclient.EntryReady
	view.Projects = append([]consoleclient.Project{{Name: "aardvark", HasLead: true, Lead: consoleclient.Entry{Name: "aardvark/lead", State: consoleclient.EntryStarting}}}, view.Projects...)
	daemon.set(consoleclient.Result{State: consoleclient.StateOK, View: view})
	screen = d.until("the lead's new state", func(v string) bool { return strings.Contains(v, "alpha/lead · ready") })
	plain(screen)
	if !strings.Contains(screen, "Project: alpha") || strings.Contains(screen, "aardvark/lead") {
		t.Fatal(screen)
	}
	// Every control is still unavailable, with its reason.
	d.send(tea.KeyPressMsg{Code: 'a', Text: "a"})
	screen = d.until("the attach notice", func(v string) bool { return strings.Contains(v, "Unavailable: Full attach.") })
	plain(screen)
	d.send(tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
	screen = d.until("the action list", func(v string) bool { return strings.Contains(v, "Actions (all unavailable)") })
	for _, want := range []string{"Approve pending gate: There is no owner channel in this build.", "Deny pending gate: There is no owner channel in this build.", "Full attach: The daemon's view is read-only."} {
		if !strings.Contains(screen, want) {
			t.Fatalf("the action list does not say %q:\n%s", want, screen)
		}
	}

	// The daemon goes away: the plain state again, in place of the leads.
	d.send(tea.KeyPressMsg{Code: tea.KeyEscape})
	daemon.set(consoleclient.Result{State: consoleclient.StateNoDaemon, Text: "no daemon answers", NextStep: "start the daemon"})
	screen = d.until("no daemon again", func(v string) bool { return strings.Contains(v, "no daemon answers") })
	plain(screen)
	// The leads are not kept under an age: the state is all the view shows.
	if strings.Contains(screen, "alpha") || strings.Contains(screen, "as of") {
		t.Fatal(screen)
	}
	// The cockpit at its full size says the same.
	d.send(tea.WindowSizeMsg{Width: 140, Height: 45})
	screen = d.until("the cockpit", func(v string) bool { return strings.Contains(v, "NO SESSION TERMINAL") })
	if first := strings.Split(screen, "\n")[0]; !strings.Contains(first, "ATANIQ · cockpit · the daemon's view, read-only") {
		t.Fatal(first)
	}
	daemon.mu.Lock()
	defer daemon.mu.Unlock()
	for _, got := range daemon.dirs {
		if got != dir {
			t.Fatalf("the daemon was asked in %q", got)
		}
	}
}

// unavailableResult is a result without a view, for the tests of the
// projection's fixed parts.
func unavailableResult() consoleclient.Result {
	return consoleclient.Result{State: consoleclient.StateNoDaemon, Text: "no daemon", NextStep: "start it"}
}
