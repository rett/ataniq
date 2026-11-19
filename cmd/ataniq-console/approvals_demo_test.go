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

// The command's part of the approvals flow tests. The whole console is driven
// in internal/terminalconsole (approvals_flow_test.go); here only what the
// command decides is checked: which backend each flag gives the console.

// TestApprovalsFlowDemoBuildsAModelWhoseHeaderSaysSampleCards: --demo gives the
// console the sample backend, and once the console has read the sample cards
// its header says they are samples and that a decision changes nothing. g
// opens the list of them, and a card opened there is not approved by a key
// alone: it has to be read and its code typed.
func TestApprovalsFlowDemoBuildsAModelWhoseHeaderSaysSampleCards(t *testing.T) {
	cfg, _, ok := parse([]string{"--demo"}, &bytes.Buffer{})
	if !ok {
		t.Fatal("parse")
	}
	if cfg.options.Backend != nil {
		t.Fatal("the flags alone give the console a backend")
	}
	d := newViewDriver(t, demoModel(cfg))
	d.send(tea.WindowSizeMsg{Width: 140, Height: 45})
	header := func(v string) string { return strings.Split(v, "\n")[0] }
	screen := d.until("the sample label in the header", func(v string) bool {
		return strings.Contains(header(v), terminalconsole.SampleLabel)
	})
	if h := header(screen); !strings.Contains(h, "sample cards: a decision changes nothing") {
		t.Fatalf("the header is %q", h)
	}

	d.send(tea.KeyPressMsg{Code: 'g', Text: "g"})
	screen = d.until("the list of sample cards", func(v string) bool {
		return strings.Contains(v, "APPROVALS") && strings.Contains(v, "Pending (") &&
			strings.Contains(v, "sample cards: a decision changes nothing")
	})
	if strings.Contains(screen, terminalconsole.NoOwnerChannel) {
		t.Fatal(screen)
	}
	d.send(tea.KeyPressMsg{Code: tea.KeyEnter})
	screen = d.until("a card", func(v string) bool { return strings.Contains(v, "APPROVAL ") })
	// The card is new and unread: a does not open the confirm field.
	d.send(tea.KeyPressMsg{Code: 'a', Text: "a"})
	screen = d.until("the reason Approve is off", func(v string) bool { return strings.Contains(v, "approve is off") })
	if strings.Contains(screen, "code>") {
		t.Fatalf("a on an unread card opened the confirm field:\n%s", screen)
	}
	d.send(tea.KeyPressMsg{Code: tea.KeyEscape})
	d.send(tea.KeyPressMsg{Code: tea.KeyEscape})
	d.until("the cockpit", func(v string) bool { return strings.Contains(v, "Projects & leads") })
}

// TestApprovalsFlowDaemonApprovalsSaysThereIsNoOwnerChannel: --daemon gives the
// console no approvals backend in this build, so g opens the screen that says
// there is no owner channel, at any size, and no key on it decides anything or
// opens a card.
func TestApprovalsFlowDaemonApprovalsSaysThereIsNoOwnerChannel(t *testing.T) {
	cfg, _, ok := parse([]string{"--daemon", t.TempDir()}, &bytes.Buffer{})
	if !ok {
		t.Fatal("parse")
	}
	if cfg.options.Backend != nil {
		t.Fatal("--daemon gives the console an approvals backend")
	}
	m := daemonModelWith(cfg, func(context.Context, string) consoleclient.Result { return consoleclient.Result{} })
	defer m.Close()
	for _, size := range [][2]int{{140, 45}, {80, 24}, {70, 20}} {
		viewOf(m, size[0], size[1])
		m.Update(tea.KeyPressMsg{Code: 'g', Text: "g"})
		v := ansi.Strip(m.View().Content)
		if !strings.Contains(v, terminalconsole.NoOwnerChannel) || strings.Contains(v, "Pending (") || strings.Contains(v, "APPROVAL ") {
			t.Fatalf("%v:\n%s", size, v)
		}
		// Nothing typed here is a command: no key returns one, and the
		// screen stays.
		for _, k := range []tea.KeyPressMsg{
			{Code: 'a', Text: "a"}, {Code: 'x', Text: "x"}, {Code: 'j', Text: "j"}, {Code: tea.KeyEnter}, {Code: 'L', Text: "L"},
		} {
			if _, cmd := m.Update(k); cmd != nil {
				t.Fatalf("%v: %v returned a command", size, k)
			}
		}
		if v := ansi.Strip(m.View().Content); !strings.Contains(v, terminalconsole.NoOwnerChannel) {
			t.Fatalf("%v: the screen went away:\n%s", size, v)
		}
		m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
		if v := ansi.Strip(m.View().Content); strings.Contains(v, terminalconsole.NoOwnerChannel) {
			t.Fatalf("%v: Esc did not go back:\n%s", size, v)
		}
	}
}
