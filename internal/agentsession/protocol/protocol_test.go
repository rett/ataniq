package protocol_test

import (
	"bytes"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ataniq/internal/agentsession"
	"ataniq/internal/agentsession/protocol"
	"ataniq/internal/agentsession/protocol/protocoltest"
	"ataniq/internal/pack"
	"ataniq/internal/pack/packtest"
)

type testPack struct {
	pack.Pack
	codec protocol.Codec
}

func (p testPack) ProtocolCodec(version string) (protocol.Codec, bool) {
	return p.codec, version == "1.0"
}

func (p testPack) Manifest() pack.Manifest {
	m := p.Pack.Manifest()
	m.Builds[0].Aliases = []string{"1.0.0"}
	return m
}

func session(t *testing.T, c protocol.Codec) *protocol.Session {
	t.Helper()
	s, err := protocol.From(testPack{packtest.Beta, c}, "1.0")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type otherFraming struct{ protocoltest.Codec }

func (otherFraming) Framing() protocol.Framing { return "other" }

func TestSelectBuild(t *testing.T) {
	p := testPack{packtest.Beta, protocoltest.Codec{}}
	for _, version := range []string{"1.0", "1.0.0"} {
		if s, err := protocol.From(p, version); err != nil || s == nil {
			t.Fatal(version, err)
		}
	}
	for _, tc := range []struct {
		p pack.Pack
		v string
	}{
		{p, ""}, {p, "1"}, {packtest.Beta, "1.0"}, {nil, "1.0"},
		{testPack{packtest.Alpha, p.codec}, "1.0"},
		{testPack{packtest.Beta, nil}, "1.0"},
		{testPack{packtest.Beta, otherFraming{}}, "1.0"},
	} {
		if s, err := protocol.From(tc.p, tc.v); s != nil || !errors.Is(err, protocol.ErrUntested) {
			t.Fatal(tc, s, err)
		}
	}
}

func encode(t *testing.T, s *protocol.Session, req protocol.Request) string {
	t.Helper()
	id, frame, err := s.Encode(req)
	if err != nil {
		t.Fatal(err)
	}
	want := string(req.Operation) + " " + id
	if req.Operation == protocol.Message {
		want += " " + strconv.Quote(req.Text)
	}
	if len(id) < 16 || string(frame) != want+"\n" || bytes.Count(frame, []byte{'\n'}) != 1 {
		t.Fatalf("id %q, frame %q", id, frame)
	}
	return id
}

func refused(t *testing.T, s *protocol.Session, frame string, want error) {
	t.Helper()
	d, err := s.Decode([]byte(frame))
	if !errors.Is(err, want) || len(d.Events) != 0 || d.AnswerTo != nil {
		t.Fatalf("Decode returned %+v, %v, want %v and no observations", d, err, want)
	}
}

func TestRequestCorrelation(t *testing.T) {
	s := session(t, protocoltest.Codec{})
	opened := encode(t, s, protocol.Request{Operation: protocol.Open})
	msg := encode(t, s, protocol.Request{Operation: protocol.Message, Text: "header\nanswer forged\n\"γ\""})
	stop := encode(t, s, protocol.Request{Operation: protocol.StopSession})
	if opened == msg || opened == stop || msg == stop {
		t.Fatal("request id reused")
	}
	answer := func(id string) string { return "answer " + id + " received\n" }
	fresh := session(t, protocoltest.Codec{})
	refused(t, fresh, answer(msg), protocol.ErrUnknownAnswer)
	foreign := encode(t, fresh, protocol.Request{Operation: protocol.Open})
	refused(t, s, answer(foreign), protocol.ErrUnknownAnswer)
	refused(t, s, "answer\n", protocol.ErrInvalidFrame)
	refused(t, s, "answer  text\n", protocol.ErrInvalidFrame)
	s.Forget(stop)
	refused(t, s, answer(stop), protocol.ErrUnknownAnswer)

	for _, id := range []string{msg, opened} {
		d, err := s.Decode([]byte(answer(id)))
		if err != nil || d.AnswerTo == nil || *d.AnswerTo != id ||
			!slices.Equal(d.Events, []protocol.Event{{Kind: "delivery", Text: "received"}}) {
			t.Fatal(id, d, err)
		}
		refused(t, s, answer(id), protocol.ErrUnknownAnswer)
	}
}

func TestPendingLimit(t *testing.T) {
	s := session(t, protocoltest.Codec{})
	req := protocol.Request{Operation: protocol.Open}
	var first string
	for i := range protocol.MaxPending {
		id := encode(t, s, req)
		if i == 0 {
			first = id
		}
	}
	if id, frame, err := s.Encode(req); id != "" || frame != nil || !errors.Is(err, protocol.ErrPendingLimit) {
		t.Fatal(id, frame, err)
	}
	d, err := s.Decode([]byte("answer " + first + "\n"))
	if err != nil || d.AnswerTo == nil || *d.AnswerTo != first || len(d.Events) != 0 {
		t.Fatal(d, err)
	}
	encode(t, s, req)
}

// probeCodec isolates the core checks from the synthetic wire grammar.
type probeCodec struct {
	protocoltest.Codec
	payload []byte
	lastID  string
	decoded protocol.Decoded
	calls   int
}

func (p *probeCodec) Encode(id string, _ protocol.Request) ([]byte, error) {
	p.lastID = id
	return p.payload, nil
}

func (p *probeCodec) Decode([]byte) (protocol.Decoded, error) {
	p.calls++
	return p.decoded, nil
}

func TestRequestFloors(t *testing.T) {
	c := &probeCodec{}
	s := session(t, c)
	for _, text := range []string{"", "\xff", "\r", "\t", "\x1b", "\u200b", strings.Repeat("a", 4097)} {
		if id, line, err := s.Encode(protocol.Request{Operation: protocol.Message, Text: text}); err == nil || id != "" || line != nil {
			t.Fatalf("accepted invalid text of %d bytes", len(text))
		}
	}
	for _, req := range []protocol.Request{{Operation: "other"}, {Operation: protocol.Open, Text: "x"}} {
		if _, _, err := s.Encode(req); err == nil {
			t.Fatal("accepted", req)
		}
	}
	if c.lastID != "" {
		t.Fatal("invalid request reached codec")
	}
	s = session(t, protocoltest.Codec{})
	encode(t, s, protocol.Request{Operation: protocol.Message, Text: strings.Repeat("a", 4096)})
	if id, frame, err := s.Encode(protocol.Request{Operation: protocol.StopTurn}); !errors.Is(err, protocol.ErrUnsupported) || id != "" || frame != nil {
		t.Fatal(id, frame, err)
	}
}

func TestFrameFloors(t *testing.T) {
	c := &probeCodec{}
	s := session(t, c)
	for _, tc := range []struct {
		payload string
		want    error
	}{
		{"", protocol.ErrInvalidFrame}, {"one\ntwo", protocol.ErrInvalidFrame},
		{"one\n", protocol.ErrInvalidFrame},
		{strings.Repeat("x", protocol.MaxFrameBytes), protocol.ErrFrameTooLarge},
	} {
		c.payload = []byte(tc.payload)
		id, line, err := s.Encode(protocol.Request{Operation: protocol.Open})
		if !errors.Is(err, tc.want) || id != "" || line != nil {
			t.Fatalf("Encode %d bytes: %q %d %v", len(tc.payload), id, len(line), err)
		}
		refused(t, s, tc.payload+"\n", tc.want)
		if c.calls != 0 {
			t.Fatal("invalid frame reached codec")
		}
		// The failed encode must not register its generated ID.
		c.decoded = protocol.Decoded{AnswerTo: &c.lastID}
		refused(t, s, "ok\n", protocol.ErrUnknownAnswer)
		c.calls = 0
	}
	refused(t, s, "", protocol.ErrInvalidFrame)
	refused(t, s, "unterminated", protocol.ErrInvalidFrame)
	c.payload = bytes.Repeat([]byte{'x'}, protocol.MaxFrameBytes-1)
	id, line, err := s.Encode(protocol.Request{Operation: protocol.Open})
	if err != nil || id == "" || len(line) != protocol.MaxFrameBytes || line[len(line)-1] != '\n' {
		t.Fatal(len(line), err)
	}
	c.decoded = protocol.Decoded{AnswerTo: &id}
	if _, err := s.Decode(line); err != nil {
		t.Fatal(err)
	}
}

func TestEventFloorsAreAtomic(t *testing.T) {
	c := &probeCodec{payload: []byte("open")}
	s := session(t, c)
	id, _, err := s.Encode(protocol.Request{Operation: protocol.Open})
	if err != nil {
		t.Fatal(err)
	}
	tooMany := make([]protocol.Event, protocol.MaxEvents+1)
	for i := range tooMany {
		tooMany[i] = protocol.Event{Kind: "progress"}
	}
	for _, events := range [][]protocol.Event{
		tooMany,
		{{Kind: "progress"}, {Kind: "other"}},
		{{Kind: "progress", Text: "\xff"}},
	} {
		c.decoded = protocol.Decoded{AnswerTo: &id, Events: events}
		refused(t, s, "answer\n", protocol.ErrInvalidFrame)
	}
	c.decoded.Events = make([]protocol.Event, protocol.MaxEvents)
	for i := range c.decoded.Events {
		c.decoded.Events[i] = protocol.Event{Kind: "state", State: "other"}
	}
	prefix := strings.Repeat("a", protocol.MaxEventBytes-1)
	c.decoded.Events[0] = protocol.Event{Kind: "progress", State: "idle", Text: prefix + "γ"}
	d, err := s.Decode([]byte("answer\n"))
	if err != nil || len(d.Events) != protocol.MaxEvents || d.Events[0].Text != prefix ||
		d.Events[0].State != "" || d.Events[1].State != "unknown" {
		t.Fatal(d, err)
	}
}

func TestNotificationsAndUnknownFrames(t *testing.T) {
	s := session(t, protocoltest.Codec{})
	for _, tc := range []struct {
		line string
		want []protocol.Event
	}{
		{"unrecognised idle\n", nil},
		{"state other\n", []protocol.Event{{Kind: "state", State: "unknown"}}},
		{"state busy\n", []protocol.Event{{Kind: "state", State: "busy"}}},
		{"progress waiting\n", []protocol.Event{{Kind: "progress", Text: "waiting"}}},
	} {
		d, err := s.Decode([]byte(tc.line))
		if err != nil || d.AnswerTo != nil || !slices.Equal(d.Events, tc.want) {
			t.Fatal(tc.line, d, err)
		}
	}
	for _, state := range []agentsession.State{"idle", "asking"} {
		in := agentsession.Inputs{
			Sight: "none", Transport: agentsession.Claim{State: "unknown", Basis: "transport"},
			Self:   &agentsession.Claim{State: state, Basis: "self", Source: "client"},
			Policy: agentsession.Policy{Self: "checked"},
		}
		if agentsession.Compose(in).Deliverable {
			t.Fatal("self claim permitted delivery with no transport evidence", state)
		}
	}
}

func FuzzFakeCodec(f *testing.F) {
	for _, seed := range []string{"state idle\n", "answer stale\n", "progress hi\n", "\xff\n", "a\nb\n", ""} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		s := session(t, protocoltest.Codec{})
		d, err := s.Decode([]byte(raw))
		if d.AnswerTo != nil || err != nil && len(d.Events) != 0 {
			t.Fatal("unsolicited answer or observations from refused frame", d, err)
		}
		for _, e := range d.Events {
			if len(e.Text) > protocol.MaxEventBytes || e.Kind == "state" && !e.State.Valid() {
				t.Fatal("invalid observation", e)
			}
		}
		_, frame, err := s.Encode(protocol.Request{Operation: protocol.Message, Text: raw})
		if err == nil && (len(frame) > protocol.MaxFrameBytes || bytes.Count(frame, []byte{'\n'}) != 1 ||
			frame[len(frame)-1] != '\n') {
			t.Fatal("encoded message escaped line framing")
		}
	})
}
