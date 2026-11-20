//go:build linux && ataniq_canary

package main

// The holder, intake and accepting-path layers of the canary slice's
// end-to-end tests (plan, section 6). Every test starts the holder in canary
// mode over the fake host of canary_helpers_linux_test.go, with the fake
// launcher: the plan's process is replaced by the stand-in agent, built here
// from cmd/ataniq-canary-standin, directly under the PTY, in a temporary
// clone. Nothing starts a tool, a namespace, a unit or a real agent.
//
// The control requests are the golden files of the daemon layer
// (internal/leaddaemon/canary_contract_test.go), sent to the holder's control
// socket. The recorded screens of the accepting path come from
// canary_fixture_path_test.go, the one file that names a fixture path.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/exchange/gitenv"
	"ataniq/internal/exchange/sandboxplan/plantest"
	"ataniq/internal/intake"
	"ataniq/internal/intake/execconfig"
	"ataniq/internal/intake/verify"
	"ataniq/internal/leadcanary/provision/provisiontest"
	"ataniq/internal/leadholder/sandboxstart"
	"ataniq/internal/leadholder/serve"
	"ataniq/internal/leadholder/session"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/leadsandbox/profile"
	"ataniq/internal/mirror"
	"ataniq/internal/terminalconsole/sessionpane"
)

// goldenDir is the folder of the golden requests of the daemon layer.
const goldenDir = "../../internal/leadholder/wire/testdata/canary"

// The facts of the golden requests (see their README).
const (
	goldenEpoch     = 1
	goldenMessageID = "golden-msg-1"
	goldenToken     = "0123456789abcdef-golden"
	maskedTicket    = "0000000000000000000000000000000000000000000000000000000000000000"
)

// The stand-in's own names.
const (
	standInBranch    = "canary/one"
	standInSubject   = "canary: token"
	standInTokenFile = "canary-token.txt"
)

// buildStandIn builds the stand-in agent, with the tag, offline, through the Go
// tool the other tests of this command use, into a temporary folder, and
// returns the path of the program.
func buildStandIn(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "stand-in")
	cmd := exec.Command(guardGoTool(t), "build", "-tags", canaryTag, "-o", out, "./cmd/ataniq-canary-standin")
	cmd.Dir = filepath.Join("..", "..")
	cmd.Env = append(guardGoEnv(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the stand-in: %v\n%s", err, b)
	}
	return out
}

// e2eWorld is a canary rig with a temporary clone of a mirror, a home for the
// stand-in and the pair of the tested table the holder is started with.
type e2eWorld struct {
	*canaryRig
	pair    testedPair
	standIn string
	// home is the stand-in's HOME: git's identity, and in replay mode the
	// replay folder.
	home string
	// mirror is the exchange mirror, and upstreamTip the commit its upstream
	// branch is at, which is the tip the clone started from.
	mirror, upstreamTip string

	sessMu sync.Mutex
	sess   *session.Session
}

func newE2EWorld(t *testing.T) *e2eWorld {
	t.Helper()
	w := &e2eWorld{canaryRig: newCanaryRig(t), pair: pickTestedPair(t), standIn: buildStandIn(t)}
	// The inputs name the pair the holder is started with.
	w.in.Runtime, w.in.RuntimeVersion = w.pair.Runtime, w.pair.Version
	w.in.Sources.InstructionFile = w.base_("owned/instructions/" + profile.Runtime(w.pair.Runtime).InstructionFileName())
	w.writeInputs()

	w.home = filepath.Join(w.canaryRig.base, "standin-home")
	if err := os.Mkdir(w.home, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := "[user]\n\tname = Sample Name\n\temail = sample@example.invalid\n"
	if err := os.WriteFile(filepath.Join(w.home, ".gitconfig"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	w.makeMirrorAndClone()
	return w
}

// base_ is a path under the rig's base folder.
func (w *e2eWorld) base_(rel string) string { return filepath.Join(w.canaryRig.base, rel) }

// makeMirrorAndClone makes the exchange mirror, with the upstream branch at one
// commit, and the lead's clone of it in the layout's tree folder.
func (w *e2eWorld) makeMirrorAndClone() {
	t := w.t
	git, err := gitenv.New("/usr/bin/git")
	if err != nil {
		t.Fatal(err)
	}
	work := w.base_("work")
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	provisiontest.Git(t, work, "init", "--quiet", "--initial-branch=main")
	provisiontest.WriteFile(t, filepath.Join(work, "README"), []byte("sample\n"), 0o644)
	provisiontest.Git(t, work, "add", "README")
	provisiontest.Git(t, work, "commit", "--quiet", "--message", "sample: first")
	w.upstreamTip = provisiontest.Git(t, work, "rev-parse", "HEAD")

	if err := os.Mkdir(filepath.Join(w.stateRoot, "mirrors"), 0o700); err != nil {
		t.Fatal(err)
	}
	if w.mirror, err = mirror.Path(w.stateRoot, canaryProject); err != nil {
		t.Fatal(err)
	}
	if _, err := mirror.Create(context.Background(), git, w.mirror); err != nil {
		t.Fatal(err)
	}
	provisiontest.Git(t, w.mirror, "--git-dir=.", "fetch", "--quiet", filepath.Join(work, ".git"), "refs/heads/main:refs/heads/main")
	provisiontest.Git(t, w.canaryRig.base, "clone", "--quiet", "--branch", "main", w.mirror, w.tree)
}

// gitIn runs git in the clone.
func (w *e2eWorld) gitIn(args ...string) string {
	w.t.Helper()
	return provisiontest.Git(w.t, w.tree, args...)
}

// spec is the holder's command line for the golden requests: the pair's
// runtime and version, the golden epoch, the layout's folders, the footer mode
// and --inject.
func (w *e2eWorld) spec(digest, mode string) *argSpec {
	a := w.canaryRig.spec(digest, "--inject")
	a.set("runtime", w.pair.Runtime).set("runtime-version", w.pair.Version)
	a.set("epoch", strconv.Itoa(goldenEpoch)).set("expected-mode", mode)
	a.set("cols", strconv.Itoa(w.pair.Cols)).set("rows", strconv.Itoa(w.pair.Rows))
	return a
}

// pinDigest is the profile digest of the plan the holder will build for this
// world's command line: sandboxstart.ProfileDigest over the inputs with the
// fields the holder fills in from that command line (the agent's vector and its
// environment, converted as the holder converts it). The holder is not started,
// so nothing is left in its state folder. TestCanaryHolderRefusesAWrongPin holds
// that the holder's own start computes the same digest.
func (w *e2eWorld) pinDigest() string {
	t := w.t
	t.Helper()
	a := w.spec("", w.pair.Mode)
	in := w.in
	in.AgentArgv = append([]string(nil), a.agent...)
	in.AgentEnv = sandboxstart.AgentEnvOf(a.env)
	uid, gid := w.sys.IDs()
	d, err := sandboxstart.ProfileDigest(in, uid, gid)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// A start pinned to the wrong digest is refused at the step verify-plan, before
// anything is launched, and the refusal names the digest the holder really
// computed, which is the one pinDigest gives.
func TestCanaryHolderRefusesAWrongPin(t *testing.T) {
	w := newE2EWorld(t)
	want := w.pinDigest()
	wrong := strings.Repeat("0", 64)
	if want == wrong {
		t.Fatal("the real digest is the wrong pin")
	}
	if code := w.runNow(w.spec(wrong, w.pair.Mode).build()); code != exitSandbox {
		t.Fatalf("the start with the wrong pin exited %d; stderr: %s", code, w.errbuf.String())
	}
	m := pinnedDigest.FindStringSubmatch(w.errbuf.String())
	if m == nil {
		t.Fatalf("the refusal does not name the digest: %s", w.errbuf.String())
	}
	if m[1] != want {
		t.Errorf("the holder computed %s, pinDigest says %s", m[1], want)
	}
	if n := len(w.launches()); n != 0 {
		t.Errorf("the refused start launched %d times", n)
	}
}

// startHolder runs the holder with the stand-in in place of the plan's process,
// in the clone, with the stand-in's own HOME and nothing else in its
// environment; tweak changes the service's configuration after the holder has
// made it.
func (w *e2eWorld) startHolder(a *argSpec, tweak func(*serve.Config)) {
	w.t.Helper()
	d := w.canaryRig.deps()
	d.canary.startSession = func(cfg session.Config) (*session.Session, error) {
		// The fake launcher's own choices: the working directory is the clone
		// (the real plan sets it inside the sandbox), the argument vector the
		// stand-in's, and its environment its HOME.
		cfg.Argv, cfg.Env, cfg.Dir = []string{w.standIn}, []string{"HOME=" + w.home, "TERM=xterm-256color"}, w.tree
		s, err := session.Start(cfg)
		if err == nil {
			w.sessMu.Lock()
			w.sess = s
			w.sessMu.Unlock()
		}
		return s, err
	}
	if tweak != nil {
		prev := d.tweakServe
		d.tweakServe = func(c *serve.Config) {
			if prev != nil {
				prev(c)
			}
			tweak(c)
		}
	}
	args := a.build()
	go func() { w.done <- run(args, d) }()
	w.waitReady()
}

func (w *e2eWorld) session() *session.Session {
	w.sessMu.Lock()
	defer w.sessMu.Unlock()
	return w.sess
}

// screenText is the text of the holder's pane.
func (w *e2eWorld) screenText() string {
	var text string
	if s := w.session(); s != nil {
		s.Screen(func(p *sessionpane.Pane, _ uint64) { text = p.TextSnapshot().Text })
	}
	return text
}

// waitScreen waits for the pane to show text.
func (w *e2eWorld) waitScreen(text string) {
	w.t.Helper()
	poll(w.t, func() string { return "the screen to show " + text + "; it shows\n" + w.screenText() }, func() bool {
		return strings.Contains(w.screenText(), text)
	})
}

// send sends one request payload, framed, to the control socket and returns the
// response.
func (w *e2eWorld) send(payload []byte) wire.Response {
	w.t.Helper()
	c, err := w.sockDial(serve.ControlSocket)
	if err != nil {
		w.t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(patience))
	if err := wire.Write(c, wire.ControlRequests, wire.TypeRequest, payload); err != nil {
		w.t.Fatal(err)
	}
	f, err := wire.NewReader(c, wire.ControlResponses).Next()
	if err != nil {
		w.t.Fatal(err)
	}
	resp, err := wire.DecodeResponse(f.Payload)
	if err != nil {
		w.t.Fatal(err)
	}
	return resp
}

// sendRequest sends a request built in the test.
func (w *e2eWorld) sendRequest(req wire.Request) wire.Response {
	w.t.Helper()
	p, err := wire.EncodeRequest(req)
	if err != nil {
		w.t.Fatal(err)
	}
	return w.send(p)
}

// counters returns the session counters of the status answer.
func (w *e2eWorld) counters() map[string]uint64 {
	resp := w.sendRequest(wire.Request{Op: wire.OpStatus, Epoch: goldenEpoch})
	if !resp.OK || resp.Status == nil {
		w.t.Fatalf("status: %+v", resp)
	}
	return resp.Status.Counters
}

// stopHolder stops the holder through its control socket and waits for it.
func (w *e2eWorld) stopHolder() {
	w.t.Helper()
	if resp := w.sendRequest(wire.Request{Op: wire.OpStop, Epoch: goldenEpoch}); !resp.OK {
		w.t.Fatalf("stop: %+v", resp)
	}
	if code := w.wait(); code != exitOK {
		w.t.Fatalf("the holder exited %d; stderr: %s", code, w.errbuf.String())
	}
	if !w.lockFree() {
		w.t.Error("the role lock is still held after the holder ended")
	}
}

func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(goldenDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// goldenTicket is the golden ticket request with a fresh ticket in place of
// the masked one, and the golden inject request.
func goldenRequests(t *testing.T) (ticket, inject []byte) {
	t.Helper()
	tk, err := wire.NewTicket()
	if err != nil {
		t.Fatal(err)
	}
	masked := readGolden(t, "ticket-request.json")
	if strings.Count(string(masked), maskedTicket) != 1 {
		t.Fatalf("the golden ticket request does not hold the masked ticket once: %s", masked)
	}
	ticket = []byte(strings.Replace(string(masked), maskedTicket, tk, 1))
	// Both decode as requests, so the holder is sent what the wire accepts.
	inject = readGolden(t, "inject-request.json")
	for _, p := range [][]byte{ticket, inject} {
		if _, err := wire.DecodeRequest(p); err != nil {
			t.Fatalf("a golden request does not decode: %v", err)
		}
	}
	return ticket, inject
}

// paste is what a terminal sends for a bracketed paste of text.
func paste(text string) []byte { return []byte("\x1b[200~" + text + "\x1b[201~") }

// pasteAndEnter is a test-only deliverer: it writes one paste of the text and
// one Enter through the session's input lock (the fence's own way to the
// agent), and answers submitted. It reads no screen and judges nothing.
type pasteAndEnter struct {
	sess *session.Session

	mu    sync.Mutex
	calls []string
}

func (d *pasteAndEnter) Deliver(ctx context.Context, messageID, text string) wire.InjectResult {
	d.mu.Lock()
	d.calls = append(d.calls, messageID)
	d.mu.Unlock()
	err := d.sess.WithFence(ctx, func(f session.Fenced) error {
		return f.Frozen(time.Now().Add(5*time.Second), func(_ session.FenceView, write func([]byte) error) error {
			if err := write(paste(text)); err != nil {
				return err
			}
			return write([]byte("\r"))
		})
	})
	if err != nil {
		return wire.InjectResult{Outcome: wire.InjectOutcomeUncertain, Reason: wire.InjectReasonDied, Written: wire.InjectWrittenUnknown, Detail: "test deliverer failed"}
	}
	return wire.InjectResult{Outcome: wire.InjectOutcomeSubmitted, Written: wire.InjectWrittenWhole, Detail: "test deliverer"}
}

// TestCanaryHolderAndIntakeLayers is the holder layer and the intake layer.
// The holder, in canary mode with the fake launcher, runs the stand-in in plain
// mode in a temporary clone. The golden requests are replayed to its control
// socket, with a test-only deliverer; the stand-in commits the token on its
// branch in the clone. Then the mirror and the intake are driven as libraries
// on that clone, through fetch, review, verify and integrate, and the upstream
// branch of the mirror is the stand-in's commit.
func TestCanaryHolderAndIntakeLayers(t *testing.T) {
	w := newE2EWorld(t)
	digest := w.pinDigest()

	// ---- the holder layer ----
	deliverer := &pasteAndEnter{}
	w.startHolder(w.spec(digest, w.pair.Mode), func(c *serve.Config) {
		deliverer.sess = w.session()
		c.Deliverer = deliverer
	})
	// The stand-in is on its prompt, in raw mode, with paste on.
	w.waitScreen(">")

	ticketReq, injectReq := goldenRequests(t)
	if resp := w.send(ticketReq); !resp.OK {
		t.Fatalf("the golden ticket request: %+v", resp)
	}
	resp := w.send(injectReq)
	want := wire.InjectResult{Outcome: wire.InjectOutcomeSubmitted, Written: wire.InjectWrittenWhole, Detail: "test deliverer"}
	if !resp.OK || resp.Inject == nil || *resp.Inject != want {
		t.Fatalf("the golden inject request: %+v (%+v), want ok and %+v", resp, resp.Inject, want)
	}
	deliverer.mu.Lock()
	calls := append([]string(nil), deliverer.calls...)
	deliverer.mu.Unlock()
	if len(calls) != 1 || calls[0] != goldenMessageID {
		t.Fatalf("the deliverer was asked for %q", calls)
	}

	// The stand-in committed the token on its branch in the clone.
	var commit string
	poll(t, func() string { return "the stand-in's commit; screen:\n" + w.screenText() }, func() bool {
		// The branch is made before the commit: wait for the token file in it.
		if _, err := tryGit(w.tree, "show", standInBranch+":"+standInTokenFile); err != nil {
			return false
		}
		out, err := tryGit(w.tree, "rev-parse", "--verify", "--quiet", "refs/heads/"+standInBranch)
		commit = out
		return err == nil
	})
	if got := w.gitIn("log", "-1", "--format=%s", standInBranch); got != standInSubject {
		t.Errorf("the commit's subject is %q, want %q", got, standInSubject)
	}
	if got := w.gitIn("show", standInBranch+":"+standInTokenFile); got != goldenToken {
		t.Errorf("the commit's token file holds %q, want %q", got, goldenToken)
	}
	if got := w.gitIn("rev-parse", standInBranch+"^"); got != w.upstreamTip {
		t.Errorf("the commit's parent is %s, want the upstream tip %s", got, w.upstreamTip)
	}
	if n := w.gitIn("rev-list", "--count", w.upstreamTip+".."+standInBranch); n != "1" {
		t.Errorf("%s commits on the stand-in's branch beyond the upstream tip, want 1", n)
	}
	w.stopHolder()

	// ---- the intake layer ----
	w.intake(commit)
}

// tryGit runs git in dir and returns its output and its error, without failing
// the test.
func tryGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("/usr/bin/git", args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/dev/null", "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	b, err := cmd.Output()
	return strings.TrimSpace(string(b)), err
}

// intake drives the mirror and the intake as libraries on the clone: fetch,
// review, verify and integrate, with a planner that confines nothing (the
// intake's test planner: git-upload-pack and /usr/bin/env on the host). It
// asserts that the upstream ref of the mirror is the stand-in's commit, moved
// fast-forward, and that an intake record exists.
func (w *e2eWorld) intake(commit string) {
	t := w.t
	t.Helper()
	ctx := context.Background()
	git, err := gitenv.New("/usr/bin/git")
	if err != nil {
		t.Fatal(err)
	}
	up, err := plantest.GitUploadPack()
	if err != nil {
		t.Fatalf("the test needs git-upload-pack: %v", err)
	}
	planner := &plantest.Fake{
		UploadPack: up, MeasureBytes: 1000,
		VerifyPrefix: []string{"/usr/bin/env"},
		VerifyEnv:    []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"},
	}
	m, err := mirror.Open(ctx, git, w.mirror)
	if err != nil {
		t.Fatal(err)
	}
	// The lead's role in the layout is the layout role of the canary.
	role := canaryLayoutRole

	// fetch
	rep, err := m.Fetch(ctx, mirror.FetchRequest{StateRoot: w.stateRoot, Role: role, Project: canaryProject, Planner: planner})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	tipRef, err := mirror.LeadRef(role, standInBranch)
	if err != nil {
		t.Fatal(err)
	}
	created := false
	for _, u := range rep.Updates {
		if u.Ref == tipRef && u.Old == "" && u.New == commit {
			created = true
		}
	}
	if !created {
		t.Fatalf("the fetch did not create %s at %s: %+v", tipRef, commit, rep.Updates)
	}

	// review
	baseRef, err := mirror.UpstreamRef("main")
	if err != nil {
		t.Fatal(err)
	}
	src := intake.Source{Git: git, GitDir: m.Path()}
	req := intake.Request{BaseRef: baseRef, TipRef: tipRef, Rules: execconfig.Default()}
	rev, err := intake.Inspect(ctx, src, req)
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if rev.Base != w.upstreamTip || rev.Tip != commit || len(rev.Commits) != 1 || len(rev.Changes) != 1 || rev.Changes[0].Path != standInTokenFile {
		t.Fatalf("the review: base %s tip %s, %d commits, changes %+v", rev.Base, rev.Tip, len(rev.Commits), rev.Changes)
	}

	// verify
	records, work := w.base_("records"), w.base_("verify-work")
	for _, d := range []string{records, work} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	store, err := intake.OpenStore(records)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	gates := []verify.Gate{{Name: "token", Argv: []string{"/bin/sh", "-c", "test -f " + standInTokenFile}}}
	res, err := verify.Run(ctx, verify.Request{
		Project: canaryProject, Mirror: m.Path(), TipRef: tipRef, Tip: commit,
		Gates: gates, Planner: planner, Runner: verify.ExecRunner{}, WorkRoot: work,
	})
	if err != nil || !res.Passed || res.Exit != 0 {
		t.Fatalf("verify: %v, result %+v", err, res)
	}
	if _, err := store.PutVerification(canaryProject, res, gates); err != nil {
		t.Fatalf("storing the verification: %v", err)
	}

	// integrate, signing off the change as a whole (no path is flagged)
	rec, err := intake.Integrate(ctx, src, intake.IntegrateRequest{
		Project: canaryProject, Review: req,
		SignOff: intake.SignOff{Tip: rev.Tip, Digest: rev.Digest(), Change: true},
		Store:   store,
	})
	if err != nil {
		t.Fatalf("integrate: %v", err)
	}
	if rec.Base != w.upstreamTip || rec.Tip != commit {
		t.Errorf("the intake record: base %s tip %s, want %s and %s", rec.Base, rec.Tip, w.upstreamTip, commit)
	}

	// The upstream ref of the mirror is the stand-in's commit, one fast-forward
	// step from where it was.
	up1, err := m.Resolve(ctx, baseRef)
	if err != nil {
		t.Fatal(err)
	}
	if up1.ID != commit {
		t.Errorf("the upstream ref is at %s, want the stand-in's commit %s", up1.ID, commit)
	}
	if _, err := tryGit(m.Path(), "--git-dir=.", "merge-base", "--is-ancestor", w.upstreamTip, up1.ID); err != nil {
		t.Errorf("the upstream ref did not move fast-forward from %s: %v", w.upstreamTip, err)
	}
	if parent, _ := tryGit(m.Path(), "--git-dir=.", "rev-parse", up1.ID+"^"); parent != w.upstreamTip {
		t.Errorf("the upstream tip's parent is %s, want %s", parent, w.upstreamTip)
	}
	recs, err := store.Intakes()
	if err != nil || len(recs) != 1 || recs[0].Tip != commit || recs[0].Base != w.upstreamTip {
		t.Errorf("the intake records: %+v %v", recs, err)
	}
}

// readReplayFile reads a file the stand-in wrote in its replay folder, or "" when
// it is not there yet or is empty.
func (w *e2eWorld) readReplayFile(name string) string {
	b, err := os.ReadFile(filepath.Join(w.home, "canary-replay", name))
	if err != nil {
		return ""
	}
	return string(b)
}

// TestCanaryAcceptingPathReachesSubmitted is the accepting path (R6, M8): the
// stand-in in replay mode draws the recorded idle screen of the tested pair,
// asks the terminal the support query and DA1, answers as an agent that
// reports idle, and draws the recorded screen after a collapsed paste when
// pasted into. The holder has the real bridge and the real deliverer, and no
// test-only one; the golden inject request must reach submitted.
func TestCanaryAcceptingPathReachesSubmitted(t *testing.T) {
	w := newE2EWorld(t)
	dir := filepath.Join(w.home, "canary-replay")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	w.pair.copyScreens(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "replay.on"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := w.pinDigest()
	w.startHolder(w.spec(digest, w.pair.Mode), nil)

	// 1. The stand-in read the OSC 7501 reply before the DA1 reply.
	poll(t, func() string { return "order.txt; stderr: " + w.errbuf.String() }, func() bool {
		return w.readReplayFile("order.txt") != ""
	})
	if got := w.readReplayFile("order.txt"); got != "7501-first\n" {
		t.Fatalf("order.txt says %q, want %q", got, "7501-first\n")
	}

	// 2. The tracker shows idle: the session counters show a query and a
	// report, and no report that did not parse.
	var counters map[string]uint64
	poll(t, func() string {
		return "a query and a report in the counters; they are " + strconv.Quote(sprint(counters))
	}, func() bool {
		counters = w.counters()
		return counters[counterQueries] == 1 && counters[counterReports] == 1
	})
	if counters[counterInvalid] != 0 {
		t.Errorf("%d reports did not parse", counters[counterInvalid])
	}

	// The agent has drawn its idle screen, and the screen has been quiet for
	// longer than the fence's rule (300 ms): an agent that is idle has been for
	// a while when the daemon sends.
	w.waitScreen(w.pair.Mode)
	time.Sleep(screenSettle)

	// 3. The golden requests: the fence writes one paste, reads the second
	// screen and writes one Enter.
	ticketReq, injectReq := goldenRequests(t)
	if resp := w.send(ticketReq); !resp.OK {
		t.Fatalf("the golden ticket request: %+v", resp)
	}
	resp := w.send(injectReq)
	if !resp.OK || resp.Inject == nil {
		t.Fatalf("the golden inject request: %+v", resp)
	}
	r := *resp.Inject
	t.Logf("the fence answered: %+v", r)
	if r.Outcome != wire.InjectOutcomeSubmitted || r.Reason != wire.InjectReasonNone || r.Written != wire.InjectWrittenWhole {
		t.Fatalf("the fence answered outcome %q, reason %q, written %q, detail %q; want submitted, no reason, whole",
			r.Outcome, r.Reason, r.Written, r.Detail)
	}
	// The fence's own words say it wrote the paste and then the Enter, after a
	// check that found the agent idle before the paste.
	for _, part := range []string{"written paste and Enter", "before: state idle"} {
		if !strings.Contains(r.Detail, part) {
			t.Errorf("the detail %q does not say %q", r.Detail, part)
		}
	}
	// The second screen was read: it is the screen the pane holds, and the
	// fence read it before the Enter (an Enter after a screen that was not read
	// is not a thing the fence does). The stand-in draws it only after the
	// paste.
	if line := longestLine(w.pair.pastedText(t)); !strings.Contains(w.screenText(), line) {
		t.Errorf("the pane does not show the recorded screen after the paste (its line %q); it shows\n%s", line, w.screenText())
	}

	// 5. The stand-in committed: its result line, and the commit in the clone.
	var result string
	poll(t, func() string { return "result.txt; it says " + strconv.Quote(result) }, func() bool {
		result = w.readReplayFile("result.txt")
		return strings.HasSuffix(result, "\n")
	})
	short, found := strings.CutPrefix(strings.TrimSuffix(result, "\n"), "committed ")
	if !found || short == "" {
		t.Fatalf("result.txt says %q, want %q and a short hash", result, "committed ")
	}
	commit := w.gitIn("rev-parse", "--verify", "refs/heads/"+standInBranch)
	if !strings.HasPrefix(commit, short) {
		t.Errorf("result.txt names %s, the commit on %s is %s", short, standInBranch, commit)
	}
	if got := w.gitIn("show", standInBranch+":"+standInTokenFile); got != goldenToken {
		t.Errorf("the commit's token file holds %q, want %q", got, goldenToken)
	}
	if got := w.gitIn("rev-parse", commit+"^"); got != w.upstreamTip {
		t.Errorf("the commit's parent is %s, want the upstream tip %s", got, w.upstreamTip)
	}
	w.stopHolder()
}

// longestLine is the longest line of text, trimmed.
func longestLine(text string) string {
	best := ""
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); len(l) > len(best) {
			best = l
		}
	}
	return best
}

// screenSettle is how long the test lets the drawn screen stand before it
// sends the message: more than the fence's screen quiet of 300 ms.
const screenSettle = 600 * time.Millisecond

func sprint(m map[string]uint64) string {
	var parts []string
	for k, v := range m {
		parts = append(parts, k+"="+strconv.FormatUint(v, 10))
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}
