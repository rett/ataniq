//go:build linux && ataniq_ownerchannel

package hostadapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ataniq/internal/approval/door"
	"ataniq/internal/approval/door/doortest"
	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/approval/record"
	"ataniq/internal/exchange/sandboxplan"
	"ataniq/internal/intake/execconfig"
)

// This file is the phone trial: a STAND-IN rig for scratch state only, in
// which the owner tries the phone end to end against a real daemon without a
// lead, a mirror or a sandbox. "ataniqd trial init" writes its settings
// ([TrialScratch], with "trial": true), "ataniqd run --owner-channel --trial"
// serves it ([OpenTrial]) and "ataniqd trial submit" makes a sample card on
// demand ([Trial.Submit]).
//
// What is real: the owner channel's service, its journal in the state folder,
// the stand-in door of the scratch pass, the key list, and everything a
// decision goes through (the challenge, the passkey assertion and its
// verification, the decision record, the door's checks of the binding and the
// journal's evidence). Nothing of that is skipped or loosened.
//
// What is a sample: the request (no lead wrote it), and the host's findings
// for it. The trial host below stands for the merge, the suite, the review and
// the mirror with values it makes up and keeps in memory: no git runs, no
// sandbox starts, and no file but the journal and the key list is written. Its
// Apply moves nothing and says so: an approved trial card ends not-applied,
// with the line [TrialLine]. Trial settings name no mirror, no drop folder and
// no sandbox, so there is nothing a trial card could be applied to.

// TrialLine is the line of the outcome of every trial card that is used: the
// trial applies nothing.
const TrialLine = "applied: nothing (trial)"

// The kinds of sample card [Trial.Submit] makes.
var (
	// TrialFinish is a feature finish: a sample feature, its merge, a suite
	// record, a review with three or four changed paths and, every other
	// card, a flagged path that needs its own sign-off.
	TrialFinish = "finish"
	// TrialPrune is a branch prune of two or three sample branches. The
	// service takes no prune request from a lead yet, so the trial asks the
	// stand-in door to plan it directly; deciding it goes through the
	// service as any card does.
	//
	// Its word is the last of the record's verb "branch prune", made from the
	// verb rather than written as a literal of its own: the package's test
	// that no maintenance entry point exists refuses every non-test string
	// literal that is a git maintenance subcommand, and this word is the
	// record's verb, never a git command (nothing of the trial runs git).
	TrialPrune = record.VerbBranchPrune[len("branch "):]
)

// TrialKinds are the kinds of sample card, in the order they are named.
var TrialKinds = []string{TrialFinish, TrialPrune}

// TrialScratch returns the settings "ataniqd trial init" writes: a sample
// project, role and target, a seat at epoch 1 and revision 1, the stand-in
// door, and the three kinds of passkey.
func TrialScratch() Scratch {
	return Scratch{
		Project: "trial-project", Role: "trial-lead", Target: "refs/heads/develop",
		Seat: ScratchSeat{Epoch: 1, SeatRev: 1}, Door: DoorStandIn,
		Kinds: []string{"platform-passkey", "vault-passkey", "passkey"},
		Trial: true,
	}
}

// checkTrial checks settings that say they are a trial's: the stand-in door,
// a valid project, role, target and seat, valid kinds, and none of the keys
// that name a place (mirror, drop folder, sandbox template, work folder,
// roots) or the merge (identity, gates).
func (s Scratch) checkTrial() error {
	switch {
	case s.Door != DoorStandIn:
		return errors.New("hostadapter: the scratch settings do not select the stand-in door (\"door\": \"stand-in\")")
	case s.Mirror != "" || s.Drop != "" || s.Sandbox != "" || s.Work != "" || len(s.Roots) != 0 ||
		len(s.Gates) != 0 || s.Identity != (ScratchIdentity{}):
		return errors.New("hostadapter: the trial's settings name a mirror, a drop folder, a sandbox template, a work folder, a root, a gate or an identity; a trial has none of them")
	case !sandboxplan.ValidName(s.Project) || !sandboxplan.ValidName(s.Role):
		return errors.New("hostadapter: the trial's project or role is not a valid name")
	case !validTarget(s.Target):
		return errors.New("hostadapter: the target is not a branch under refs/heads/")
	case s.Seat.Epoch < 0 || s.Seat.SeatRev < 0:
		return errors.New("hostadapter: the trial's seat has a negative number")
	}
	if err := s.KindPolicy().Check(); err != nil {
		return errors.New("hostadapter: the scratch settings name a kind of credential that is not one (platform-passkey, vault-passkey, passkey), or one twice")
	}
	return nil
}

// ErrScratchExists says that the state folder holds scratch settings already;
// they are never overwritten.
var ErrScratchExists = errors.New("hostadapter: the state folder holds scratch settings already, which are never overwritten")

// WriteTrialScratch writes [TrialScratch] into the owner folder of the state
// folder, a clean absolute path whose parent exists. It makes the state folder
// and the owner folder private (0700) when they are not there and refuses
// either when it is not a private folder of the caller's, and it writes the
// file 0600 and never over one that is there ([ErrScratchExists]). The caller
// checks first that the state folder is a scratch one.
func WriteTrialScratch(stateDir string) (Scratch, error) {
	if stateDir == "" || !filepath.IsAbs(stateDir) || filepath.Clean(stateDir) != stateDir {
		return Scratch{}, errors.New("hostadapter: the state folder is not a clean absolute path")
	}
	owner := filepath.Join(stateDir, OwnerDirName)
	for _, d := range []string{stateDir, owner} {
		if err := os.Mkdir(d, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return Scratch{}, err
		}
		if err := checkPrivateDir(d); err != nil {
			return Scratch{}, err
		}
	}
	sc := TrialScratch()
	b, err := json.MarshalIndent(sc, "", "  ")
	if err != nil {
		return Scratch{}, err
	}
	b = append(b, '\n')
	path := filepath.Join(owner, ScratchName)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return Scratch{}, ErrScratchExists
	}
	if err != nil {
		return Scratch{}, err
	}
	_, werr := f.Write(b)
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		_ = os.Remove(path)
		return Scratch{}, err
	}
	return sc, nil
}

// TrialConfig is what [OpenTrial] makes the trial from.
type TrialConfig struct {
	// StateDir is the daemon's state folder, a clean absolute path; the
	// journal is in OwnerDirName below it.
	StateDir string
	// Scratch is the trial's settings, which [LoadScratch] read with
	// "trial": true.
	Scratch Scratch
	// Now is the clock. Default time.Now.
	Now func() time.Time
	// Manual says that no loop is started: the caller ticks the service. A
	// test uses it.
	Manual bool
	// TickEvery is the period of the service's tick; zero is
	// [DefaultTickEvery].
	TickEvery time.Duration
	// Notice, when set, is called with a host-written line for something the
	// operator should see.
	Notice func(line string)
	// Service holds the settings of the service the trial does not own (the
	// key list, the kind policy, the presence window). Its journal, door,
	// merger, intake, verifier, mailbox, ender, clock and notice are set by
	// OpenTrial.
	Service ownerchannel.Config
}

// Trial is the owner channel's service of a phone trial, on the real journal
// and the stand-in door, with the trial host in place of the merge, the suite,
// the review and the mirror.
type Trial struct {
	// Service is the owner channel's service, as the daemon serves it.
	Service *ownerchannel.Service

	door  *doortest.Door
	host  *trialHost
	sc    Scratch
	now   func() time.Time
	stamp string

	mu sync.Mutex
	n  int

	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
	err    error
}

// OpenTrial opens the trial's service in the state folder: the real journal
// in its owner folder, the stand-in door of the settings' seat, and the trial
// host. It starts the service's tick unless cfg.Manual. Close ends it.
func OpenTrial(ctx context.Context, cfg TrialConfig) (*Trial, error) {
	if cfg.StateDir == "" || !filepath.IsAbs(cfg.StateDir) || filepath.Clean(cfg.StateDir) != cfg.StateDir {
		return nil, errors.New("hostadapter: the state folder is not a clean absolute path")
	}
	if !cfg.Scratch.Trial {
		return nil, errors.New("hostadapter: the settings are not a trial's")
	}
	if err := cfg.Scratch.checkTrial(); err != nil {
		return nil, err
	}
	if cfg.TickEvery < 0 {
		return nil, errors.New("hostadapter: a period is negative")
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	owner := filepath.Join(cfg.StateDir, OwnerDirName)
	if err := os.Mkdir(owner, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, err
	}
	if err := checkPrivateDir(owner); err != nil {
		return nil, err
	}
	sc := cfg.Scratch
	started := now()
	dcfg := doortest.Sample(now)
	dcfg.Seat.Project, dcfg.Seat.RoleName = sc.Project, sc.Role
	dcfg.Seat.Epoch, dcfg.Seat.SeatRev = sc.Seat.Epoch, sc.Seat.SeatRev
	dcfg.Seat.Holder = record.Holder{Kind: "lead", DisplayID: sc.Role + "@e" + strconv.FormatInt(sc.Seat.Epoch, 10), Runtime: "trial"}
	dcfg.Command = "qargi"
	// The stand-in keeps nothing across a restart and the journal keeps
	// everything, so the card numbers start from the time of day, and past
	// the journal's own once it is open: a restart does not make a card
	// whose id the journal holds a decision for.
	dcfg.FirstNumber = StandInFirstNumber(started)
	standIn := doortest.New(dcfg)
	host := newTrialHost(sc.Project, sc.Target)

	jpath := filepath.Join(owner, JournalName)
	journal, err := ownerjournal.Open(jpath)
	if err != nil {
		return nil, fmt.Errorf("hostadapter: the journal: %w", err)
	}
	if err := seedNumbers(standIn, journal, started); err != nil {
		_ = journal.Close()
		return nil, err
	}
	svcCfg := cfg.Service
	svcCfg.Journal = journal
	svcCfg.Reopen = func() (ownerchannel.Journal, error) { return ownerjournal.Open(jpath) }
	svcCfg.Door, svcCfg.Ender = standIn, trialEnder{standIn}
	svcCfg.Merger, svcCfg.Intake, svcCfg.Verifier, svcCfg.Mailbox = host, host, host, host
	svcCfg.Now = now
	svcCfg.Notice = cfg.Notice
	svc, err := ownerchannel.New(ctx, svcCfg)
	if err != nil {
		_ = journal.Close()
		return nil, err
	}
	t := &Trial{Service: svc, door: standIn, host: host, sc: sc, now: now,
		stamp: strconv.FormatInt(started.Unix(), 36)}
	loopCtx, cancel := context.WithCancel(context.Background())
	t.cancel = cancel
	if !cfg.Manual {
		every := cfg.TickEvery
		if every == 0 {
			every = DefaultTickEvery
		}
		t.wg.Add(1)
		go func() {
			defer t.wg.Done()
			tk := time.NewTicker(every)
			defer tk.Stop()
			for {
				select {
				case <-loopCtx.Done():
					return
				case <-tk.C:
				}
				if err := svc.Tick(loopCtx); err != nil && loopCtx.Err() == nil && cfg.Notice != nil {
					cfg.Notice("the service's tick: " + oneLine(err.Error()))
				}
			}
		}()
	}
	return t, nil
}

// Close stops the tick and closes the service, which closes the journal. It
// may be called more than once.
func (t *Trial) Close() error {
	t.once.Do(func() {
		t.cancel()
		t.wg.Wait()
		t.err = t.Service.Close()
	})
	return t.err
}

// trialEnder ends a request at the stand-in door.
type trialEnder struct{ d *doortest.Door }

func (e trialEnder) EndRequest(_ context.Context, id string) error { return e.d.Withdraw(id) }

// TrialCard is what [Trial.Submit] made of one sample request: a card
// (pending, with its id) or a refusal (refused, with the code and the
// host-written detail).
type TrialCard struct {
	Kind      string `json:"kind"`
	RequestID string `json:"request_id"`
	// Feature is the sample feature of a finish, empty for a prune.
	Feature   string `json:"feature,omitempty"`
	State     string `json:"state"`
	PendingID string `json:"pending_id,omitempty"`
	Code      string `json:"code,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

// trialWords name the sample features, in turn.
var trialWords = []string{"login-retry", "cache-trim", "queue-limit", "log-rotate", "seat-banner", "docs-typos"}

// next returns the number of the next sample.
func (t *Trial) next() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.n++
	return t.n
}

// trialNote is the lead's note of a sample: it says on the card that no lead
// wrote it.
func trialNote(n int) record.Text {
	return record.Text(fmt.Sprintf("TRIAL sample card %d, made by ataniqd trial submit: no lead asked for it, and approving it applies nothing.", n))
}

// Submit makes one sample card of the kind given ([TrialFinish] or
// [TrialPrune]) and returns what became of it. A finish goes through the
// service's Submit, as a lead's request does: the stand-in door admits it, the
// trial host stands for the merge, the suite and the review, and the door
// plans it. A prune is admitted and planned at the stand-in door directly. A
// refusal (the door's caps, a duplicate prune) is a card in the state refused,
// not an error.
func (t *Trial) Submit(ctx context.Context, kind string) (TrialCard, error) {
	switch kind {
	case TrialFinish:
		return t.submitFinish(ctx)
	case TrialPrune:
		return t.submitPrune(ctx)
	}
	return TrialCard{}, errors.New("hostadapter: the kind of a trial card is finish or prune")
}

// request makes the bytes of a sample request.
func (t *Trial) request(n int, verb string, argv []string, feature string, note record.Text) (record.Request, []byte, error) {
	req := record.Request{
		Schema:     record.SchemaRequest,
		RequestID:  fmt.Sprintf("rq-trial-%s-%d", t.stamp, n),
		Epoch:      t.sc.Seat.Epoch,
		Verb:       verb,
		Argv:       argv,
		FeatureSHA: feature,
		NoteSHA256: record.Digest([]byte(note)),
		At:         record.FormatTime(t.now()),
	}
	b, err := record.EncodeRequest(req)
	return req, b, err
}

func (t *Trial) submitFinish(ctx context.Context) (TrialCard, error) {
	n := t.next()
	name := fmt.Sprintf("trial-%s-%d", trialWords[(n-1)%len(trialWords)], n)
	tip := t.host.addFeature(name, t.stamp, n)
	note := trialNote(n)
	req, b, err := t.request(n, record.VerbFeatureFinish, []string{name}, tip, note)
	if err != nil {
		return TrialCard{}, err
	}
	st, err := t.Service.Submit(ctx, ownerchannel.Collected{Project: t.sc.Project, Request: b, Note: note, At: t.now()})
	if err != nil {
		return TrialCard{}, err
	}
	return TrialCard{Kind: TrialFinish, RequestID: req.RequestID, Feature: name, State: string(st.State),
		PendingID: st.PendingID, Code: string(st.Code), Detail: st.Detail}, nil
}

func (t *Trial) submitPrune(ctx context.Context) (TrialCard, error) {
	n := t.next()
	effects := t.host.addBranches(t.stamp, n, 2+n%2)
	note := trialNote(n)
	req, b, err := t.request(n, record.VerbBranchPrune, []string{}, "", note)
	if err != nil {
		return TrialCard{}, err
	}
	out := TrialCard{Kind: TrialPrune, RequestID: req.RequestID}
	refused := func(err error) (TrialCard, error) {
		var e *record.Error
		if !errors.As(err, &e) {
			return TrialCard{}, err
		}
		out.State, out.Code, out.Detail = string(ownerchannel.RequestRefused), string(e.Code), e.Detail
		return out, nil
	}
	if _, err := t.door.Admit(ctx, door.RecordOf(b)); err != nil {
		return refused(err)
	}
	rec, err := t.door.Plan(ctx, door.PlanInput{Request: door.RecordOf(b), Note: note, Evidence: []record.Evidence{}, Deletes: effects})
	if err != nil {
		_ = t.door.Withdraw(req.RequestID)
		return refused(err)
	}
	p, err := record.DecodePending(rec.Bytes)
	if err != nil {
		return TrialCard{}, err
	}
	// The card was planned at the door, not through the service: a tick
	// lets a held wait (the phone's live refresh) look at the door again.
	if err := t.Service.Tick(ctx); err != nil && !errors.Is(err, ownerchannel.ErrDoorUnavailable) {
		return TrialCard{}, err
	}
	out.State, out.PendingID = string(ownerchannel.RequestPending), p.ID
	return out, nil
}

// trialHost is the host's side of a trial card: the Merger, the Intake, the
// Verifier and the Mailbox of the service, in memory. Its object ids are made
// up (digests of labels), the target's tip never moves, its suite record is a
// label that says no suite ran, and its Apply moves nothing. It runs no git and
// reads and writes no file.
type trialHost struct {
	project, target string
	// tip is the target's tip in the trial; nothing moves it.
	tip string
	// rules flag the sample paths, as the intake's review does, and ruleSHA
	// is their digest.
	rules   execconfig.Set
	ruleSHA string

	mu       sync.Mutex
	features map[string]trialFeature
	merges   map[string]trialReview
	branches map[string]string
	pins     map[string]string
	evidence map[string]record.Evidence
	applies  int
	replies  int
}

// trialFeature is one sample feature: its tip and its changes.
type trialFeature struct {
	tip     string
	changes []trialChange
}

// trialChange is one changed path of a sample.
type trialChange struct {
	path, status     string
	oldMode, newMode string
	added, removed   []string
}

// trialReview is the review of one sample merge.
type trialReview struct {
	base, merge, digest string
	changes             []trialChange
}

func newTrialHost(project, target string) *trialHost {
	rules := execconfig.Default()
	return &trialHost{
		project: project, target: target, tip: trialID("trial target", project, target),
		rules: rules, ruleSHA: execconfig.Digest(rules),
		features: map[string]trialFeature{}, merges: map[string]trialReview{},
		branches: map[string]string{}, pins: map[string]string{}, evidence: map[string]record.Evidence{},
	}
}

// trialID is a made-up object id: the first 40 hexadecimal digits of the
// SHA-256 of the parts.
func trialID(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])[:40]
}

// trialDigest is the SHA-256 of the parts in hexadecimal.
func trialDigest(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

// addFeature makes the sample feature name with its changes and returns its
// tip. An odd sample also changes the Makefile, a path the rules flag.
func (h *trialHost) addFeature(name, stamp string, n int) string {
	word := trialWords[(n-1)%len(trialWords)]
	pkg := strings.ReplaceAll(word, "-", "")
	changes := []trialChange{
		{path: "internal/" + pkg + "/" + pkg + ".go", status: "M", oldMode: "100644", newMode: "100644",
			added: []string{"\tif n > limit {", "\t\treturn errTooMany", "\t}"}, removed: []string{"\t// TODO: bound n"}},
		{path: "internal/" + pkg + "/" + pkg + "_test.go", status: "A", oldMode: "000000", newMode: "100644",
			added: []string{"package " + pkg, "", "func TestLimit(t *testing.T) {", "\tif err := run(limit + 1); err == nil {", "\t\tt.Fatal(\"no error past the limit\")", "\t}", "}"}},
		{path: "docs/" + word + ".md", status: "M", oldMode: "100644", newMode: "100644",
			added: []string{"A request past the limit is refused."}, removed: []string{"There is no limit."}},
	}
	if n%2 == 1 {
		changes = append(changes, trialChange{path: "Makefile", status: "M", oldMode: "100644", newMode: "100644",
			added: []string{"test-" + word + ":", "\tsh scripts/go-tool.sh test ./internal/" + pkg + "/"}})
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].path < changes[j].path })
	tip := trialID("trial feature", stamp, name)
	h.mu.Lock()
	h.features[name] = trialFeature{tip: tip, changes: changes}
	h.mu.Unlock()
	return tip
}

// addBranches makes k merged sample branches and returns the effects of a
// prune that deletes them.
func (h *trialHost) addBranches(stamp string, n, k int) []record.Effect {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []record.Effect
	for i := 1; i <= k; i++ {
		ref := fmt.Sprintf("refs/heads/feature/trial-done-%d-%d", n, i)
		tip := trialID("trial branch", stamp, ref)
		h.branches[ref] = tip
		out = append(out, record.Effect{Kind: record.EffectRef, Ref: ref, Old: tip, New: strings.Repeat("0", 40),
			MergedInto: &record.MergedInto{Ref: h.target, Tip: h.tip}})
	}
	return out
}

// errTrialUnknown is the refusal of a name or a commit the trial did not make.
func errTrialUnknown(what string) error {
	return &record.Error{Code: record.CodeRequestInvalid, Detail: "the trial made no such " + what}
}

// oneProject refuses a project other than the trial's one.
func (h *trialHost) oneProject(project string) error {
	if project != h.project {
		return errors.New("hostadapter: the trial serves one project")
	}
	return nil
}

// Resolve is the Intake's: the target's tip and the sample feature's.
func (h *trialHost) Resolve(_ context.Context, in ownerchannel.ResolveInput) (ownerchannel.Resolved, error) {
	if err := h.oneProject(in.Project); err != nil {
		return ownerchannel.Resolved{}, err
	}
	if len(in.Request.Argv) != 1 {
		return ownerchannel.Resolved{}, errTrialUnknown("feature")
	}
	h.mu.Lock()
	f, ok := h.features[in.Request.Argv[0]]
	h.mu.Unlock()
	if !ok {
		return ownerchannel.Resolved{}, errTrialUnknown("feature")
	}
	return ownerchannel.Resolved{TargetRef: h.target, TargetTip: h.tip, FeatureTip: f.tip}, nil
}

// Build is the Merger's: a made-up merge of the target's tip and the
// feature's, pinned in memory.
func (h *trialHost) Build(_ context.Context, in ownerchannel.MergeInput) (ownerchannel.BuiltMerge, error) {
	if err := h.oneProject(in.Project); err != nil {
		return ownerchannel.BuiltMerge{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	f, ok := h.features[in.Feature]
	if !ok || f.tip != in.FeatureTip || in.TargetTip != h.tip {
		return ownerchannel.BuiltMerge{}, errTrialUnknown("feature")
	}
	merge := trialID("trial merge", in.TargetTip, in.FeatureTip, in.Key)
	parts := []string{"trial review", in.TargetTip, merge}
	for _, c := range f.changes {
		parts = append(parts, c.status, c.path, strings.Join(c.added, "\n"), strings.Join(c.removed, "\n"))
	}
	h.merges[merge] = trialReview{base: in.TargetTip, merge: merge, digest: trialDigest(parts...), changes: f.changes}
	h.pins[in.Key] = merge
	return ownerchannel.BuiltMerge{Commit: merge, Parents: [2]string{in.TargetTip, in.FeatureTip}}, nil
}

// Pinned is the Merger's.
func (h *trialHost) Pinned(_ context.Context, key string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pins[key], nil
}

// Unpin is the Merger's.
func (h *trialHost) Unpin(_ context.Context, key, commit string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.pins[key] == commit {
		delete(h.pins, key)
	}
	return nil
}

// Verify is the Verifier's: no suite runs. The record it names says so, and
// the trial keeps it as the newest, so that a use finds it standing.
func (h *trialHost) Verify(_ context.Context, in ownerchannel.VerifyInput) (ownerchannel.Verification, error) {
	if err := h.oneProject(in.Project); err != nil {
		return ownerchannel.Verification{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.merges[in.Merge]; !ok {
		return ownerchannel.Verification{Detail: ownerchannel.VerifyCouldNotRun}, nil
	}
	ev := record.Evidence{Kind: record.EvidenceSuite, RecordID: "trial-no-suite-ran-" + in.Merge[:12],
		RecordSHA256: trialDigest("trial: no suite ran on the merge", in.Merge)}
	h.evidence[ev.RecordID] = ev
	return ownerchannel.Verification{Clean: true, Evidence: ev}, nil
}

// review returns the review of a merge.
func (h *trialHost) review(project, base, merge string) (trialReview, error) {
	if err := h.oneProject(project); err != nil {
		return trialReview{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	r, ok := h.merges[merge]
	if !ok || r.base != base {
		return trialReview{}, errTrialUnknown("merge")
	}
	return r, nil
}

// paths returns the path changes of a review.
func (h *trialHost) paths(r trialReview) []record.PathChange {
	out := make([]record.PathChange, 0, len(r.changes))
	for _, c := range r.changes {
		var bytes int64
		for _, l := range c.added {
			bytes += int64(len(l)) + 1
		}
		out = append(out, record.PathChange{Path: record.Text(c.path), Status: c.status, OldMode: c.oldMode, NewMode: c.newMode,
			Added: int64(len(c.added)), Removed: int64(len(c.removed)), NewBytes: bytes})
	}
	return out
}

// Review is the Intake's: the summary of the sample's changes, the paths the
// built-in rules flag among them, and the rules' digest.
func (h *trialHost) Review(_ context.Context, in ownerchannel.ReviewInput) (record.Summary, error) {
	r, err := h.review(in.Project, in.Base, in.Merge)
	if err != nil {
		return record.Summary{}, err
	}
	s := record.Summary{
		ReviewDigest: r.digest, Base: r.base, Tip: r.merge,
		Paths: h.paths(r), Flagged: []record.Flag{},
		Binary: []record.Text{}, Symlinks: []record.Text{}, ModeChanges: []record.Text{}, Submodules: []record.Text{},
		ExecConfigSet: record.ExecConfigSet{Matcher: execconfig.MatcherVersion, SHA256: h.ruleSHA, Additions: int64(execconfig.Additions(h.rules))},
	}
	for _, p := range s.Paths {
		s.Files++
		s.Added += p.Added
		s.Removed += p.Removed
		s.NewBytes += p.NewBytes
		if m, ok := h.rules.Match(string(p.Path)); ok {
			s.Flagged = append(s.Flagged, record.Flag{Kind: record.FlagExecConfig, Path: p.Path, Pattern: m.Pattern})
		}
	}
	return s, nil
}

// TargetTip is the Intake's: the target's tip, which never moves.
func (h *trialHost) TargetTip(_ context.Context, project, ref string) (string, error) {
	if err := h.oneProject(project); err != nil {
		return "", err
	}
	if ref != h.target {
		return "", errTrialUnknown("target")
	}
	return h.tip, nil
}

// Paths is the Intake's.
func (h *trialHost) Paths(_ context.Context, ref ownerchannel.ReviewRef) (ownerchannel.ReviewPaths, error) {
	r, err := h.review(ref.Project, ref.Base, ref.Merge)
	if err != nil {
		return ownerchannel.ReviewPaths{}, err
	}
	return ownerchannel.ReviewPaths{ReviewDigest: r.digest, Paths: h.paths(r)}, nil
}

// Patch is the Intake's: a unified diff of the sample's changes.
func (h *trialHost) Patch(_ context.Context, ref ownerchannel.ReviewRef) (ownerchannel.ReviewPatch, error) {
	r, err := h.review(ref.Project, ref.Base, ref.Merge)
	if err != nil {
		return ownerchannel.ReviewPatch{}, err
	}
	var lines []string
	for _, c := range r.changes {
		from, to := "a/"+c.path, "b/"+c.path
		lines = append(lines, "diff --git "+from+" "+to)
		if c.status == "A" {
			lines = append(lines, "new file mode "+c.newMode)
			from = "/dev/null"
		}
		lines = append(lines, "--- "+from, "+++ "+to,
			fmt.Sprintf("@@ -1,%d +1,%d @@", len(c.removed), len(c.added)))
		for _, l := range c.removed {
			lines = append(lines, "-"+l)
		}
		for _, l := range c.added {
			lines = append(lines, "+"+l)
		}
	}
	return ownerchannel.ReviewPatch{ReviewDigest: r.digest, Lines: lines}, nil
}

// Fresh is the Intake's: what the trial shows now, which is what it showed
// when the card was planned. The door compares it with the binding as it does
// for any card.
func (h *trialHost) Fresh(_ context.Context, in ownerchannel.FreshInput) (door.Fresh, error) {
	if err := h.oneProject(in.Project); err != nil {
		return door.Fresh{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var out door.Fresh
	for _, e := range in.Pending.Binding.Effects {
		v, ok := h.branches[e.Ref]
		if e.Ref == h.target {
			v, ok = h.tip, true
		}
		if !ok {
			return door.Fresh{}, errTrialUnknown("ref")
		}
		out.Refs = append(out.Refs, door.RefValue{Ref: e.Ref, Value: v})
	}
	if s := in.Pending.Binding.Summary; s != nil {
		r, ok := h.merges[s.Tip]
		if !ok {
			return door.Fresh{}, errTrialUnknown("merge")
		}
		out.ReviewDigest, out.RuleSetSHA256 = r.digest, h.ruleSHA
	}
	for _, e := range in.Pending.Binding.Evidence {
		ev, ok := h.evidence[e.RecordID]
		out.Evidence = append(out.Evidence, door.EvidenceState{RecordID: e.RecordID, RecordSHA256: ev.RecordSHA256, Newest: ok})
	}
	return out, nil
}

// Apply is the Intake's: it moves nothing, and so the outcome is
// not-applied, with the line [TrialLine].
func (h *trialHost) Apply(context.Context, ownerchannel.ApplyInput) (door.Outcome, error) {
	h.mu.Lock()
	h.applies++
	h.mu.Unlock()
	return door.OutcomeNotApplied, nil
}

// Mirror is the Intake's: nothing was ever applied.
func (h *trialHost) Mirror(context.Context, ownerchannel.ApplyInput) (door.Outcome, error) {
	return door.OutcomeNotApplied, nil
}

// OutcomeLine makes the trial's host an [ownerchannel.OutcomeLiner]: every
// outcome says that nothing was applied.
func (h *trialHost) OutcomeLine(door.Outcome) string { return TrialLine }

// Reply is the Mailbox's: no lead asked, so the reply is counted and dropped.
func (h *trialHost) Reply(context.Context, ownerchannel.Reply) error {
	h.mu.Lock()
	h.replies++
	h.mu.Unlock()
	return nil
}

var (
	_ ownerchannel.Merger       = (*trialHost)(nil)
	_ ownerchannel.Intake       = (*trialHost)(nil)
	_ ownerchannel.Verifier     = (*trialHost)(nil)
	_ ownerchannel.Mailbox      = (*trialHost)(nil)
	_ ownerchannel.OutcomeLiner = (*trialHost)(nil)
)
