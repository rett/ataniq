package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/clientapi"
	"ataniq/internal/leadwire"
	tea "charm.land/bubbletea/v2"
)

// The legacy hello offers a directory and viewer tickets. The lead itself
// has messages enabled, so the editor can open for this epoch.
type viewerDirectory struct{ t *testing.T }

func (viewerDirectory) Operations() []string { return []string{leadwire.OpList, leadwire.OpTicket} }
func (viewerDirectory) Mutating() bool       { return true }
func (d viewerDirectory) Handle(_ context.Context, op string, _ json.RawMessage) (any, *clientapi.ErrorBody) {
	if op != leadwire.OpList {
		d.t.Errorf("unexpected request %s", op)
		return nil, &clientapi.ErrorBody{Code: "refused"}
	}
	return leadwire.ListResult{Leads: []leadwire.Lead{{
		Key: "alpha", Role: "lead-alpha", Defined: true, State: "ready", Epoch: 3, Inject: true,
	}}}, nil
}

func TestLeadsCapabilityRefusal(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	listener, _, err := clientapi.Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	counters := &clientapi.Counters{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- clientapi.Serve(ctx, listener, &ownerDaemon{t: t}, clientapi.Options{
			Extra: []clientapi.Extension{viewerDirectory{t}}, Counters: counters,
		})
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})

	client, err := newLeadsClient(dir)
	if err != nil {
		t.Fatal(err)
	}
	d := newViewDriver(t, rigFor(t, client).model())
	d.send(tea.WindowSizeMsg{Width: 140, Height: 45})
	d.send(openLeadsKey())
	listShown(d)
	d.send(tea.KeyPressMsg{Code: 'w', Text: "w"})
	d.until("writer ticket refusal", func(v string) bool {
		return strings.Contains(v, "attach ticket: refused: the daemon does not offer a writer ticket for this lead")
	})
	if _, ok := d.m.Attach(); ok {
		t.Fatal("refused writer ticket requested an attach")
	}

	openEditor := func() {
		d.send(tea.KeyPressMsg{Code: 'm', Text: "m"})
		d.until("message editor", func(v string) bool { return strings.Contains(v, "message to alpha (epoch 3)>") })
	}
	openEditor()
	d.send(tea.KeyPressMsg{Code: 'h', Text: "h"})
	d.send(tea.KeyPressMsg{Code: tea.KeyEnter})
	d.until("message refusal", func(v string) bool {
		return strings.Contains(v, "message: refused: the daemon does not offer sending a message to this lead")
	})
	openEditor()
	if s := counters.Snapshot(); s.Unsupported != 0 || s.Served[leadwire.OpTicket] != 0 {
		t.Fatalf("refused command reached the daemon: %+v", s)
	}
}
