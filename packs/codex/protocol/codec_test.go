package protocol_test

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	core "ataniq/internal/agentsession/protocol"
	"ataniq/internal/pack"
	"ataniq/packs/codex/protocol"
	"ataniq/packs/holder"
)

const dir = "../testdata/appserver/"

func TestSchemaPin(t *testing.T) {
	compressed, err := os.ReadFile(dir + "schema.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	r, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(r)
	if closeErr := r.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil || !jsontext.Value(b).IsValid() {
		t.Fatal("schema capture", err)
	}
	digest := func(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }
	measured := digest(b)
	if measured != protocol.SchemaDigest {
		t.Fatal("captured schema changed", measured)
	}
	b[0] ^= 1
	for _, tc := range []struct {
		name            pack.Name
		version, digest string
		want            error
	}{
		{"codex", "0.172.0", measured, nil}, {"codex", "0.172.0", digest(b), protocol.ErrSchema},
		{"codex", "0.172.0", "", protocol.ErrSchema}, {"codex", "0.172", measured, core.ErrUntested},
		{"claude", "0.172.0", measured, core.ErrUntested},
	} {
		c, err := holder.ProtocolCodec(tc.name, tc.version, tc.digest)
		if !errors.Is(err, tc.want) || (c == nil) != (err != nil) {
			t.Fatal(tc, err)
		}
		if c != nil && c.Framing() != core.Lines {
			t.Fatal(c.Framing())
		}
	}
}

func fresh(t *testing.T) protocol.Codec {
	t.Helper()
	c, err := holder.ProtocolCodec("codex", protocol.Version, protocol.SchemaDigest)
	if err != nil {
		t.Fatal(err)
	}
	return c.(protocol.Codec)
}

func frames(t *testing.T, name string) [][]byte {
	t.Helper()
	b, err := os.ReadFile(dir + name + ".jsonl")
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Split(bytes.TrimSuffix(b, []byte{'\n'}), []byte{'\n'})
}

func observe(t *testing.T, c protocol.Codec, frame []byte, events ...core.Event) {
	t.Helper()
	d, err := c.Decode(frame)
	if err != nil || d.AnswerTo != nil || !reflect.DeepEqual(d.Events, events) {
		t.Fatal(string(frame), d, err)
	}
}

func equalJSON(t *testing.T, b []byte, want string) {
	t.Helper()
	a, v := jsontext.Value(b).Clone(), jsontext.Value(want)
	if a.Canonicalize() != nil || v.Canonicalize() != nil || !bytes.Equal(a, v) || bytes.ContainsRune(b, '\n') {
		t.Fatalf("payload %s\nwant %s", b, want)
	}
}

func TestConversation(t *testing.T) {
	c, f := fresh(t), frames(t, "events")
	id := "req"
	send := func(op core.Operation, text, want string) {
		t.Helper()
		b, err := c.Encode(id, core.Request{Operation: op, Text: text})
		if err != nil {
			t.Fatal(err)
		}
		equalJSON(t, b, want)
	}
	send(core.Open, "", `{"method":"initialize","id":"req","params":{"clientInfo":{"name":"ataniq","version":"1"}}}`)
	equalJSON(t, c.Initialized(), `{"method":"initialized"}`)
	b, err := c.StartThread(id)
	if err != nil {
		t.Fatal(err)
	}
	equalJSON(t, b, `{"method":"thread/start","id":"req","params":{}}`)
	observe(t, c, f[0])
	send(core.Message, "a\nb", `{"method":"turn/start","id":"req","params":{"threadId":"thread-a","input":[{"type":"text","text":"a\nb"}]}}`)
	observe(t, c, f[1], core.Event{Kind: "state", State: "busy"})
	b, err = c.Steer(id, "thread-a", "turn-a", "extra")
	if err != nil {
		t.Fatal(err)
	}
	equalJSON(t, b, `{"id":"req","method":"turn/steer","params":{"threadId":"thread-a","expectedTurnId":"turn-a","input":[{"type":"text","text":"extra"}]}}`)
	send(core.StopTurn, "", `{"method":"turn/interrupt","id":"req","params":{"threadId":"thread-a","turnId":"turn-a"}}`)
	observe(t, c, f[2], core.Event{Kind: "progress", Text: "two words"})
	observe(t, c, f[3], core.Event{Kind: "question", Text: "Permission requested"})
	observe(t, c, f[4], core.Event{Kind: "state", State: "idle"})
	observe(t, c, f[5], core.Event{Kind: "ended"})
}

func TestStaleTurn(t *testing.T) {
	c, f := fresh(t), frames(t, "events")
	refuse := func(c protocol.Codec, thread, turn string) {
		t.Helper()
		if b, err := c.Steer("r", thread, turn, "text"); b != nil || !errors.Is(err, protocol.ErrTurn) {
			t.Fatal(thread, turn, string(b), err)
		}
	}
	refuse(c, "", "")
	observe(t, c, f[0])
	observe(t, c, f[1], core.Event{Kind: "state", State: "busy"})
	refuse(fresh(t), "thread-a", "turn-a")
	refuse(c, "other", "turn-a")
	observe(t, c, bytes.ReplaceAll(f[1], []byte("turn-a"), []byte("turn-b")), core.Event{Kind: "state", State: "busy"})
	observe(t, c, f[4])
	observe(t, c, bytes.ReplaceAll(f[4], []byte("thread-a"), []byte("other")))
	refuse(c, "thread-a", "turn-a")
	if b, err := c.Steer("r", "thread-a", "turn-b", "text"); b == nil || err != nil {
		t.Fatal(err)
	}
	observe(t, c, bytes.ReplaceAll(f[4], []byte("turn-a"), []byte("turn-b")), core.Event{Kind: "state", State: "idle"})
	refuse(c, "thread-a", "turn-b")
	observe(t, c, f[5], core.Event{Kind: "ended"})
	observe(t, c, f[0])
	observe(t, c, f[1])
	refuse(c, "thread-a", "turn-a")
}

func TestEncodeRefusals(t *testing.T) {
	c, f := fresh(t), frames(t, "events")
	for _, req := range []core.Request{{Operation: core.StopTurn}, {Operation: core.Message, Text: "text"},
		{Operation: core.StopSession}, {Operation: core.Open, Text: "x"}, {Operation: "other"}} {
		if b, err := c.Encode("r", req); err == nil || b != nil {
			t.Fatal(req)
		}
	}
	for _, id := range []string{"", "\xff", strings.Repeat("a", core.MaxFrameBytes)} {
		if b, err := c.StartThread(id); b != nil || err == nil {
			t.Fatal("accepted invalid id")
		}
	}
	observe(t, c, f[0])
	if b, err := c.Encode("r", core.Request{Operation: core.Message, Text: "\x00"}); b != nil || err == nil {
		t.Fatal(err)
	}
	observe(t, c, f[1], core.Event{Kind: "state", State: "busy"})
	if b, err := c.Encode("r", core.Request{Operation: core.Message, Text: "text"}); b != nil || err == nil {
		t.Fatal("implicit steer")
	}
}

func TestCloseActiveTurn(t *testing.T) {
	c, req := active(t)
	observe(t, c, req, core.Event{Kind: "question", Text: "Permission requested"})
	g, err := c.DecodeGrant(req, frames(t, "grants")[0])
	if err != nil {
		t.Fatal(err)
	}
	observe(t, c, frames(t, "events")[5], core.Event{Kind: "ended"})
	if b, err := c.Steer("r", "thread-a", "turn-a", "text"); b != nil || !errors.Is(err, protocol.ErrTurn) {
		t.Errorf("steer after close: %s, %v", b, err)
	}
	if b, err := c.EncodeGrant(req, g); b != nil || !errors.Is(err, protocol.ErrTurn) {
		t.Errorf("grant after close: %s, %v", b, err)
	}
}

func TestForeignThread(t *testing.T) {
	f := frames(t, "events")
	for _, tc := range []struct {
		name  string
		frame []byte
	}{
		{"thread started", f[0]},
		{"turn started", bytes.ReplaceAll(f[1], []byte("turn-a"), []byte("turn-b"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := active(t)
			observe(t, c, bytes.ReplaceAll(tc.frame, []byte("thread-a"), []byte("thread-b")))
			if b, err := c.Steer("r", "thread-a", "turn-a", "text"); b == nil || err != nil {
				t.Fatal("lost original turn", err)
			}
			for _, thread := range []string{"thread-a", "thread-b"} {
				if b, err := c.Steer("r", thread, "turn-b", "text"); b != nil || !errors.Is(err, protocol.ErrTurn) {
					t.Fatal("steered foreign turn", thread, err)
				}
			}
		})
	}
}

func TestStaleDelta(t *testing.T) {
	c, _ := active(t)
	observe(t, c, bytes.ReplaceAll(frames(t, "events")[2], []byte("turn-a"), []byte("old")))
}

func TestStartedTurnRequiresInProgress(t *testing.T) {
	c, _ := active(t)
	frame := bytes.ReplaceAll(frames(t, "events")[1], []byte("inProgress"), []byte("completed"))
	if d, err := c.Decode(frame); !errors.Is(err, core.ErrInvalidFrame) || !reflect.DeepEqual(d, core.Decoded{}) {
		t.Fatal(d, err)
	}
}

func TestThreadStatus(t *testing.T) {
	for _, tc := range []struct {
		status       string
		want         core.Event
		steer, start bool
	}{
		{`{"type":"active","activeFlags":[]}`, core.Event{Kind: "state", State: "busy"}, true, false},
		{`{"type":"active","activeFlags":["waitingOnApproval"]}`, core.Event{Kind: "state", State: "blocked"}, true, false},
		{`{"type":"active","activeFlags":["waitingOnUserInput"]}`, core.Event{Kind: "state", State: "asking"}, true, false},
		{`{"type":"idle"}`, core.Event{Kind: "state", State: "idle"}, false, true},
		{`{"type":"notLoaded"}`, core.Event{Kind: "state", State: "unknown"}, false, false},
		{`{"type":"systemError"}`, core.Event{Kind: "state", State: "unknown"}, false, false},
	} {
		c, _ := active(t)
		observe(t, c, []byte(`{"method":"thread/status/changed","params":{"threadId":"thread-a","status":`+tc.status+`}}`), tc.want)
		_, err := c.Steer("r", "thread-a", "turn-a", "x")
		if (err == nil) != tc.steer {
			t.Fatal(tc, err)
		}
		_, err = c.Encode("r", core.Request{Operation: core.Message, Text: "x"})
		if (err == nil) != tc.start {
			t.Fatal(tc, err)
		}
	}
}

func TestEnvelope(t *testing.T) {
	c := fresh(t)
	for _, raw := range []string{`{"id":"r","result":{}}`, `{"id":"r","error":{"code":-1,"message":"stale"}}`} {
		d, err := c.Decode([]byte(raw))
		if err != nil || d.AnswerTo == nil || *d.AnswerTo != "r" {
			t.Fatal(raw, d, err)
		}
		if strings.Contains(raw, "error") && !reflect.DeepEqual(d.Events, []core.Event{{Kind: "progress", Text: "Request failed: stale"}}) {
			t.Fatal(d)
		}
	}
	observe(t, c, []byte(`{"id":"r","method":"other"}`))
	for _, raw := range []string{"", "{}", "null", "[]", "{}\n{}", strings.Repeat("a", core.MaxFrameBytes),
		`{"method":"x","emittedAtMs":0.5}`, `{"id":7,"method":"x","emittedAtMs":1}`, `{"method":"x","emittedAtMs":null}`,
		`{"id":1,"result":{}}`, `{"id":"r","result":null}`, `{"id":"r","result":{},"error":{}}`, `{"id":"r","result":{},"x":1}`,
		`{"id":"r","error":{"code":1}}`, `{"id":"r","error":{"code":1.2,"message":"x"}}`,
		`{"id":"r","method":"x","result":{}}`, `{"method":"","id":"r"}`, `{"id":1.1,"method":"other"}`,
		`{"id":"r","result":{"a":1,"a":2}}`, `{"method":"x","params":false}`, `{"method":"x","params":{"x":"\ud800"}}`,
		`{"id":3,"method":"thread/started","params":{}}`, `{"method":"thread/started","params":{}}`,
		`{"method":"turn/completed","params":{}}`, `{"id":"r","result":{},"params":{}}`} {
		if d, err := c.Decode([]byte(raw)); err == nil || !reflect.DeepEqual(d, core.Decoded{}) {
			t.Fatal(raw, d, err)
		}
	}
}
