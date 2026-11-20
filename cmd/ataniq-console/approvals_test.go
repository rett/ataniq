package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"ataniq/internal/consoleclient"
	"ataniq/internal/terminalconsole"
)

func TestDemoGivesTheConsoleTheSampleBackend(t *testing.T) {
	cfg, _, ok := parse([]string{"--demo"}, &bytes.Buffer{})
	if !ok {
		t.Fatal("parse")
	}
	if cfg.options.Backend != nil {
		t.Fatal("the flags alone give no backend")
	}
	d := newViewDriver(t, demoModel(cfg))
	d.send(tea.WindowSizeMsg{Width: 140, Height: 45})
	// The console reads the sample cards from a command, and then the header
	// of the cockpit says they are samples.
	screen := d.until("the sample label in the header", func(v string) bool {
		return strings.Contains(strings.Split(v, "\n")[0], terminalconsole.SampleLabel)
	})
	if !strings.Contains(strings.Split(screen, "\n")[0], "sample cards: a decision changes nothing") {
		t.Fatal(screen)
	}
	// g opens the approvals view on the sample cards, and Esc goes back.
	d.send(tea.KeyPressMsg{Code: 'g', Text: "g"})
	screen = d.until("the approvals list", func(v string) bool {
		return strings.Contains(v, "APPROVALS") && strings.Contains(v, "sample cards: a decision changes nothing") && strings.Contains(v, "Pending (")
	})
	if strings.Contains(screen, terminalconsole.NoOwnerChannel) {
		t.Fatal(screen)
	}
	d.send(tea.KeyPressMsg{Code: tea.KeyEscape})
	d.until("the cockpit", func(v string) bool { return strings.Contains(v, "Projects & leads") })
}

func TestDaemonApprovalsSaysThereIsNoOwnerChannel(t *testing.T) {
	cfg, _, ok := parse([]string{"--daemon", t.TempDir()}, &bytes.Buffer{})
	if !ok {
		t.Fatal("parse")
	}
	m := daemonModelWith(cfg, func(context.Context, string) consoleclient.Result { return consoleclient.Result{} })
	defer m.Close()
	for _, size := range [][2]int{{140, 45}, {70, 20}} {
		viewOf(m, size[0], size[1])
		m.Update(tea.KeyPressMsg{Code: 'g', Text: "g"})
		v := ansi.Strip(m.View().Content)
		if !strings.Contains(v, "no owner channel in this build") || strings.Contains(v, "Pending (") {
			t.Fatalf("%v:\n%s", size, v)
		}
		m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
		if v := ansi.Strip(m.View().Content); strings.Contains(v, "no owner channel in this build") && strings.Contains(v, "APPROVALS") {
			t.Fatalf("%v: Esc did not go back:\n%s", size, v)
		}
	}
}

func TestTheBellFlag(t *testing.T) {
	var errout bytes.Buffer
	cfg, _, ok := parse([]string{"--demo"}, &errout)
	if !ok || cfg.options.BellOff {
		t.Fatalf("the bell is on by default: %+v", cfg.options)
	}
	cfg, code, ok := parse([]string{"--demo", "--no-bell"}, &errout)
	if !ok || code != 0 || !cfg.options.BellOff || errout.Len() != 0 {
		t.Fatalf("--no-bell: %v %d %+v %s", ok, code, cfg.options, errout.String())
	}
	// The bell rings for a card, and only --demo has cards in this build.
	for _, args := range [][]string{
		{"--no-bell"},
		{"--daemon", "d", "--no-bell"},
		{"--attach", "d", "--ticket-file", "t", "--no-bell"},
	} {
		var out, err bytes.Buffer
		if got := run(args, &out, &err); got != 2 || !strings.Contains(err.String(), "--no-bell needs --demo") || strings.Count(err.String(), "Usage:") != 1 {
			t.Fatalf("%q: %d %s", args, got, err.String())
		}
	}
	var out, err bytes.Buffer
	if code := run([]string{"--help"}, &out, &err); code != 0 || !strings.Contains(err.String(), "-no-bell") || !strings.Contains(err.String(), "[--no-bell]") {
		t.Fatal(code, err.String())
	}
}
