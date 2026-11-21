package consoleclient

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
	"ataniq/internal/clientapi"
)

// The tests of the owner client talk to the client protocol's own server on
// a private socket in a temporary folder, with the owner channel's operations
// registered through the extension seam and answered by a fake daemon. A few
// need a peer that no real server can be (one that sends text the server
// refuses to send, or another protocol); those use rawPeer. Nothing here
// starts a unit, a namespace or an agent.

// fakeCall is one request that reached the fake daemon.
type fakeCall struct {
	op     string
	params string
	// conn is the number of the server run (a restart makes a new one).
	run int
}

// answer is a handler's reply: a result, or a refusal.
type answer func(params json.RawMessage) (any, *clientapi.ErrorBody)

// ownerFake is a daemon with an owner channel. It implements
// clientapi.Handler and clientapi.Extension.
type ownerFake struct {
	t   *testing.T
	dir string
	// ops are the operations it registers; none registers no extension.
	ops  []string
	opts clientapi.Options

	mu       sync.Mutex
	calls    []fakeCall
	handlers map[string]answer
	run      int
	cancel   context.CancelFunc
	done     chan error
	counters *clientapi.Counters
}

func (f *ownerFake) Hello(context.Context) clientapi.HelloResult {
	return clientapi.HelloResult{Version: "0.0.0-test"}
}

func (f *ownerFake) Status(context.Context) (clientapi.StatusResult, error) {
	return clientapi.StatusResult{}, nil
}

func (f *ownerFake) Operations() []string { return f.ops }
func (f *ownerFake) Mutating() bool       { return true }

func (f *ownerFake) Handle(_ context.Context, op string, params json.RawMessage) (any, *clientapi.ErrorBody) {
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{op: op, params: string(params), run: f.run})
	h := f.handlers[op]
	f.mu.Unlock()
	if h == nil {
		return nil, &clientapi.ErrorBody{Code: "internal", Message: "no handler in the test"}
	}
	return h(params)
}

// startOwner starts a fake daemon that registers ops (all of the owner
// channel's when none are given), with the decider answer "yes" and no other
// handler: a test sets its own with on.
func startOwner(t *testing.T, ops ...string) *ownerFake {
	t.Helper()
	if len(ops) == 0 {
		ops = ownerwire.Operations()
	}
	return startOwnerWith(t, clientapi.Options{}, ops)
}

// startOwnerWith is startOwner with server options; no ops starts a daemon
// with no owner channel.
func startOwnerWith(t *testing.T, opts clientapi.Options, ops []string) *ownerFake {
	t.Helper()
	f := &ownerFake{t: t, dir: socketDir(t), ops: ops, opts: opts, handlers: map[string]answer{}, counters: &clientapi.Counters{}}
	f.on(ownerwire.OpDecider, func(json.RawMessage) (any, *clientapi.ErrorBody) {
		return json.RawMessage(mustEncode(t)(ownerwire.EncodeDeciderResult(ownerwire.DeciderResult{Decider: true}))), nil
	})
	f.start()
	t.Cleanup(f.stop)
	return f
}

// start listens and serves on the folder.
func (f *ownerFake) start() {
	f.t.Helper()
	l, _, err := clientapi.Listen(f.dir)
	if err != nil {
		f.t.Fatal(err)
	}
	opts := f.opts
	opts.Counters = f.counters
	if len(f.ops) > 0 {
		opts.Extra = []clientapi.Extension{f}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	f.mu.Lock()
	f.run++
	f.cancel, f.done = cancel, done
	f.mu.Unlock()
	go func() { done <- clientapi.Serve(ctx, l, f, opts) }()
}

// stop ends the server and its connections. It may be called twice.
func (f *ownerFake) stop() {
	f.mu.Lock()
	cancel, done := f.cancel, f.done
	f.cancel = nil
	f.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		f.t.Error("Serve did not return after its context ended")
	}
}

// restart ends every connection by ending the server, and starts it again on
// the same folder, as a daemon that restarted.
func (f *ownerFake) restart() {
	f.t.Helper()
	f.stop()
	f.start()
}

// on sets the answer of an operation.
func (f *ownerFake) on(op string, h answer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[op] = h
}

// onResult sets an operation to answer with an encoded payload.
func (f *ownerFake) onResult(op string, payload []byte) {
	f.on(op, func(json.RawMessage) (any, *clientapi.ErrorBody) { return json.RawMessage(payload), nil })
}

// refuse sets an operation to answer with an error response.
func (f *ownerFake) refuse(op string, code clientapi.ErrorCode, message string) {
	f.on(op, func(json.RawMessage) (any, *clientapi.ErrorBody) {
		return nil, &clientapi.ErrorBody{Code: code, Message: message}
	})
}

// sent returns the requests that reached the daemon.
func (f *ownerFake) sent() []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeCall(nil), f.calls...)
}

// count returns how many requests of op reached the daemon.
func (f *ownerFake) count(op string) int {
	n := 0
	for _, c := range f.sent() {
		if c.op == op {
			n++
		}
	}
	return n
}

// of returns the requests of op, in order.
func (f *ownerFake) of(op string) []fakeCall {
	var out []fakeCall
	for _, c := range f.sent() {
		if c.op == op {
			out = append(out, c)
		}
	}
	return out
}

// mustEncode returns a function that passes on an encoded payload, failing
// the test when the wire package refused to write it.
func mustEncode(t *testing.T) func(b []byte, err error) []byte {
	return func(b []byte, err error) []byte {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
}

// newOwner makes an Owner for the daemon's folder with a short bound, and
// closes it when the test ends.
func newOwner(t *testing.T, dir string) *Owner {
	t.Helper()
	o, err := NewOwner(OwnerConfig{Dir: dir, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { o.Close() })
	return o
}

// rawPeer is a listener that speaks the wire by hand: for the peers the real
// server cannot be. answer returns the bytes to write for a request, nil to
// write nothing, and an empty slice to hang up.
type rawPeer struct {
	dir string
	mu  sync.Mutex
	ops []string
}

func startRawPeer(t *testing.T, answer func(req clientapi.Request) []byte) *rawPeer {
	t.Helper()
	p := &rawPeer{dir: socketDir(t)}
	if err := os.MkdirAll(p.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", clientapi.SocketPath(p.dir))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var connsMu sync.Mutex
	var conns []net.Conn
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			connsMu.Lock()
			conns = append(conns, c)
			connsMu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer c.Close()
				br := bufio.NewReader(c)
				for {
					line, err := br.ReadBytes('\n')
					if err != nil {
						return
					}
					var req clientapi.Request
					if json.Unmarshal(line, &req) != nil {
						return
					}
					p.mu.Lock()
					p.ops = append(p.ops, req.Op)
					p.mu.Unlock()
					out := answer(req)
					if out == nil {
						continue
					}
					if len(out) == 0 {
						return
					}
					if _, err := c.Write(out); err != nil {
						return
					}
				}
			}()
		}
	}()
	t.Cleanup(func() {
		l.Close()
		connsMu.Lock()
		for _, c := range conns {
			c.Close()
		}
		connsMu.Unlock()
		wg.Wait()
	})
	return p
}

func (p *rawPeer) sent() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.ops...)
}

// ownerHello is the hello result of a peer that has the owner channel.
func ownerHello() clientapi.HelloResult {
	return clientapi.HelloResult{
		Protocols:  clientapi.SupportedProtocols(),
		Operations: append(clientapi.Operations(), ownerwire.Operations()...),
		Version:    "0.0.0-test",
	}
}

// A mark in text that a hostile peer sends and the client must not repeat or
// show raw: a direction override and an escape sequence.
const hostileMark = "\u202eEVIL\u200b\u2066"

// record fixtures ------------------------------------------------------

func testChange(path, status string, added, removed int64) record.PathChange {
	old := "100644"
	if status == "A" {
		old = "000000"
	}
	return record.PathChange{Path: record.Text(path), Status: status, OldMode: old, NewMode: "100644", Added: added, Removed: removed, NewBytes: added * 30}
}

func testOID(seed string) string { return record.Digest([]byte(seed))[:40] }

// testCard is a pending record, as the daemon would send it.
type testCard struct {
	p      record.Pending
	data   []byte
	review string
}

// newTestCard writes a valid pending record for id "ap-"+slug of a feature
// finish, holding the first cut of paths (all when cut is 0). The review
// digest is made from the slug.
func newTestCard(t *testing.T, slug, feature string, asked time.Time, paths []record.PathChange, cut int, flags ...record.Flag) testCard {
	t.Helper()
	target, featureTip, merge := testOID("target "+slug), testOID("feature "+slug), testOID("merge "+slug)
	review := record.Digest([]byte("review " + slug))
	var added, removed, newBytes int64
	for _, p := range paths {
		added += p.Added
		removed += p.Removed
		newBytes += p.NewBytes
	}
	rows := paths
	if cut > 0 && cut < len(rows) {
		rows = rows[:cut]
	}
	plan := "merge feature/" + feature + " into develop --no-ff\nresult built by the host\n"
	suite := record.EvidenceSuite
	p := record.Pending{
		Schema:        record.SchemaPending,
		ID:            "ap-" + slug,
		Planner:       "planner-a",
		RequestID:     "rq-" + slug,
		RequestSHA256: record.Digest([]byte("request " + slug)),
		Binding: record.Binding{
			Project: "project-a",
			Subject: record.Subject{
				Kind: record.SubjectSeat, RoleName: "role-a", Epoch: 7, SeatRev: 12,
				Holder: &record.Holder{Kind: "lead", DisplayID: "role-a@e7", Runtime: "runtime-a"},
			},
			Verb:      record.VerbFeatureFinish,
			Argv:      []string{feature},
			Class:     record.ClassRefMove,
			PlanToken: "h-" + testOID("token " + slug)[:8],
			Effects: []record.Effect{{
				Kind: record.EffectRef, Ref: "refs/heads/develop", Old: target, New: merge,
			}},
			Merge: &record.Merge{Commit: merge, Parents: []string{target, featureTip}, BuiltBy: record.BuiltByHost},
			Summary: &record.Summary{
				ReviewDigest: review, Base: target, Tip: merge,
				Files: int64(len(paths)), Added: added, Removed: removed, NewBytes: newBytes,
				Paths: rows, PathsTruncated: len(rows) < len(paths),
				Flagged: flags,
				ExecConfigSet: record.ExecConfigSet{
					Matcher: "matcher-a", SHA256: record.Digest([]byte("rules")), Additions: 0,
				},
			},
			Evidence: []record.Evidence{{
				Kind: suite, RecordID: "suite-" + slug,
				RecordSHA256: record.Digest([]byte("suite " + slug)),
			}},
			RequestedAt:      record.FormatTime(asked),
			PendingExpiresAt: record.FormatTime(asked.Add(24 * time.Hour)),
		},
		PlanText: record.Text(plan),
		Note:     "a note",
	}
	p.Binding.PlanSHA256 = record.PlanDigest(plan)
	digest, err := record.BindingDigest(p.Binding)
	if err != nil {
		t.Fatal(err)
	}
	p.BindingSHA256 = digest
	data, err := record.EncodePending(p)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := record.DecodePending(data)
	if err != nil {
		t.Fatal(err)
	}
	return testCard{p: decoded, data: data, review: review}
}

// show returns the encoded owner.show answer for the card.
func (c testCard) show(t *testing.T, edit func(*ownerwire.ShowResult)) []byte {
	t.Helper()
	r := ownerwire.ShowResult{Record: ownerwire.NewRecord(c.data)}
	if edit != nil {
		edit(&r)
	}
	return mustEncode(t)(ownerwire.EncodeShowResult(r))
}

// serveCard makes the fake answer owner.show with the card.
func (f *ownerFake) serveCard(c testCard) {
	f.t.Helper()
	f.onResult(ownerwire.OpShow, c.show(f.t, nil))
}

// openOne makes an Owner for a daemon that serves the card and opens it.
func openOne(t *testing.T, f *ownerFake, o *Owner, c testCard) {
	t.Helper()
	f.serveCard(c)
	if _, err := o.Open(c.p.ID); err != nil {
		t.Fatal(err)
	}
}
