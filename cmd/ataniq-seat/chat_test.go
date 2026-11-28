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

// hintingPort is a chat port that learns the chat hint.
type hintingPort struct {
	chatPort
	h  seatapp.ChatHint
	ch chan struct{}
}

func (p *hintingPort) ChatHint() seatapp.ChatHint       { return p.h }
func (p *hintingPort) ChatHintChanged() <-chan struct{} { return p.ch }

// TestGuardPassesTheChatHintOn: the guard passes the link's chat hint and its
// channel on, outside the decision guard, for the Leads pages' stream; for a
// port that learns none it says the hint is not live, with a channel that
// never closes.
func TestGuardPassesTheChatHintOn(t *testing.T) {
	p := &hintingPort{h: seatapp.ChatHint{Live: true, Cursor: 9}, ch: make(chan struct{})}
	g := newGuardedDaemon(p, "", nil)
	g.check = func(string) error { t.Fatal("the chat hint entered the decision guard"); return nil }
	if h := g.ChatHint(); !h.Live || h.Cursor != 9 {
		t.Errorf("hint %+v", h)
	}
	close(p.ch)
	select {
	case <-g.ChatHintChanged():
	default:
		t.Error("the port's channel was not passed on")
	}
	g.Daemon = &chatPort{}
	if h := g.ChatHint(); h.Live || h.Cursor != 0 {
		t.Errorf("hint %+v of a port that learns none", h)
	}
	select {
	case <-g.ChatHintChanged():
		t.Error("the channel of a port that learns no hint closed")
	default:
	}
}
