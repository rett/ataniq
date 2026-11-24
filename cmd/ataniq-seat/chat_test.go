package main

import (
	"ataniq/internal/seatapp"
	"context"
	"testing"
)

type chatPort struct {
	unwiredDaemon
	calls []seatapp.ChatCall
}

func (*chatPort) ChatOffered() bool { return true }
func (p *chatPort) Chat(_ context.Context, c seatapp.ChatCall) (seatapp.ChatView, error) {
	p.calls = append(p.calls, c)
	return seatapp.ChatView{State: c.Verb, Lead: c.Lead, StandIn: true}, nil
}

func TestGuardPassesChat(t *testing.T) {
	p := &chatPort{}
	g := newGuardedDaemon(p, "", nil)
	g.check = func(string) error { t.Fatal("chat entered the decision guard"); return nil }
	if !g.ChatOffered() || len(p.calls) != 0 {
		t.Fatal("chat offer")
	}
	for _, call := range []seatapp.ChatCall{{Verb: "leads"}, {Verb: "read", Lead: "daemon:alpha", After: 3}} {
		got, err := g.Chat(context.Background(), call)
		if err != nil || !got.StandIn || got.State != call.Verb || p.calls[len(p.calls)-1] != call {
			t.Fatal("chat forwarding", err)
		}
	}
	g.Daemon = unwiredDaemon{}
	if g.ChatOffered() {
		t.Fatal("unwired offer")
	}
	if _, err := g.Chat(context.Background(), seatapp.ChatCall{Verb: "leads"}); err != seatapp.ErrNotOffered {
		t.Fatal("unwired chat", err)
	}
}
