//go:build !(linux && ataniq_ownerchannel)

package main

import (
	"bytes"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"ataniq/internal/terminalconsole"
)

// TestTheReleaseBuildHasNoOwnerChannel holds what the console is given with
// --daemon in a build without the owner channel: no backend, so the
// approvals screen says there is no owner channel, and no key on it asks the
// daemon anything. This is the build every release is.
func TestTheReleaseBuildHasNoOwnerChannel(t *testing.T) {
	b, closeOwner, err := ownerBackend(t.TempDir())
	if b != nil || err != nil {
		t.Fatalf("ownerBackend = %v, %v", b, err)
	}
	closeOwner()
	cfg, _, ok := parse([]string{"--daemon", t.TempDir()}, &bytes.Buffer{})
	if !ok {
		t.Fatal("parse")
	}
	m, closeModel, err := daemonConsole(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer closeModel()
	defer m.Close()
	viewOf(m, 140, 45)
	m.Update(tea.KeyPressMsg{Code: 'g', Text: "g"})
	v := viewOf(m, 140, 45)
	if !strings.Contains(v, terminalconsole.NoOwnerChannel) || strings.Contains(v, "Pending (") {
		t.Fatal(v)
	}
	// The cockpit's approval controls say so too.
	p := daemonProjectionFor(unavailableResult(), false)
	for _, c := range p.Controls {
		if c.Approval && !strings.Contains(c.Reason, noOwnerChannel) {
			t.Fatalf("%+v", c)
		}
	}
}
