//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"ataniq/internal/leadunit"
)

// Timings of the run.
const (
	termWait     = 2 * time.Second // between SIGTERM and SIGKILL
	pollEvery    = 200 * time.Millisecond
	emptyBound   = 5 * time.Second  // for the group to empty after SIGKILL
	readyBound   = 10 * time.Second // for the child to say it ignores SIGTERM
	cleanupBound = 20 * time.Second
)

// Literal arguments of the child. systemd must hand them to the process as
// they are: no $VAR or ${VAR} expansion and no specifier ("%h" is the home
// folder, "%%" a percent sign, "%u" the user) for a transient unit's command.
const (
	literalDollar  = "a$HOME-${HOME}-b"
	literalPercent = "a%h-%%-%u-b"
)

// The result of one check.
const (
	resultPass    = "pass"
	resultFail    = "fail"
	resultSkipped = "skipped"
)

// check is one line of the report.
type check struct {
	Name   string `json:"name"`
	Result string `json:"result"`
	Detail string `json:"detail,omitempty"`
}

// report is what the harness prints.
type report struct {
	Tool         string   `json:"tool"`
	Status       string   `json:"status"`
	Unit         string   `json:"unit"`
	Intent       string   `json:"intent"`
	Checks       []check  `json:"checks"`
	NotAttempted []string `json:"not_attempted"`
	Verdict      string   `json:"verdict"`
}

// lifecycle is the real manager as the harness uses it: the contract plus a
// reading of the populated flag of a State's control group.
type lifecycle interface {
	leadunit.Manager
	Populated(ctx context.Context, st leadunit.State) (bool, error)
}

// qualifier holds the run. The functions are fields so that the logic can be
// driven without waiting and without a child process.
type qualifier struct {
	mgr      lifecycle
	sleep    func(ctx context.Context, d time.Duration) error
	ready    func(ctx context.Context, path string) error
	rand     func() (string, error)
	exe      string
	stateDir string

	// limits, when set, is what the qualification unit is started with, and
	// turns on the checks of it: read back with systemctl and in the control
	// group files, lowered once, and no runtime folder entry left after the
	// stop. The command always sets it, from its flags. A qualifier without it
	// starts the unit with no limits and makes none of those checks.
	limits *leadunit.Limits
	// lowered is the memory limit the running unit is lowered to once.
	lowered uint64
	// host runs systemctl and reads files for the limit checks.
	host host
	// cgroupRoot is where the unified control group hierarchy is mounted;
	// runtimeDir is the user's runtime folder. The control group files are
	// compared with the limits exactly: the command refuses a memory size that
	// is not a whole number of pages, so that there is nothing to round.
	cgroupRoot string
	runtimeDir string

	suffix    string
	unit      string
	intent    string
	readyPath string
	argsPath  string
	mayExist  bool
	first     leadunit.State // what Start returned: it can be partial
	started   leadunit.State // the settled state: active, with its invocation and control group

	// The bound child unit: a second unit, bound to the first, that must stop
	// when the first does.
	childUnit     string
	childIntent   string
	childMayExist bool
	childStarted  leadunit.State
}

func realSleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func randomHex() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// waitReadyFile waits for the child's ready file to hold a pid.
func waitReadyFile(ctx context.Context, path string) error {
	for waited := time.Duration(0); ; waited += 50 * time.Millisecond {
		if b, err := os.ReadFile(path); err == nil {
			if pid, perr := strconv.Atoi(strings.TrimSuffix(string(b), "\n")); perr == nil && pid > 0 && strings.HasSuffix(string(b), "\n") {
				return nil
			}
		}
		if waited >= readyBound {
			return errors.New("the child did not write its ready file in time")
		}
		if err := realSleep(ctx, 50*time.Millisecond); err != nil {
			return err
		}
	}
}

func (q *qualifier) spec(intent string) leadunit.Spec {
	return leadunit.Spec{
		Unit:   q.unit,
		Epoch:  1,
		Intent: intent,
		Argv: []string{q.exe, "child", "--ignore-term", "--ready-file", q.readyPath, "--args-file", q.argsPath,
			"--literal", literalDollar, "--literal", literalPercent},
		WorkDir: q.stateDir,
		Env:     []string{"LANG=C"},
		Limits:  q.limitsCopy(),
	}
}

// limitsCopy returns a copy of the limits the unit is started with, nil when
// there are none.
func (q *qualifier) limitsCopy() *leadunit.Limits {
	if q.limits == nil {
		return nil
	}
	l := *q.limits
	return &l
}

// childSpec is the unit that is bound to the qualification unit: a plain child
// that dies of SIGTERM (it does not ignore it), which is what systemd sends it
// when its parent stops.
func (q *qualifier) childSpec() leadunit.Spec {
	return leadunit.Spec{
		Unit:    q.childUnit,
		Epoch:   1,
		Intent:  q.childIntent,
		Argv:    []string{q.exe, "child"},
		WorkDir: q.stateDir,
		Env:     []string{"LANG=C"},
		Parent:  q.unit,
	}
}

type step struct {
	name string
	fn   func(ctx context.Context) (detail string, err error)
}

// qualify runs every check and always the cleanup.
func (q *qualifier) qualify(ctx context.Context) report {
	rep := report{
		Tool:         "leadunit-qualify",
		Status:       "provisional, unqualified",
		NotAttempted: []string{"lost start reply simulation: not attempted, a lost reply cannot be provoked safely against the real manager"},
		Checks:       []check{},
	}
	suffix, err := q.rand()
	if err != nil {
		rep.Checks = append(rep.Checks, check{"setup", resultFail, "no random name: " + err.Error()})
		rep.Verdict = resultFail
		return rep
	}
	q.suffix = suffix
	q.unit = "ataniq-qualify-" + suffix + ".service"
	q.readyPath = filepath.Join(q.stateDir, "ready-"+suffix)
	q.argsPath = filepath.Join(q.stateDir, "args-"+suffix)
	q.childUnit = "ataniq-qualify-" + suffix + "-child.service"
	q.childIntent = "qc-" + suffix
	intent := "q-" + suffix
	q.intent = intent
	rep.Unit, rep.Intent = q.unit, intent
	spec := q.spec(intent)

	steps := []step{
		{"record-unit-name", func(ctx context.Context) (string, error) {
			err := os.WriteFile(filepath.Join(q.stateDir, "unit-"+suffix), []byte(q.unit+"\n"), 0o600)
			return "unit name recorded before the start", err
		}},
		{"start", func(ctx context.Context) (string, error) { return q.start(ctx, spec) }},
		{"unit-active", q.settle},
		{"description-read-back", func(ctx context.Context) (string, error) { return q.description(spec) }},
		{"child-ready", func(ctx context.Context) (string, error) {
			rctx, cancel := context.WithTimeout(ctx, readyBound+time.Second)
			defer cancel()
			return "the child ignores SIGTERM", q.ready(rctx, q.readyPath)
		}},
		{"arguments-reach-the-child-unchanged", func(ctx context.Context) (string, error) { return q.argumentsUnchanged(spec) }},
		{"second-start-refused", func(ctx context.Context) (string, error) { return q.secondStart(ctx, spec) }},
		{"inspect-matches", q.inspectMatches},
	}
	if q.limits != nil {
		steps = append(steps,
			step{"limits-show-read-back", q.limitsShowReadBack},
			step{"limits-cgroup-read-back", q.limitsCgroupReadBack},
			step{"memory-limit-lowered", q.memoryLowered},
			step{"lowered-memory-limit-show-read-back", q.loweredShowReadBack},
			step{"lowered-memory-limit-cgroup-read-back", q.loweredCgroupReadBack},
		)
	}
	steps = append(steps, []step{
		{"bound-child-started", q.startChild},
		{"sigterm-leaves-group-populated", q.sigterm},
		{"sigkill-sent", func(ctx context.Context) (string, error) {
			return "SIGKILL sent to the control group", q.mgr.Signal(ctx, q.started, syscall.SIGKILL)
		}},
		{"empty-true", q.emptyTrue},
		{"unit-not-loaded", q.notLoaded},
		{"bound-child-stops-with-parent", q.childStops},
	}...)
	if q.limits != nil {
		steps = append(steps, step{"runtime-folders-clean-after-stop", q.runtimeFoldersClean})
	}
	steps = append(steps, step{"empty-true-for-a-fresh-name", q.emptyFreshName})
	failed := false
	for _, s := range steps {
		if failed {
			rep.Checks = append(rep.Checks, check{s.name, resultSkipped, "an earlier check failed"})
			continue
		}
		detail, err := s.fn(ctx)
		if err != nil {
			failed = true
			rep.Checks = append(rep.Checks, check{s.name, resultFail, err.Error()})
			continue
		}
		rep.Checks = append(rep.Checks, check{s.name, resultPass, detail})
	}
	cl := q.cleanup(ctx)
	rep.Checks = append(rep.Checks, cl)
	rep.Verdict = resultPass
	if failed || cl.Result != resultPass {
		rep.Verdict = resultFail
	}
	return rep
}

func (q *qualifier) start(ctx context.Context, spec leadunit.Spec) (string, error) {
	st, err := q.startUnit(ctx, spec, &q.mayExist)
	if err != nil {
		return "", err
	}
	q.first = st
	return "the job was accepted and the unit is " + string(st.Active), nil
}

// startUnit starts spec and checks the state Start returned. It sets *mayExist
// from the call on, unless the manager says it refused, so that the cleanup
// knows whether to look for the unit.
func (q *qualifier) startUnit(ctx context.Context, spec leadunit.Spec, mayExist *bool) (leadunit.State, error) {
	*mayExist = true
	st, err := q.mgr.Start(ctx, spec)
	if err != nil {
		switch {
		case errors.Is(err, leadunit.ErrReplyLost):
			return leadunit.State{}, fmt.Errorf("start reply lost, the unit may be running (cleanup inspects it): %w", err)
		case errors.Is(err, leadunit.ErrUnitExists), errors.Is(err, leadunit.ErrUnavailable), errors.Is(err, leadunit.ErrInvalidSpec):
			*mayExist = false
		}
		return leadunit.State{}, fmt.Errorf("start failed: %w", err)
	}
	var bad []string
	if !st.Loaded {
		bad = append(bad, "not loaded")
	}
	if st.Active != leadunit.ActiveActive && st.Active != leadunit.ActiveActivating {
		bad = append(bad, "active state "+string(st.Active))
	}
	if st.BootID == "" {
		bad = append(bad, "no boot id")
	}
	if len(bad) > 0 {
		return leadunit.State{}, errors.New("start returned a state that is " + strings.Join(bad, ", "))
	}
	return st, nil
}

// settle waits for the unit to be active with its invocation id and control
// group, which systemd fills in after the job is accepted, and keeps that
// state as the one every later check compares with.
func (q *qualifier) settle(ctx context.Context) (string, error) {
	st, err := q.settleUnit(ctx, q.unit, q.first)
	if err != nil {
		return "", err
	}
	q.started = st
	return "active, invocation " + st.InvocationID + ", control group " + st.ControlGroup, nil
}

// settleUnit waits for unit to be active with an invocation id and a control
// group and returns that state. first is what Start returned for it.
func (q *qualifier) settleUnit(ctx context.Context, unit string, first leadunit.State) (leadunit.State, error) {
	var st leadunit.State
	ok, err := q.poll(ctx, emptyBound, func() (bool, error) {
		var err error
		st, err = q.mgr.Inspect(ctx, unit)
		if err != nil {
			return false, err
		}
		return st.Loaded && st.Active == leadunit.ActiveActive && st.InvocationID != "" &&
			st.ControlGroup != "" && st.ControlGroupID != "" && st.MainPID > 0, nil
	})
	if !ok {
		return leadunit.State{}, fmt.Errorf("%s was not active with an invocation and a control group within %s (last error: %v; last state %+v)", unit, emptyBound, err, st)
	}
	if first.InvocationID != "" && first.InvocationID != st.InvocationID {
		return leadunit.State{}, fmt.Errorf("invocation %s differs from the one Start returned, %s", st.InvocationID, first.InvocationID)
	}
	return st, nil
}

// startChild starts the unit that is bound to the qualification unit and checks
// that Inspect reads the qualification unit back as its parent.
func (q *qualifier) startChild(ctx context.Context) (string, error) {
	spec := q.childSpec()
	first, err := q.startUnit(ctx, spec, &q.childMayExist)
	if err != nil {
		return "", fmt.Errorf("bound child %s: %w", q.childUnit, err)
	}
	st, err := q.settleUnit(ctx, q.childUnit, first)
	if err != nil {
		return "", fmt.Errorf("bound child: %w", err)
	}
	q.childStarted = st
	if st.Description != spec.Description() || !st.Ours || st.Epoch != spec.Epoch || st.Intent != spec.Intent {
		return "", fmt.Errorf("bound child: description %q read back as ours=%v epoch=%d intent=%q", st.Description, st.Ours, st.Epoch, st.Intent)
	}
	if st.Parent != q.unit {
		return "", fmt.Errorf("bound child: Inspect reads its parent as %q, it was started bound to %q", st.Parent, q.unit)
	}
	return q.childUnit + " is active and Inspect reads " + q.unit + " back as its parent", nil
}

// childStops checks, after the qualification unit is gone, that the unit bound
// to it was stopped by systemd with it, and that its control group is proven
// empty. Nothing here signals the child: the cleanup would, and a pass must
// not owe anything to it.
func (q *qualifier) childStops(ctx context.Context) (string, error) {
	ok, err := q.poll(ctx, emptyBound, func() (bool, error) {
		st, err := q.mgr.Inspect(ctx, q.childUnit)
		return err == nil && !st.Loaded, err
	})
	if !ok {
		return "", fmt.Errorf("the bound child %s is still loaded or cannot be inspected %s after its parent was gone (last error: %v)", q.childUnit, emptyBound, err)
	}
	ok, err = q.poll(ctx, emptyBound, func() (bool, error) { return q.mgr.Empty(ctx, q.childStarted) })
	if !ok {
		return "", fmt.Errorf("Empty did not become true for the bound child within %s (last error: %v)", emptyBound, err)
	}
	return "the bound child is not loaded and its control group is proven empty, with no signal sent to it", nil
}

func (q *qualifier) description(spec leadunit.Spec) (string, error) {
	for _, st := range []leadunit.State{q.first, q.started} {
		if st.Description != spec.Description() {
			return "", fmt.Errorf("description %q, want %q", st.Description, spec.Description())
		}
		if !st.Ours || st.Epoch != spec.Epoch || st.Intent != spec.Intent {
			return "", fmt.Errorf("description read back as ours=%v epoch=%d intent=%q", st.Ours, st.Epoch, st.Intent)
		}
	}
	return "description " + strconv.Quote(spec.Description()) + " read back by Start and by Inspect as epoch and intent", nil
}

// argumentsUnchanged compares what the child saw as its arguments, which it
// wrote before its ready file, with what the harness sent, among them one
// element with $HOME and one with %h.
func (q *qualifier) argumentsUnchanged(spec leadunit.Spec) (string, error) {
	b, err := os.ReadFile(q.argsPath)
	if err != nil {
		return "", fmt.Errorf("the child's argument record: %w", err)
	}
	var got []string
	if err := json.Unmarshal(b, &got); err != nil {
		return "", fmt.Errorf("the child's argument record is not a list of strings: %w", err)
	}
	want := spec.Argv[2:] // the child saw what follows "child"
	if len(got) != len(want) {
		return "", fmt.Errorf("the child saw %d arguments %q, the unit was started with %d %q", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			return "", fmt.Errorf("argument %d reached the child as %q, it was sent as %q", i, got[i], want[i])
		}
	}
	return "the arguments, including " + strconv.Quote(literalDollar) + " and " + strconv.Quote(literalPercent) + ", reached the child unchanged", nil
}

// emptyFreshName checks Empty case 3 on a unit name that was never used: the
// State has no invocation and no control group, so the answer rests on the
// unit not being loaded and no group of that name existing under the user
// manager, which needs the manager's own control group to be derived.
func (q *qualifier) emptyFreshName(ctx context.Context) (string, error) {
	suffix, err := q.rand()
	if err != nil {
		return "", fmt.Errorf("no random name: %w", err)
	}
	if suffix == q.suffix {
		return "", errors.New("the random name repeated, so it is not a fresh one")
	}
	unit := "ataniq-qualify-" + suffix + ".service"
	ok, err := q.mgr.Empty(ctx, leadunit.State{Unit: unit})
	if err != nil {
		return "", fmt.Errorf("Empty for %s: %w", unit, err)
	}
	if !ok {
		return "", fmt.Errorf("Empty for the never-used %s is false", unit)
	}
	return "Empty is true for the never-used " + unit + " (case 3: not loaded and no group of that name)", nil
}

func (q *qualifier) secondStart(ctx context.Context, spec leadunit.Spec) (string, error) {
	_, err := q.mgr.Start(ctx, spec)
	switch {
	case errors.Is(err, leadunit.ErrUnitExists):
		return "the second start was refused with ErrUnitExists", nil
	case err == nil:
		return "", errors.New("the second start with the same name succeeded")
	default:
		return "", fmt.Errorf("the second start was not refused as ErrUnitExists: %w", err)
	}
}

func (q *qualifier) inspectMatches(ctx context.Context) (string, error) {
	st, err := q.mgr.Inspect(ctx, q.unit)
	if err != nil {
		return "", fmt.Errorf("inspect: %w", err)
	}
	w := q.started
	var diff []string
	cmp := func(name string, got, want any) {
		if got != want {
			diff = append(diff, fmt.Sprintf("%s %v, started with %v", name, got, want))
		}
	}
	cmp("loaded", st.Loaded, w.Loaded)
	cmp("invocation", st.InvocationID, w.InvocationID)
	cmp("control group", st.ControlGroup, w.ControlGroup)
	cmp("control group identity", st.ControlGroupID, w.ControlGroupID)
	cmp("main pid", st.MainPID, w.MainPID)
	cmp("boot", st.BootID, w.BootID)
	cmp("description", st.Description, w.Description)
	cmp("ours", st.Ours, w.Ours)
	cmp("epoch", st.Epoch, w.Epoch)
	cmp("intent", st.Intent, w.Intent)
	if len(diff) > 0 {
		return "", errors.New("inspect differs from the start: " + strings.Join(diff, "; "))
	}
	return "inspect equals the state of the start", nil
}

func (q *qualifier) sigterm(ctx context.Context) (string, error) {
	if err := q.mgr.Signal(ctx, q.started, syscall.SIGTERM); err != nil {
		return "", fmt.Errorf("SIGTERM: %w", err)
	}
	if err := q.sleep(ctx, termWait); err != nil {
		return "", err
	}
	st, err := q.mgr.Inspect(ctx, q.unit)
	if err != nil {
		return "", fmt.Errorf("inspect after SIGTERM: %w", err)
	}
	if !st.Loaded || st.InvocationID != q.started.InvocationID {
		return "", fmt.Errorf("after SIGTERM the unit is loaded=%v invocation=%q: the child did not ignore it", st.Loaded, st.InvocationID)
	}
	pop, err := q.mgr.Populated(ctx, q.started)
	if err != nil {
		return "", fmt.Errorf("populated after SIGTERM: %w", err)
	}
	if !pop {
		return "", errors.New("the control group is empty after SIGTERM: the child did not ignore it")
	}
	return "after SIGTERM and 2 s the unit is still loaded with the same invocation and its control group is populated", nil
}

func (q *qualifier) emptyTrue(ctx context.Context) (string, error) {
	ok, err := q.poll(ctx, emptyBound, func() (bool, error) { return q.mgr.Empty(ctx, q.started) })
	if !ok {
		return "", fmt.Errorf("Empty did not become true within %s after SIGKILL (last error: %v)", emptyBound, err)
	}
	return "Empty is true", nil
}

func (q *qualifier) notLoaded(ctx context.Context) (string, error) {
	ok, err := q.poll(ctx, emptyBound, func() (bool, error) {
		st, err := q.mgr.Inspect(ctx, q.unit)
		return err == nil && !st.Loaded, err
	})
	if !ok {
		return "", fmt.Errorf("the unit is still loaded or cannot be inspected after SIGKILL (last error: %v)", err)
	}
	return "Inspect answers not loaded", nil
}

// poll calls f every pollEvery until it says true or bound has passed, on the
// injected sleep. It returns the last error of f when it never said true.
func (q *qualifier) poll(ctx context.Context, bound time.Duration, f func() (bool, error)) (bool, error) {
	for waited := time.Duration(0); ; waited += pollEvery {
		ok, err := f()
		if ok {
			return true, nil
		}
		if waited >= bound {
			return false, err
		}
		if serr := q.sleep(ctx, pollEvery); serr != nil {
			return false, serr
		}
	}
}

// ours reports whether st is a unit that this run started with that intent:
// transient (the manager reads a description as ours only for a transient
// unit), with this run's epoch and the intent in its description. A unit of
// the same name that is not transient is not ours, whatever its description
// says.
func ours(st leadunit.State, intent string) bool {
	return st.Ours && st.Epoch == 1 && st.Intent == intent
}

// cleanup always runs, on a context that outlives a cancelled run. It cleans up
// the qualification unit and, when it was started, the unit bound to it, and
// returns one check that passes only when both do.
func (q *qualifier) cleanup(ctx context.Context) check {
	main := q.cleanupUnit(ctx, q.unit, q.intent, q.mayExist, q.started)
	if !q.childMayExist {
		return main
	}
	child := q.cleanupUnit(ctx, q.childUnit, q.childIntent, true, q.childStarted)
	res := resultPass
	if main.Result != resultPass || child.Result != resultPass {
		res = resultFail
	}
	return check{"cleanup", res, main.Detail + "; bound child: " + child.Detail}
}

// cleanupUnit cleans up one unit of the run. It never signals a unit whose
// description is not the run's own intent for it, signals the unit it owns once
// its invocation is known, says what it did and did not do, and proves the
// control group empty or says it could not. started is the settled state of the
// unit, when there is one.
func (q *qualifier) cleanupUnit(ctx context.Context, unit, intent string, mayExist bool, started leadunit.State) check {
	if !mayExist {
		return check{"cleanup", resultPass, "nothing was started"}
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupBound)
	defer cancel()
	// A run that stopped right after Start can find the unit loaded with no
	// invocation yet, which Signal refuses: wait, bounded, for it to be set.
	var cur leadunit.State
	ok, err := q.poll(cctx, emptyBound, func() (bool, error) {
		var ierr error
		cur, ierr = q.mgr.Inspect(cctx, unit)
		if ierr != nil {
			return false, ierr
		}
		return !cur.Loaded || !ours(cur, intent) || cur.InvocationID != "", nil
	})
	if !ok {
		if err != nil {
			return check{"cleanup", resultFail, fmt.Sprintf("could not inspect %s, so it is not proven empty: %v", unit, err)}
		}
		return check{"cleanup", resultFail, fmt.Sprintf("not signalled: %s is loaded with no invocation id after %s, so it is not proven empty and ends by itself at the child's maximum life", unit, emptyBound)}
	}
	if cur.Loaded && !ours(cur, intent) {
		return check{"cleanup", resultFail, fmt.Sprintf("not signalled: a unit named %s is loaded but its description %q is not this run's intent %q, so it was left alone and is not proven empty", unit, cur.Description, intent)}
	}
	prove := cur
	note := "the unit was already gone, nothing was sent"
	if !cur.Loaded && started.ControlGroup != "" {
		prove = started
	}
	if cur.Loaded {
		switch serr := q.mgr.Signal(cctx, cur, syscall.SIGKILL); {
		case serr == nil:
			note = "SIGKILL sent"
		case errors.Is(serr, leadunit.ErrNotLoaded):
			note = "SIGKILL not sent: the unit was gone before the signal"
		default:
			note = "SIGKILL not sent (" + serr.Error() + ")"
		}
	}
	ok, err = q.poll(cctx, cleanupBound-emptyBound-time.Second, func() (bool, error) { return q.mgr.Empty(cctx, prove) })
	if !ok {
		return check{"cleanup", resultFail, fmt.Sprintf("%s; the control group of %s could not be proven empty (last error: %v)", note, unit, err)}
	}
	return check{"cleanup", resultPass, note + "; the control group is proven empty"}
}

// saveReport writes the report and removes the ready marker; it returns the
// error of the report file.
func (q *qualifier) saveReport(rep report) error {
	if q.suffix == "" {
		return nil
	}
	_ = os.Remove(q.readyPath)
	_ = os.Remove(q.argsPath)
	var buf bytes.Buffer
	if err := writeReport(&buf, rep); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(q.stateDir, "report-"+q.suffix+".json"), buf.Bytes(), 0o600)
}

// writeReport prints the report as one JSON object with one line per check.
func writeReport(w io.Writer, rep report) error {
	enc := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			panic(err) // only strings and slices of strings are encoded
		}
		return string(b)
	}
	var b strings.Builder
	b.WriteString("{\n")
	fmt.Fprintf(&b, "  \"tool\": %s,\n  \"status\": %s,\n  \"unit\": %s,\n  \"intent\": %s,\n", enc(rep.Tool), enc(rep.Status), enc(rep.Unit), enc(rep.Intent))
	b.WriteString("  \"checks\": [\n")
	for i, c := range rep.Checks {
		sep := ","
		if i == len(rep.Checks)-1 {
			sep = ""
		}
		fmt.Fprintf(&b, "    %s%s\n", enc(c), sep)
	}
	b.WriteString("  ],\n")
	fmt.Fprintf(&b, "  \"not_attempted\": %s,\n  \"verdict\": %s\n}\n", enc(rep.NotAttempted), enc(rep.Verdict))
	_, err := io.WriteString(w, b.String())
	return err
}
