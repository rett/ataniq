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
	"ataniq/internal/pack/packtest"
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

const startAnswer = `{"id":"start","result":{"thread":{"id":"thread-a","status":{"type":"idle"}}}}`

func acceptStart(t *testing.T, c protocol.Codec) {
	t.Helper()
	d, err := c.Decode([]byte(startAnswer))
	if err != nil || d.AnswerTo == nil || *d.AnswerTo != "start" || len(d.Events) != 0 {
		t.Fatal(d, err)
	}
}

func startThread(t *testing.T, c protocol.Codec) {
	t.Helper()
	if _, err := c.StartThread("start"); err != nil {
		t.Fatal(err)
	}
	acceptStart(t, c)
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
	b, err := c.StartThread("start")
	if err != nil {
		t.Fatal(err)
	}
	equalJSON(t, b, `{"method":"thread/start","id":"start","params":{"approvalPolicy":"on-request","sandbox":"read-only"}}`)
	observe(t, c, f[0])
	acceptStart(t, c)
	send(core.Message, "a\nb", `{"method":"turn/start","id":"req","params":{"threadId":"thread-a","input":[{"type":"text","text":"a\nb"}]}}`)
	observe(t, c, f[1], core.Event{Kind: "state", State: "busy"})
	id = "steer"
	b, err = c.Steer(id, "thread-a", "turn-a", "extra")
	if err != nil {
		t.Fatal(err)
	}
	equalJSON(t, b, `{"id":"steer","method":"turn/steer","params":{"threadId":"thread-a","expectedTurnId":"turn-a","input":[{"type":"text","text":"extra"}]}}`)
	id = "stop"
	send(core.StopTurn, "", `{"method":"turn/interrupt","id":"stop","params":{"threadId":"thread-a","turnId":"turn-a"}}`)
	observe(t, c, f[2], core.Event{Kind: "progress", Text: "two words"})
	observe(t, c, f[3], core.Event{Kind: "question", Text: "Permission requested"})
	observe(t, c, f[4], core.Event{Kind: "state", State: "idle"})
	observe(t, c, f[5], core.Event{Kind: "ended"})
}

func sendMessage(t *testing.T, c protocol.Codec, id string) {
	t.Helper()
	if _, err := c.Encode(id, core.Request{Operation: core.Message, Text: "hello"}); err != nil {
		t.Fatal(err)
	}
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
	startThread(t, c)
	observe(t, c, f[0])
	sendMessage(t, c, "first")
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
	startThread(t, c)
	observe(t, c, f[0])
	if b, err := c.Encode("r", core.Request{Operation: core.Message, Text: "\x00"}); b != nil || err == nil {
		t.Fatal(err)
	}
	observe(t, c, f[1], core.Event{Kind: "state", State: "busy"})
	if b, err := c.Encode("r", core.Request{Operation: core.Message, Text: "text"}); b != nil || err == nil {
		t.Fatal("implicit steer")
	}
}

func TestThreadStartBinding(t *testing.T) {
	c, f := fresh(t), frames(t, "events")
	if _, err := c.StartThread("start"); err != nil {
		t.Fatal(err)
	}
	observe(t, c, bytes.ReplaceAll(f[0], []byte("thread-a"), []byte("thread-b")))
	if _, err := c.Decode(bytes.ReplaceAll([]byte(startAnswer), []byte(`"start"`), []byte(`"unrelated"`))); err != nil {
		t.Fatal(err)
	}
	message := core.Request{Operation: core.Message, Text: "hello"}
	if b, err := c.Encode("r", message); b != nil || !errors.Is(err, protocol.ErrTurn) {
		t.Fatal("dispatched before matching response", string(b), err)
	}
	if b, err := c.StartThread("other"); b != nil || err == nil {
		t.Fatal("replaced pending start")
	}
	acceptStart(t, c)
	sendMessage(t, c, "first")
	observe(t, c, f[0])
	observe(t, c, f[1], core.Event{Kind: "state", State: "busy"})
	if b, err := c.Steer("r", "thread-a", "turn-a", "text"); b == nil || err != nil {
		t.Fatal("did not bind response thread", err)
	}
}

func TestThreadStartFailure(t *testing.T) {
	c := fresh(t)
	if _, err := c.StartThread("abandoned"); err != nil {
		t.Fatal(err)
	}
	c.Forget("abandoned")
	if _, err := c.StartThread("start"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Decode([]byte(`{"id":"start","result":{}}`)); !errors.Is(err, core.ErrInvalidFrame) {
		t.Fatal("accepted missing thread", err)
	}
	if _, err := c.Decode([]byte(`{"id":"start","error":{"code":-1,"message":"failed"}}`)); err != nil {
		t.Fatal(err)
	}
	startThread(t, c)
}

func TestOutstandingStartIDsCannotBeReused(t *testing.T) {
	c, f := fresh(t), frames(t, "events")
	refused := func(b []byte, err error) {
		t.Helper()
		if b != nil || !errors.Is(err, protocol.ErrTurn) {
			t.Fatalf("got %s, %v; want ErrTurn", b, err)
		}
	}
	if _, err := c.StartThread("start"); err != nil {
		t.Fatal(err)
	}
	refused(c.Encode("start", core.Request{Operation: core.Open}))
	acceptStart(t, c)
	sendMessage(t, c, "first")
	observe(t, c, f[1], core.Event{Kind: "state", State: "busy"})
	c.Forget("first")
	refused(c.Steer("first", "thread-a", "turn-a", "text"))
	refused(c.StartThread("first"))
	refused(c.Encode("first", core.Request{Operation: core.Open}))
	refused(c.Encode("first", core.Request{Operation: core.StopTurn}))

	// An error for the steer must leave the message awaiting its own answer.
	if _, err := c.Steer("steer", "thread-a", "turn-a", "text"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Decode([]byte(`{"id":"steer","error":{"code":-1,"message":"failed"}}`)); err != nil {
		t.Fatal(err)
	}
	observe(t, c, f[4], core.Event{Kind: "state", State: "idle"})
	refused(c.Encode("second", core.Request{Operation: core.Message, Text: "text"}))
	if _, err := c.Decode([]byte(turnStartAnswer)); err != nil {
		t.Fatal(err)
	}
	sendMessage(t, c, "second")
}

func TestTurnStartConsumesIdle(t *testing.T) {
	c := fresh(t)
	startThread(t, c)
	observe(t, c, frames(t, "events")[0])
	message := core.Request{Operation: core.Message, Text: "hello"}
	if b, err := c.Encode("", message); b != nil || err == nil {
		t.Fatal("encoded invalid id")
	}
	if b, err := c.Encode("r", message); b == nil || err != nil {
		t.Fatal("failed encode consumed idle", err)
	}
	if b, err := c.Encode("s", message); b != nil || !errors.Is(err, protocol.ErrTurn) {
		t.Fatal("encoded a second turn/start", string(b), err)
	}
}

const idleStatus = `{"method":"thread/status/changed","params":{"threadId":"thread-a","status":{"type":"idle"}}}`
const turnStartAnswer = `{"id":"first","result":{"turn":{"id":"turn-a"}}}`
const turnStartError = `{"id":"first","error":{"code":-1,"message":"failed"}}`

func TestAcceptedTurnStartSurvivesStaleIdle(t *testing.T) {
	c, f := fresh(t), frames(t, "events")
	startThread(t, c)
	msg := core.Request{Operation: core.Message, Text: "hello"}
	if _, err := c.Encode("first", msg); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Decode([]byte(turnStartAnswer)); err != nil {
		t.Fatal(err)
	}
	stop := func() {
		t.Helper()
		frame, err := c.Encode("stop", core.Request{Operation: core.StopTurn})
		if err != nil {
			t.Fatal(err)
		}
		equalJSON(t, frame, `{"method":"turn/interrupt","id":"stop","params":{"threadId":"thread-a","turnId":"turn-a"}}`)
	}
	stop()
	observe(t, c, f[1], core.Event{Kind: "state", State: "busy"})
	observe(t, c, []byte(idleStatus))
	observe(t, c, f[2], core.Event{Kind: "progress", Text: "two words"})
	stop()
	if _, err := c.Encode("second", msg); !errors.Is(err, protocol.ErrTurn) {
		t.Fatalf("stale idle after success permitted a retry: %v", err)
	}
	observe(t, c, []byte(`{"method":"thread/status/changed","params":{"threadId":"thread-a","status":{"type":"notLoaded"}}}`), core.Event{Kind: "state", State: "unknown"})
	stop()
}

func TestTurnStartSurvivesIdleObservation(t *testing.T) {
	f := frames(t, "events")
	for _, afterTurn := range []bool{false, true} {
		t.Run(fmt.Sprintf("afterTurn=%t", afterTurn), func(t *testing.T) {
			c := fresh(t)
			startThread(t, c)
			if afterTurn {
				observe(t, c, f[1], core.Event{Kind: "state", State: "busy"})
				observe(t, c, f[4], core.Event{Kind: "state", State: "idle"})
			}
			msg := core.Request{Operation: core.Message, Text: "hello"}
			if _, err := c.Encode("first", msg); err != nil {
				t.Fatal(err)
			}
			observe(t, c, []byte(idleStatus), core.Event{Kind: "state", State: "idle"})
			if b, err := c.Encode("second", msg); b != nil || !errors.Is(err, protocol.ErrTurn) {
				t.Fatalf("idle notification released pending start: %s, %v", b, err)
			}
		})
	}
}

func TestTurnStartSurvivesUnrelatedCompletion(t *testing.T) {
	c := fresh(t)
	startThread(t, c)
	msg := core.Request{Operation: core.Message, Text: "hello"}
	if _, err := c.Encode("first", msg); err != nil {
		t.Fatal(err)
	}
	observe(t, c, frames(t, "events")[4])
	if b, err := c.Encode("second", msg); b != nil || !errors.Is(err, protocol.ErrTurn) {
		t.Fatalf("unrelated completion released pending start: %s, %v", b, err)
	}
	observe(t, c, []byte(`{"method":"thread/status/changed","params":{"threadId":"thread-a","status":{"type":"active","activeFlags":[]}}}`), core.Event{Kind: "state", State: "busy"})
	if _, err := c.Decode([]byte(turnStartError)); err != nil {
		t.Fatal(err)
	}
	if b, err := c.Encode("retry", msg); b != nil || !errors.Is(err, protocol.ErrTurn) {
		t.Fatalf("rejected start permitted a retry while active: %s, %v", b, err)
	}
	observe(t, c, []byte(idleStatus), core.Event{Kind: "state", State: "idle"})
	if _, err := c.Encode("retry", msg); err != nil {
		t.Fatalf("retry after idle: %v", err)
	}
}

func TestRejectedTurnStartPreservesObservedTurn(t *testing.T) {
	c, f := fresh(t), frames(t, "events")
	startThread(t, c)
	msg := core.Request{Operation: core.Message, Text: "hello"}
	if _, err := c.Encode("first", msg); err != nil {
		t.Fatal(err)
	}
	turnB := func(frame []byte) []byte {
		return bytes.ReplaceAll(frame, []byte("turn-a"), []byte("turn-b"))
	}
	observe(t, c, turnB(f[1]), core.Event{Kind: "state", State: "busy"})
	if _, err := c.Decode([]byte(turnStartError)); err != nil {
		t.Fatal(err)
	}
	if b, err := c.Encode("second", msg); b != nil || !errors.Is(err, protocol.ErrTurn) {
		t.Fatalf("rejected start permitted a second message during turn-b: %s, %v", b, err)
	}
	observe(t, c, turnB(f[2]), core.Event{Kind: "progress", Text: "two words"})
	frame, err := c.Encode("stop", core.Request{Operation: core.StopTurn})
	if err != nil {
		t.Fatal(err)
	}
	equalJSON(t, frame, `{"method":"turn/interrupt","id":"stop","params":{"threadId":"thread-a","turnId":"turn-b"}}`)
}

func TestOverlappingTurnRevokesOutstandingPermission(t *testing.T) {
	c, f := fresh(t), frames(t, "events")
	startThread(t, c)
	if _, err := c.Encode("first", core.Request{Operation: core.Message, Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	turnB := func(frame []byte) []byte {
		return bytes.ReplaceAll(frame, []byte("turn-a"), []byte("turn-b"))
	}
	observe(t, c, turnB(f[1]), core.Event{Kind: "state", State: "busy"})
	req := turnB(f[3])
	observePermission(t, c, req)
	if _, err := c.Decode([]byte(turnStartAnswer)); err != nil {
		t.Fatal(err)
	}
	g := protocol.Grant{Scope: "turn", Permissions: &protocol.Permissions{}}
	if _, err := c.DecodeGrant(req, frames(t, "grants")[0]); !errors.Is(err, protocol.ErrTurn) {
		t.Fatalf("decoded permission for turn-b: %v", err)
	}
	if frame, err := c.EncodeGrant(req, g); frame != nil || !errors.Is(err, protocol.ErrTurn) {
		t.Fatalf("encoded permission for turn-b: %s, %v", frame, err)
	}
	wantPermissionDecline(t, c, req, "7")
	observePermission(t, c, f[3]) // the same server ID is available on turn-a
	observe(t, c, f[4])           // only turn-a completed
	wantPermissionDecline(t, c, req, "7")
}

type sessionPack struct {
	pack.Pack
	codec protocol.Codec
}

func (p sessionPack) ProtocolCodec(string) (core.Codec, bool) { return p.codec, true }

func TestTurnStartRetry(t *testing.T) {
	for _, abandoned := range []bool{false, true} {
		t.Run(fmt.Sprintf("abandoned=%t", abandoned), func(t *testing.T) {
			c := fresh(t)
			startThread(t, c)
			s, err := core.From(sessionPack{packtest.Beta, c}, "1.0")
			if err != nil {
				t.Fatal(err)
			}
			msg := core.Request{Operation: core.Message, Text: "hello"}
			first, _, err := s.Encode(msg)
			if err != nil {
				t.Fatal(err)
			}
			rejected := []byte(fmt.Sprintf("{\"id\":%q,\"error\":{\"code\":-1,\"message\":\"busy\"}}\n", first))
			if abandoned {
				s.Forget(first)
				if _, _, err := s.Encode(msg); !errors.Is(err, protocol.ErrTurn) {
					t.Fatalf("forgotten start permitted a retry: %v", err)
				}
				if _, err := s.Decode(rejected); !errors.Is(err, core.ErrUnknownAnswer) {
					t.Fatalf("forgotten answer: %v", err)
				}
			} else if _, err := s.Decode(rejected); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.Encode(msg); err != nil {
				t.Fatalf("second turn/start: %v", err)
			}
			s.Forget(first)
			if _, err := s.Decode(rejected); !errors.Is(err, core.ErrUnknownAnswer) {
				t.Fatalf("late answer: %v", err)
			}
			if _, _, err := s.Encode(msg); !errors.Is(err, protocol.ErrTurn) {
				t.Fatalf("late answer released second turn/start: %v", err)
			}
		})
	}
}

func TestForgottenTurnStartCompletes(t *testing.T) {
	c, f := fresh(t), frames(t, "events")
	startThread(t, c)
	msg := core.Request{Operation: core.Message, Text: "hello"}
	if _, err := c.Encode("first", msg); err != nil {
		t.Fatal(err)
	}
	c.Forget("first")
	observe(t, c, f[1], core.Event{Kind: "state", State: "busy"})
	observe(t, c, f[4], core.Event{Kind: "state", State: "idle"})
	if _, err := c.Encode("second", msg); !errors.Is(err, protocol.ErrTurn) {
		t.Fatalf("unmatched completion permitted a retry: %v", err)
	}
	if _, err := c.Decode([]byte(turnStartAnswer)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Encode("second", msg); err != nil {
		t.Fatalf("completed turn prevented a retry: %v", err)
	}
}

func TestLateStartAnswerPreservesBusyThread(t *testing.T) {
	f := frames(t, "events")
	checkBlocked := func(t *testing.T, observations ...[]byte) protocol.Codec {
		t.Helper()
		c := fresh(t)
		startThread(t, c)
		msg := core.Request{Operation: core.Message, Text: "hello"}
		if _, err := c.Encode("first", msg); err != nil {
			t.Fatal(err)
		}
		for _, frame := range observations {
			if _, err := c.Decode(frame); err != nil {
				t.Fatal(err)
			}
		}
		d, err := c.Decode([]byte(turnStartAnswer))
		if err != nil || d.AnswerTo == nil || *d.AnswerTo != "first" || len(d.Events) != 0 {
			t.Fatal(d, err)
		}
		if frame, err := c.Encode("second", msg); frame != nil || !errors.Is(err, protocol.ErrTurn) {
			t.Fatalf("late answer permitted a second message: %s, %v", frame, err)
		}
		return c
	}
	t.Run("started b", func(t *testing.T) {
		turnB := func(frame []byte) []byte {
			return bytes.ReplaceAll(frame, []byte("turn-a"), []byte("turn-b"))
		}
		c := checkBlocked(t, f[1], f[4], turnB(f[1]))
		observe(t, c, turnB(f[2]), core.Event{Kind: "progress", Text: "two words"})
		frame, err := c.Encode("stop", core.Request{Operation: core.StopTurn})
		if err != nil {
			t.Fatal(err)
		}
		equalJSON(t, frame, `{"method":"turn/interrupt","id":"stop","params":{"threadId":"thread-a","turnId":"turn-b"}}`)
	})
	t.Run("status active", func(t *testing.T) {
		checkBlocked(t, f[4], []byte(`{"method":"thread/status/changed","params":{"threadId":"thread-a","status":{"type":"active","activeFlags":[]}}}`))
	})
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

func TestTurnStartAnswerRequiresTurnID(t *testing.T) {
	c := fresh(t)
	startThread(t, c)
	msg := core.Request{Operation: core.Message, Text: "hello"}
	if _, err := c.Encode("first", msg); err != nil {
		t.Fatal(err)
	}
	for _, result := range []string{`{}`, `{"turn":null}`, `{"turn":{"id":""}}`, `{"turn":{"id":1}}`} {
		if _, err := c.Decode([]byte(`{"id":"first","result":` + result + `}`)); !errors.Is(err, core.ErrInvalidFrame) {
			t.Fatalf("invalid turn/start result %s: %v", result, err)
		}
	}
	observe(t, c, []byte(idleStatus), core.Event{Kind: "state", State: "idle"})
	if _, err := c.Encode("second", msg); !errors.Is(err, protocol.ErrTurn) {
		t.Fatalf("malformed success released the reservation: %v", err)
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

func TestSessionEscapesEventControls(t *testing.T) {
	c, _ := active(t)
	s, err := core.From(sessionPack{packtest.Beta, c}, "1.0")
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := s.Encode(core.Request{Operation: core.StopTurn})
	if err != nil {
		t.Fatal(err)
	}
	const controls = `\u001b]52;c;QQ==\u0007\r`
	const escaped = `\x1b]52;c;QQ==\a\r`
	for _, tc := range []struct {
		name, frame, want string
		answer            *string
	}{
		{"message delta", strings.Replace(string(frames(t, "events")[2]), "two words", controls, 1), escaped, nil},
		{"request error", fmt.Sprintf(`{"id":%q,"error":{"code":-1,"message":"%s"}}`, id, controls), "Request failed: " + escaped, &id},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := s.Decode([]byte(tc.frame + "\n"))
			want := core.Decoded{AnswerTo: tc.answer, Events: []core.Event{{Kind: "progress", Text: tc.want}}}
			if err != nil || !reflect.DeepEqual(d, want) {
				t.Fatalf("answer %v, events %q, error %v", d.AnswerTo, d.Events, err)
			}
		})
	}
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
		status string
		want   []core.Event
		steer  bool
	}{
		{`{"type":"active","activeFlags":[]}`, []core.Event{{Kind: "state", State: "busy"}}, true},
		{`{"type":"active","activeFlags":["waitingOnApproval"]}`, []core.Event{{Kind: "state", State: "blocked"}}, true},
		{`{"type":"active","activeFlags":["waitingOnUserInput"]}`, []core.Event{{Kind: "state", State: "asking"}}, true},
		{`{"type":"idle"}`, nil, true},
		{`{"type":"notLoaded"}`, []core.Event{{Kind: "state", State: "unknown"}}, true},
		{`{"type":"systemError"}`, []core.Event{{Kind: "state", State: "unknown"}}, false},
	} {
		c, _ := active(t)
		observe(t, c, []byte(`{"method":"thread/status/changed","params":{"threadId":"thread-a","status":`+tc.status+`}}`), tc.want...)
		_, err := c.Steer("r", "thread-a", "turn-a", "x")
		if (err == nil) != tc.steer {
			t.Fatal(tc, err)
		}
		_, err = c.Encode("r", core.Request{Operation: core.Message, Text: "x"})
		if !errors.Is(err, protocol.ErrTurn) {
			t.Fatal(tc, err)
		}
	}
}

func TestActiveStatusPreventsMessageOnIdleThread(t *testing.T) {
	c := fresh(t)
	startThread(t, c)
	observe(t, c, []byte(`{"method":"thread/status/changed","params":{"threadId":"thread-a","status":{"type":"active","activeFlags":[]}}}`), core.Event{Kind: "state", State: "busy"})
	if _, err := c.Encode("first", core.Request{Operation: core.Message, Text: "hello"}); !errors.Is(err, protocol.ErrTurn) {
		t.Fatalf("active thread accepted message: %v", err)
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
	observe(t, c, []byte(`{"method":"other"}`))
	for _, raw := range []string{
		`{"method":"other","result":{}}`,
		`{"method":"other","error":{"code":1,"message":"bad"}}`,
		`{"id":7,"method":"other","emittedAtMs":1}`,
	} {
		if d, err := c.Decode([]byte(raw)); !errors.Is(err, core.ErrInvalidFrame) || !reflect.DeepEqual(d, core.Decoded{}) {
			t.Errorf("%s: got %+v, %v", raw, d, err)
		}
	}
	for _, raw := range []string{"", "{}", "null", "[]", "{}\n{}", strings.Repeat("a", core.MaxFrameBytes),
		`{"method":"x","emittedAtMs":0.5}`, `{"method":"x","emittedAtMs":null}`,
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
