package seatlink

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"time"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
	"ataniq/internal/seatapp"
	"ataniq/internal/seatgate"
)

const CapChat = "chat"

var _ seatapp.Chat = (*Daemon)(nil)

var errChatAnswer = errors.New("seatlink: invalid chat response")
var chatCodes = map[string]error{
	ownerwire.CodeChatOff: seatapp.ErrChatOff, ownerwire.CodeChatUnknownLead: seatapp.ErrChatUnknownLead,
	ownerwire.CodeChatBusy: seatapp.ErrBusy, seatgate.CodeRateLimited: seatapp.ErrBusy,
	seatgate.CodeBusy: seatapp.ErrBusy, codeTooManyRefusals: seatapp.ErrBusy, codeUnknownOp: seatapp.ErrNotOffered,
}

// ChatOffered uses the current connection's hello. Client.Caps also remembers
// the last hello while disconnected, so checking the connection matters.
func (d *Daemon) ChatOffered() bool {
	_, connected := d.c.Connected()
	return connected && d.offers(CapChat)
}

type chatPeer interface {
	Connected() (time.Time, bool)
	Caps() []string
	Call(context.Context, string, any, any) error
}

type chatCached struct {
	raw   json.RawMessage
	until time.Time
}

type chatMemory struct {
	// Serialize chat alone so a late call cannot restore a forgotten answer.
	mu         sync.Mutex
	connection time.Time
	answers    map[seatapp.ChatCall]chatCached
}

func (d *Daemon) Chat(ctx context.Context, c seatapp.ChatCall) (seatapp.ChatView, error) {
	return d.chat(ctx, c, d.c)
}

// chat forgets every kept answer when the gate refuses a call or answers
// one the link does not take, since the leads or the host changed. A call
// the link does not send (no connection, no capability, no room) is no news
// of the gate, and keeps them: a tap that meets the busy read room must not
// cost the next page its directory.
func (d *Daemon) chat(ctx context.Context, c seatapp.ChatCall, peer chatPeer) (v seatapp.ChatView, err error) {
	m := &d.cmem
	m.mu.Lock()
	defer m.mu.Unlock()
	connection, connected := peer.Connected()
	if !connected || ctx.Err() != nil {
		return v, seatapp.ErrDaemonUnavailable
	}
	if !slices.Contains(peer.Caps(), CapChat) {
		return v, seatapp.ErrNotOffered
	}
	if connection != m.connection {
		m.answers = nil
		m.connection = connection
	}
	now := d.gnow()
	for k, p := range m.answers {
		select {
		case <-k.SessionDone:
			delete(m.answers, k)
		default:
			if !now.Before(p.until) {
				delete(m.answers, k)
			}
		}
	}
	p := ownerwire.SeatChatParams{Verb: c.Verb, Lead: c.Lead, After: c.After}
	if c.Verb == seatapp.ChatRead {
		p.Limit = seatapp.MaxChatEntries
	}
	params, err := ownerwire.EncodeSeatChatParams(p)
	if err != nil {
		return v, errors.New("seatlink: invalid chat request")
	}
	cached, hit := m.answers[c]
	raw := cached.raw
	if !hit {
		if !d.budget.take(kindRead) {
			return v, seatapp.ErrBusy
		}
		if err := peer.Call(ctx, ownerwire.OpSeatChat, json.RawMessage(params), &raw); err != nil {
			m.answers = nil
			return v, mapCall(ctx, ownerwire.OpSeatChat, err, chatCodes)
		}
	}
	v, err = decodeChat(c, raw)
	if err != nil {
		m.answers = nil
		return seatapp.ChatView{}, err
	}
	select {
	case <-c.SessionDone:
		return seatapp.ChatView{}, seatapp.ErrDaemonUnavailable
	default:
	}
	if current, ok := peer.Connected(); !ok || current != connection || ctx.Err() != nil {
		return seatapp.ChatView{}, seatapp.ErrDaemonUnavailable
	}
	v.Connection = connection
	if !hit {
		ttl := viewFresh
		if c.Verb == seatapp.ChatLeads || v.Reset {
			// A fresh directory may revoke access or describe a new incarnation.
			m.answers = nil
		}
		if c.Verb == seatapp.ChatLeads {
			ttl = listFresh
		}
		// Every directory, the trial's or a real one, is kept listFresh (20
		// seconds), as the interview's list is. A transcript page is kept
		// viewFresh (10 seconds) only when it is the trial's, whose samples
		// never change: a real transcript requires live read authorization
		// on every request, so it is never kept.
		if c.Verb == seatapp.ChatLeads || v.StandIn {
			if m.answers == nil || len(m.answers) >= 32 {
				m.answers = make(map[seatapp.ChatCall]chatCached)
			}
			m.answers[c] = chatCached{raw: raw, until: d.gnow().Add(ttl)}
		}
	}
	return v, nil
}

func decodeChat(c seatapp.ChatCall, raw json.RawMessage) (seatapp.ChatView, error) {
	r, err := ownerwire.DecodeSeatChatResult(raw)
	if err != nil || r.State != c.Verb {
		return seatapp.ChatView{}, errChatAnswer
	}
	v := seatapp.ChatView{State: r.State, StandIn: r.StandIn, Lead: r.Lead, Epoch: r.Epoch, Head: r.Head, More: r.More, Reset: r.Reset}
	if r.State == seatapp.ChatLeads {
		// The typed union loses the presence of optional zero-valued integers
		// and booleans. None of the read members belongs on a list.
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return seatapp.ChatView{}, errChatAnswer
		}
		for name := range fields {
			if name != "state" && name != "stand_in" && name != "leads" {
				return seatapp.ChatView{}, errChatAnswer
			}
		}
		v.Rows = make([]seatapp.LeadRow, 0, len(r.Leads))
		for _, row := range r.Leads {
			v.Rows = append(v.Rows, seatapp.LeadRow{Lead: row.Lead, Chat: row.Chat, State: row.State, Epoch: row.Epoch, Head: row.Head})
		}
	} else {
		v.Entries = make([]seatapp.ChatEntry, 0, len(r.Entries))
		for _, e := range r.Entries {
			at, err := record.ParseTime(e.At)
			if err != nil {
				return seatapp.ChatView{}, errChatAnswer
			}
			v.Entries = append(v.Entries, seatapp.ChatEntry{Seq: e.Seq, Who: e.Who, At: at, Text: string(e.Text), Cut: e.Cut})
		}
	}
	if !v.ValidFor(c) {
		return seatapp.ChatView{}, errChatAnswer
	}
	return v, nil
}
