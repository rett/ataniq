//go:build linux && ataniq_ownerchannel && ataniq_canary

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"ataniq/internal/approval/gatehost"
	"ataniq/internal/approval/hostadapter"
	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/clientapi"
	"ataniq/internal/leadcanary"
	"ataniq/internal/leadcanary/provision/provisiontest"
	"ataniq/internal/leadchat"
	"ataniq/internal/leaddaemon"
	"ataniq/internal/leadwire"
	"ataniq/internal/seatgate"
)

// These tests run the daemon of a build with both tags as the owner runs it
// for real scratch leads with the seat:
//
//	ataniqd run --scratch CONFIG --owner-channel --seat-gate IP:PORT
//
// in process, on private temporary roots: the scratch world's config, root
// marker, source repository and fake unit manager (the leads are defined, not
// started), the owner channel's non-trial scratch settings in owner/ of the
// config's state root with the real host adapter as the builder (a suite of
// the test's, so that no sandbox is needed), the gate's key made by
// "ataniqd seat-gate init" and one pinned seat key. A seat dials the gate on
// the loopback address with the pinned keys and calls seat.chat as the seat
// link does.

// scratchSeatWorld is one such setup.
type scratchSeatWorld struct {
	w      *provisiontest.ScratchWorld
	addr   string
	gateFP string
	seat   seatgate.Identity
	drop   string
	env    func(string) string
	socket string
}

// newScratchSeatWorld makes the setup. With origin the owner-local seat
// origin is configured too, so the gate also serves login and decide.
func newScratchSeatWorld(t *testing.T, origin bool) *scratchSeatWorld {
	t.Helper()
	w := newScratchWorld(t)
	s := &scratchSeatWorld{w: w, addr: freeLoopback(t), seat: newSeat(t, "seat"), socket: filepath.Join(w.Runtime, "daemon.sock")}
	// The owner channel's scratch folders: one scratch root apart from the
	// scratch config's roots, holding the mirror, the drop folder, the
	// sandbox template and the work folder of the suite.
	base := filepath.Join(w.Dir, "owner-scratch")
	s.drop = filepath.Join(base, "drop")
	for _, d := range []string{base, s.drop, filepath.Join(w.State, hostadapter.OwnerDirName)} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeScratch(t, w.State, base, func(sc *hostadapter.Scratch) {
		sc.Sandbox, sc.Work = filepath.Join(base, "sandbox.json"), filepath.Join(base, "work")
	})
	s.gateFP = gateInit(t, w.State)
	writePins(t, w.State, s.seat.Fingerprint()+"\n", 0o600)
	env := w.Getenv()
	if origin {
		o, _ := writeOrigin(t, "https://seat.example.invalid\n", 0o600)
		s.env = func(k string) string {
			if v, ok := o[k]; ok {
				return v
			}
			return env(k)
		}
	} else {
		s.env = env
	}
	old := ownerInspector
	ownerInspector = ownerCmdTerminal{}
	t.Cleanup(func() { ownerInspector = old })
	return s
}

// start runs the daemon and returns it with its standard error.
func (s *scratchSeatWorld) start(t *testing.T) (*testApp, *gateBuf, <-chan int) {
	t.Helper()
	ta := canaryApp(t)
	ta.getenv = s.env
	errs := &gateBuf{}
	ta.stderr = errs
	done := startApp(t, ta, []string{"--scratch", s.w.ConfigPath, "--owner-channel", "--seat-gate", s.addr}, s.socket)
	return ta, errs, done
}

// stop ends the daemon with SIGTERM and wants exit 0.
func stopApp(t *testing.T, ta *testApp, errs *gateBuf, done <-chan int) {
	t.Helper()
	ta.signals <- syscall.SIGTERM
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit %d: %s", code, errs.String())
		}
	case <-time.After(60 * time.Second):
		t.Fatal("the daemon did not stop in 60 s")
	}
}

// dial connects the pinned seat to the gate.
func (s *scratchSeatWorld) dial(t *testing.T) *seatgate.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := seatgate.Dial(ctx, s.addr, seatgate.ClientConfig{
		TLS:        seatgate.ClientTLS(s.seat, s.gateFP, "gate.example.invalid"),
		Caps:       []string{"pair", "read", "login", "decide", "watch", "genesis", "chat"},
		BackoffMin: 10 * time.Millisecond, BackoffMax: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("the pinned seat: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// define defines the lead of the key on the daemon's socket, as the console
// does.
func (s *scratchSeatWorld) define(t *testing.T, key string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cl, err := clientapi.Dial(ctx, s.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	var r leadwire.Receipt
	if err := cl.Call(ctx, leadwire.OpDefine, leadwire.DefineParams{Client: "test", CommandID: "def-" + key, Role: "scratch-" + key + "/lead"}, &r); err != nil || r.Status != leadwire.StatusDone {
		t.Fatalf("define %s: %+v %v", key, r, err)
	}
}

// outbox is the host path of the lead's reply folder.
func (s *scratchSeatWorld) outbox(key string) string {
	return filepath.Join(s.w.State, "leads", "scratch-"+key+"-lead", "home", leadchat.OutboxName)
}

// chatSet runs "ataniqd chat set KEY SWITCH --config CONFIG" and wants 0.
func (s *scratchSeatWorld) chatSet(t *testing.T, key, sw string) leadwire.ChatLead {
	t.Helper()
	code, out, errs := chatRun(t, "set", key, sw, "--config", s.w.ConfigPath)
	var row leadwire.ChatLead
	if code != 0 || json.Unmarshal([]byte(out), &row) != nil || row.Key != key || row.Chat != sw {
		t.Fatalf("chat set %s %s: %d %q %s", key, sw, code, out, errs)
	}
	return row
}

// chatRow is the lead's row of "ataniqd chat list".
func (s *scratchSeatWorld) chatRow(t *testing.T, key string) leadwire.ChatLead {
	t.Helper()
	code, out, errs := chatRun(t, "list", "--config", s.w.ConfigPath)
	var list leadwire.ChatListResult
	if code != 0 || json.Unmarshal([]byte(out), &list) != nil {
		t.Fatalf("chat list: %d %q %s", code, out, errs)
	}
	for _, r := range list.Leads {
		if r.Key == key {
			return r
		}
	}
	t.Fatalf("chat list has no %s: %+v", key, list)
	return leadwire.ChatLead{}
}

// writeReply writes a reply as a lead does: under a dot-name, then renamed.
func writeReply(t *testing.T, outbox, name, text string) {
	t.Helper()
	tmp := filepath.Join(outbox, "."+name+".tmp")
	if err := os.WriteFile(tmp, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(outbox, name)); err != nil {
		t.Fatal(err)
	}
}

// waitUntil polls cond every 50 ms for at most d.
func waitUntil(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for !cond() {
		if time.Now().After(end) {
			t.Fatalf("%s: not within %s", what, d)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// seatChat sends seat.chat with the params over the gate and returns the
// result as the wire's strict decoder reads it, or the gate's code.
func seatChat(t *testing.T, c *seatgate.Client, params string) (ownerwire.SeatChatResult, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var raw json.RawMessage
	if err := c.Call(ctx, ownerwire.OpSeatChat, json.RawMessage(params), &raw); err != nil {
		var re *seatgate.RemoteError
		if errors.As(err, &re) {
			return ownerwire.SeatChatResult{}, re.Code
		}
		t.Fatalf("seat.chat %s: %v", params, err)
	}
	res, err := ownerwire.DecodeSeatChatResult(raw)
	if err != nil {
		t.Fatalf("seat.chat %s: the result does not decode: %v", params, err)
	}
	return res, ""
}

// The flags that would put the phone trial's samples beside leads are
// refused from the flags alone, before any config or setting is read or
// anything is made: --trial with --scratch and with --canary (whose agent may
// be live), with or without --owner-channel and --seat-gate, at the usage
// exit code like the other refusals of run's flags. An unreadable or missing
// config is never read, so the refusal is the usage's, not the config's.
func TestRunRefusesTheTrialWithLeads(t *testing.T) {
	w := newScratchWorld(t)
	addr := freeLoopback(t)
	unreadable := filepath.Join(w.Dir, "unreadable.json")
	if err := os.WriteFile(unreadable, []byte("{}"), 0); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(w.Dir, "missing.json")
	f := newCanaryFixture(t)
	live := f.cfg
	live.Agent.Kind = leadcanary.AgentLive
	livePath := f.write(t, live)
	for _, args := range [][]string{
		{"run", "--scratch", w.ConfigPath, "--owner-channel", "--trial"},
		{"run", "--owner-channel", "--trial", "--scratch", w.ConfigPath},
		{"run", "--scratch", w.ConfigPath, "--owner-channel", "--trial", "--seat-gate", addr},
		{"run", "--scratch", w.ConfigPath, "--trial"},
		{"run", "--scratch", unreadable, "--owner-channel", "--trial"},
		{"run", "--trial", "--scratch", missing},
		{"run", "--canary", livePath, "--owner-channel", "--trial", "--seat-gate", addr},
		{"run", "--owner-channel", "--trial", "--canary", livePath},
		{"run", "--canary", unreadable, "--owner-channel", "--trial"},
		{"run", "--canary", missing, "--trial"},
	} {
		ta := scratchApp(t, w)
		code := ta.run(args)
		if code != leaddaemon.ExitUsage || ta.err.String() != trialLeadRefusal {
			t.Errorf("%v: exit %d, stderr %q", args, code, ta.err.String())
		}
	}
	if _, err := os.Lstat(w.Runtime); err == nil {
		t.Fatal("a daemon made the runtime root")
	}
	if _, err := os.Lstat(filepath.Join(w.State, hostadapter.OwnerDirName)); err == nil {
		t.Fatal("a daemon made the owner folder")
	}
	f.wantStateRootAsProvisioned(t)
	// The same flags without a mode with leads pass the check: the
	// standalone phone trial, whose sample port the trial's own tests serve
	// end to end (TestTrialRunSubmitsSampleCardsThatTheSeatSees).
	ta := scratchApp(t, w)
	if code := ta.trialCheck(&ownerFlags{set: true, trial: true}, &canaryFlags{}); code != -1 || ta.err.String() != "" {
		t.Fatalf("the standalone trial: %d %q", code, ta.err.String())
	}
	if code := ta.trialCheck(&ownerFlags{set: true}, &canaryFlags{scratchSet: true, scratch: w.ConfigPath}); code != -1 {
		t.Fatalf("scratch without the trial: %d", code)
	}
}

// The usage of a build with both tags names the composed command and that
// --trial is refused with --scratch.
func TestUsageNamesTheScratchSeat(t *testing.T) {
	ta := newTestApp(t, nil)
	if code := ta.run([]string{"--help"}); code != 0 {
		t.Fatal(code)
	}
	got := ta.out.String()
	for _, want := range []string{
		"ataniqd run --scratch CONFIG --owner-channel --seat-gate ADDR",
		"--trial is refused with --scratch",
		"ataniqd chat set KEY read",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the usage lacks %q", want)
		}
	}
}

// One daemon serves the scratch leads and the seat gate: the gate lists chat
// and says, after its start line, that it serves the scratch leads' replies
// (not the trial's STAND-IN samples); the seat sees every configured lead off;
// the desk turns a defined lead's chat on; a reply the lead writes into its
// outbox is read by the seat over the real wiring, unmarked as a sample; the
// desk turns it off and the same read is refused chat-off; and a new daemon on
// the same state shows the lead off again.
func TestScratchSeatServesTheLeadsRepliesReadOnly(t *testing.T) {
	useSuite(t, cmdSuite{})
	s := newScratchSeatWorld(t, false)
	ta, errs, done := s.start(t)

	log := errs.String()
	var start, next string
	lines := strings.Split(log, "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "ataniqd: seat gate: listening on "+s.addr+", key fingerprint "+s.gateFP) && i+1 < len(lines) {
			start, next = l, lines[i+1]
		}
	}
	if !strings.Contains(start, "; it serves pair, read, watch, genesis and chat only: ") ||
		next != "ataniqd: seat gate: "+scratchChatNote {
		t.Fatalf("the gate's start lines are not the scratch chat's:\n%s", log)
	}
	if strings.Contains(log, "STAND-IN sample leads") || strings.Contains(log, "TRIAL") {
		t.Fatalf("the scratch daemon names the trial's samples:\n%s", log)
	}
	if strings.Count(log, "ataniqd: "+ownerBanner+"\n") != 1 {
		t.Fatalf("the owner channel's banner is missing or repeated:\n%s", log)
	}

	c := s.dial(t)
	if got := strings.Join(c.Caps(), " "); !strings.HasSuffix(got, " chat") {
		t.Fatalf("the gate's capabilities: %q", got)
	}
	offRows := []ownerwire.ChatLeadRow{{Lead: "daemon:alpha", Chat: ownerwire.ChatOff}, {Lead: "daemon:beta", Chat: ownerwire.ChatOff}}
	leads, code := seatChat(t, c, `{"verb":"leads"}`)
	if code != "" || leads.StandIn || len(leads.Leads) != 2 || leads.Leads[0] != offRows[0] || leads.Leads[1] != offRows[1] {
		t.Fatalf("leads at start: %+v %q", leads, code)
	}
	if _, code := seatChat(t, c, `{"verb":"read","lead":"daemon:alpha","limit":16}`); code != ownerwire.CodeChatOff {
		t.Fatalf("a read of a lead that is off: %q", code)
	}

	// The owner defines the lead (from the console) and turns its chat on
	// at the desk.
	s.define(t, "alpha")
	if r := s.chatSet(t, "alpha", leadwire.ChatRead); r.Entries != 1 {
		t.Fatalf("the row after read: %+v", r)
	}
	const text = "The branch is rebased.\nTwo tests fail; I am looking at them."
	writeReply(t, s.outbox("alpha"), "0001-status.txt", text)
	waitUntil(t, 20*time.Second, "the reply is taken", func() bool { return s.chatRow(t, "alpha").Entries == 2 })

	leads, code = seatChat(t, c, `{"verb":"leads"}`)
	if code != "" || leads.StandIn || len(leads.Leads) != 2 || leads.Leads[0].Lead != "daemon:alpha" || leads.Leads[0].Chat != ownerwire.ChatRead ||
		leads.Leads[0].State != ownerwire.ChatLeadStopped || leads.Leads[1] != offRows[1] {
		t.Fatalf("leads while alpha is on: %+v %q", leads, code)
	}
	page, code := seatChat(t, c, `{"verb":"read","lead":"daemon:alpha","limit":16}`)
	if code != "" || page.StandIn || page.Lead != "daemon:alpha" || len(page.Entries) != 2 ||
		page.Entries[0].Who != ownerwire.ChatWhoSystem || page.Entries[1].Who != ownerwire.ChatWhoLead || string(page.Entries[1].Text) != text {
		t.Fatalf("the page: %+v %q", page, code)
	}

	// Off at the desk: the same read is refused at once.
	s.chatSet(t, "alpha", leadwire.ChatOff)
	if _, code := seatChat(t, c, `{"verb":"read","lead":"daemon:alpha","limit":16}`); code != ownerwire.CodeChatOff {
		t.Fatalf("a read after off: %q", code)
	}
	// On again, and a restart: the new daemon shows the lead off.
	s.chatSet(t, "alpha", leadwire.ChatRead)
	c.Close()
	stopApp(t, ta, errs, done)

	ta, errs, done = s.start(t)
	c = s.dial(t)
	leads, code = seatChat(t, c, `{"verb":"leads"}`)
	if code != "" || len(leads.Leads) != 2 || leads.Leads[0] != offRows[0] || leads.Leads[1] != offRows[1] {
		t.Fatalf("leads after a restart: %+v %q", leads, code)
	}
	if _, code := seatChat(t, c, `{"verb":"read","lead":"daemon:alpha","limit":16}`); code != ownerwire.CodeChatOff {
		t.Fatalf("a read after a restart: %q", code)
	}
	if r := s.chatRow(t, "alpha"); r.Chat != leadwire.ChatOff {
		t.Fatalf("the desk's row after a restart: %+v", r)
	}
	c.Close()
	stopApp(t, ta, errs, done)
}

// countingService is the real owner channel's service (the host adapter's,
// on the scratch settings), every call counted by the kind of authority it
// carries before it is passed on: decision (the decide path, the logins and
// the presence beat), enrolment (the pairing and the key list), limits, the
// reads and the wait.
type countingService struct {
	inner interface {
		leaddaemon.OwnerService
		gatehost.SeatPort
		gatehost.WaitPort
	}
	mu    sync.Mutex
	calls map[string][]string
}

func (c *countingService) hit(kind, op string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.calls == nil {
		c.calls = map[string][]string{}
	}
	c.calls[kind] = append(c.calls[kind], op)
}

func (c *countingService) seen() map[string][]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string][]string{}
	for k, v := range c.calls {
		out[k] = append([]string(nil), v...)
	}
	return out
}

const (
	kindDecision  = "decision"
	kindEnrolment = "enrolment"
	kindLimits    = "limits"
	kindRead      = "read"
	kindWait      = "wait"
)

func (c *countingService) List(ctx context.Context) (ownerwire.Listing, error) {
	c.hit(kindRead, "list")
	return c.inner.List(ctx)
}
func (c *countingService) Show(ctx context.Context, id string) (ownerwire.ShowResult, error) {
	c.hit(kindRead, "show")
	return c.inner.Show(ctx, id)
}
func (c *countingService) Paths(ctx context.Context, p ownerwire.PageParams) (ownerwire.PathsPage, error) {
	c.hit(kindRead, "paths")
	return c.inner.Paths(ctx, p)
}
func (c *countingService) Patch(ctx context.Context, p ownerwire.PageParams) (ownerwire.PatchPage, error) {
	c.hit(kindRead, "patch")
	return c.inner.Patch(ctx, p)
}
func (c *countingService) Decide(ctx context.Context, src ownerchannel.Source, f ownerwire.DecisionFrame) (ownerwire.DecideResult, error) {
	c.hit(kindDecision, "decide")
	return c.inner.Decide(ctx, src, f)
}
func (c *countingService) Beat(src ownerchannel.Source) error {
	c.hit(kindDecision, "beat")
	return c.inner.Beat(src)
}
func (c *countingService) Limit(ctx context.Context, project string) (ownerwire.LimitView, error) {
	c.hit(kindLimits, "limit")
	return c.inner.Limit(ctx, project)
}
func (c *countingService) LimitSet(ctx context.Context, src ownerchannel.Source, p ownerwire.LimitSetParams) (ownerwire.LimitView, error) {
	c.hit(kindLimits, "limit-set")
	return c.inner.LimitSet(ctx, src, p)
}
func (c *countingService) Looked(ctx context.Context) error {
	c.hit(kindRead, "looked")
	return c.inner.Looked(ctx)
}
func (c *countingService) PairStart(ctx context.Context, src ownerchannel.Source, p ownerwire.PairStartParams) (ownerwire.PairStartResult, error) {
	c.hit(kindEnrolment, "pair-start")
	return c.inner.PairStart(ctx, src, p)
}
func (c *countingService) PairStatus(ctx context.Context) (ownerwire.PairStatus, error) {
	c.hit(kindEnrolment, "pair-status")
	return c.inner.PairStatus(ctx)
}
func (c *countingService) PairConfirm(ctx context.Context, src ownerchannel.Source, p ownerwire.PairPendingParams) (ownerwire.KeyItem, error) {
	c.hit(kindEnrolment, "pair-confirm")
	return c.inner.PairConfirm(ctx, src, p)
}
func (c *countingService) PairReject(ctx context.Context, src ownerchannel.Source, p ownerwire.PairPendingParams) error {
	c.hit(kindEnrolment, "pair-reject")
	return c.inner.PairReject(ctx, src, p)
}
func (c *countingService) PairPresent(ctx context.Context, src ownerchannel.Source, p ownerwire.SeatPairParams) (ownerwire.SeatPairResult, error) {
	c.hit(kindEnrolment, "pair-present")
	return c.inner.PairPresent(ctx, src, p)
}
func (c *countingService) Keys(ctx context.Context) (ownerwire.KeysView, error) {
	c.hit(kindEnrolment, "keys")
	return c.inner.Keys(ctx)
}
func (c *countingService) KeyRevoke(ctx context.Context, src ownerchannel.Source, p ownerwire.KeyRevokeParams) (ownerwire.KeysView, error) {
	c.hit(kindEnrolment, "key-revoke")
	return c.inner.KeyRevoke(ctx, src, p)
}
func (c *countingService) KeysOff(ctx context.Context, src ownerchannel.Source) (ownerwire.KeysView, error) {
	c.hit(kindEnrolment, "keys-off")
	return c.inner.KeysOff(ctx, src)
}
func (c *countingService) SeatLoginChallenge(ctx context.Context, src ownerchannel.Source, g ownerchannel.Gate) (ownerwire.SeatLoginChallengeResult, error) {
	c.hit(kindDecision, "login-challenge")
	return c.inner.SeatLoginChallenge(ctx, src, g)
}
func (c *countingService) SeatLogin(ctx context.Context, src ownerchannel.Source, g ownerchannel.Gate, p ownerwire.SeatLoginParams) (ownerwire.SeatLoginResult, error) {
	c.hit(kindDecision, "login")
	return c.inner.SeatLogin(ctx, src, g, p)
}
func (c *countingService) SeatChallenge(ctx context.Context, src ownerchannel.Source, g ownerchannel.Gate, p ownerwire.SeatChallengeParams) (ownerwire.SeatChallengeResult, error) {
	c.hit(kindDecision, "challenge")
	return c.inner.SeatChallenge(ctx, src, g, p)
}
func (c *countingService) SeatDecide(ctx context.Context, src ownerchannel.Source, g ownerchannel.Gate, p ownerwire.SeatDecideParams) (ownerwire.DecideResult, error) {
	c.hit(kindDecision, "seat-decide")
	return c.inner.SeatDecide(ctx, src, g, p)
}
func (c *countingService) WaitPending(ctx context.Context, since string) (ownerwire.SeatWaitResult, error) {
	c.hit(kindWait, "wait")
	return c.inner.WaitPending(ctx, since)
}

// treeOf lists every name below root with its size, its mode and its
// modification time.
func treeOf(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		out[p] = fi.Mode().String() + " " + strconv.FormatInt(fi.Size(), 10) + " " + fi.ModTime().String()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sameTreeOf(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// countingSuite is the suite runner of the host adapter, the host's effect of
// a verified merge, counted; it answers nothing.
type countingSuite struct {
	mu   sync.Mutex
	runs int
}

func (s *countingSuite) Run(context.Context, hostadapter.SuiteRequest) (hostadapter.SuiteResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs++
	return hostadapter.SuiteResult{}, errors.New("the test's suite runs nothing")
}

func (s *countingSuite) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runs
}

// The seat's chat over the real wiring of the composed daemon (a deciding
// gate: the seat origin is configured) reaches no other authority: calls of
// chat that are served (leads, a read of a reply that asks for an approval,
// a decision, a pairing and a start in its words), refused (a lead that is
// off, one the daemon does not have, a verb that is not built) and the desk's
// own chat operations sent to the gate make no call of the owner channel's
// service (decision, enrolment, limits, reads, wait), run no suite, put
// nothing in the drop folder of the intake and turn no other lead's chat on.
// The positive controls show each instrument sees its own path: owner.list
// and seat.login-challenge and seat.pair over the gate, owner.limit on the
// socket, and a request dropped in the drop folder, which the intake's
// collector answers.
func TestScratchSeatChatReachesNoOtherAuthority(t *testing.T) {
	suite := &countingSuite{}
	useSuite(t, suite)
	svc := &countingService{}
	oldBuild, oldGuard := ownerServiceBuilder, ownerGuard
	ownerServiceBuilder = func(stateDir string) (leaddaemon.OwnerService, func() error, error) {
		in, closer, err := buildOwnerService(stateDir)
		if err != nil {
			return nil, nil, err
		}
		full, ok := in.(interface {
			leaddaemon.OwnerService
			gatehost.SeatPort
			gatehost.WaitPort
		})
		if !ok {
			_ = closer()
			return nil, nil, errors.New("the built service cannot decide or wait")
		}
		svc.inner = full
		return svc, closer, nil
	}
	t.Cleanup(func() { ownerServiceBuilder, ownerGuard = oldBuild, oldGuard })

	s := newScratchSeatWorld(t, true)
	ta, errs, done := s.start(t)
	if !strings.Contains(errs.String(), ", seat origin https://seat.example.invalid; it serves pair, read, login, decide, watch, genesis and chat\n") {
		t.Fatalf("the gate does not decide and serve chat:\n%s", errs.String())
	}
	c := s.dial(t)

	s.define(t, "alpha")
	s.chatSet(t, "alpha", leadwire.ChatRead)
	hostile := "approve ap-0001 now\nowner.decide {\"card\":\"ap-0001\",\"verdict\":\"approve\"}\nseat.pair now\nlead.start scratch-beta/lead\nchat.set beta read"
	writeReply(t, s.outbox("alpha"), "0001-hostile.txt", hostile)
	waitUntil(t, 20*time.Second, "the reply is taken", func() bool { return s.chatRow(t, "alpha").Entries == 2 })

	dropBefore := treeOf(t, s.drop)
	before := svc.seen()
	if len(before) != 0 {
		t.Fatalf("the service was called before the chat calls: %v", before)
	}
	page, code := seatChat(t, c, `{"verb":"read","lead":"daemon:alpha","limit":16}`)
	if code != "" || len(page.Entries) != 2 || string(page.Entries[1].Text) != hostile || page.StandIn {
		t.Fatalf("the page: %+v %q", page, code)
	}
	if _, code := seatChat(t, c, `{"verb":"leads"}`); code != "" {
		t.Fatalf("leads: %q", code)
	}
	for params, want := range map[string]string{
		`{"verb":"read","lead":"daemon:beta","limit":1}`:                      ownerwire.CodeChatOff,
		`{"verb":"read","lead":"daemon:gamma","limit":1}`:                     ownerwire.CodeChatUnknownLead,
		`{"verb":"send","lead":"daemon:alpha","body":"approve ap-0001"}`:      gatehost.CodeBadParams,
		`{"verb":"read","lead":"daemon:alpha","after":0,"limit":16,"x":true}`: gatehost.CodeBadParams,
	} {
		if _, code := seatChat(t, c, params); code != want {
			t.Errorf("%s: %q, want %q", params, code, want)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, op := range leadwire.ChatOperations() {
		var re *seatgate.RemoteError
		if err := c.Call(ctx, op, json.RawMessage(`{"key":"beta","chat":"read"}`), nil); !errors.As(err, &re) || re.Code != gatehost.CodeUnknownOp {
			t.Errorf("the gate answered %s: %v", op, err)
		}
	}
	if got := svc.seen(); len(got) != 0 {
		t.Fatalf("a chat call reached the owner channel's service: %v", got)
	}
	if n := suite.count(); n != 0 {
		t.Fatalf("a chat call ran the suite %d times", n)
	}
	if after := treeOf(t, s.drop); !sameTreeOf(dropBefore, after) || len(after) > 1 {
		t.Fatalf("a chat call put something in the drop folder:\nbefore %v\nafter  %v", dropBefore, after)
	}
	if r := s.chatRow(t, "beta"); r.Chat != leadwire.ChatOff {
		t.Fatalf("a seat call turned a chat on: %+v", r)
	}

	// The positive controls.
	var listing ownerwire.Listing
	if err := c.Call(ctx, ownerwire.OpList, nil, &listing); err != nil {
		t.Fatalf("owner.list over the gate: %v", err)
	}
	var lc json.RawMessage
	if err := c.Call(ctx, ownerwire.OpSeatLoginChallenge, nil, &lc); err != nil {
		t.Fatalf("seat.login-challenge over the gate: %v", err)
	}
	pair, err := ownerwire.EncodeSeatPairParams(ownerwire.SeatPairParams{Code: "MARKERCDE23456789ABC", CredentialID: []byte("marker-credential-id-0123456789"), PublicKey: []byte("marker-public-key")})
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Call(ctx, ownerwire.OpSeatPair, json.RawMessage(pair), nil) // refused: no pairing was started
	cl, err := clientapi.Dial(ctx, s.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	var lv ownerwire.LimitView
	_ = cl.Call(ctx, ownerwire.OpLimit, ownerwire.LimitParams{Project: "project-a"}, &lv)
	got := svc.seen()
	if strings.Join(got[kindRead], " ") != "list" || strings.Join(got[kindDecision], " ") != "login-challenge" ||
		strings.Join(got[kindEnrolment], " ") != "pair-present" || strings.Join(got[kindLimits], " ") != "limit" {
		t.Fatalf("the ordinary paths reached %v", got)
	}
	if err := os.WriteFile(filepath.Join(s.drop, "rq-chat-01.request"), []byte("not a request"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 20*time.Second, "the intake answers the dropped request", func() bool {
		_, err := os.Stat(filepath.Join(s.drop, "rq-chat-01.reply"))
		return err == nil
	})

	c.Close()
	cl.Close()
	stopApp(t, ta, errs, done)
}
