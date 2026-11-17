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

	suffix    string
	unit      string
	intent    string
	readyPath string
	mayExist  bool
	first     leadunit.State // what Start returned: it can be partial
	started   leadunit.State // the settled state: active, with its invocation and control group
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
		Unit:    q.unit,
		Epoch:   1,
		Intent:  intent,
		Argv:    []string{q.exe, "child", "--ignore-term", "--ready-file", q.readyPath},
		WorkDir: q.stateDir,
		Env:     []string{"LANG=C"},
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
		{"second-start-refused", func(ctx context.Context) (string, error) { return q.secondStart(ctx, spec) }},
		{"inspect-matches", q.inspectMatches},
		{"sigterm-leaves-group-populated", q.sigterm},
		{"sigkill-sent", func(ctx context.Context) (string, error) {
			return "SIGKILL sent to the control group", q.mgr.Signal(ctx, q.started, syscall.SIGKILL)
		}},
		{"empty-true", q.emptyTrue},
		{"unit-not-loaded", q.notLoaded},
	}
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
	// From the call on the unit may exist, unless the manager says it refused.
	q.mayExist = true
	st, err := q.mgr.Start(ctx, spec)
	if err != nil {
		switch {
		case errors.Is(err, leadunit.ErrReplyLost):
			return "", fmt.Errorf("start reply lost, the unit may be running (cleanup inspects it): %w", err)
		case errors.Is(err, leadunit.ErrUnitExists), errors.Is(err, leadunit.ErrUnavailable), errors.Is(err, leadunit.ErrInvalidSpec):
			q.mayExist = false
		}
		return "", fmt.Errorf("start failed: %w", err)
	}
	q.first = st
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
		return "", errors.New("start returned a state that is " + strings.Join(bad, ", "))
	}
	return "the job was accepted and the unit is " + string(st.Active), nil
}

// settle waits for the unit to be active with its invocation id and control
// group, which systemd fills in after the job is accepted, and keeps that
// state as the one every later check compares with.
func (q *qualifier) settle(ctx context.Context) (string, error) {
	var st leadunit.State
	ok, err := q.poll(ctx, emptyBound, func() (bool, error) {
		var err error
		st, err = q.mgr.Inspect(ctx, q.unit)
		if err != nil {
			return false, err
		}
		return st.Loaded && st.Active == leadunit.ActiveActive && st.InvocationID != "" &&
			st.ControlGroup != "" && st.ControlGroupID != "" && st.MainPID > 0, nil
	})
	if !ok {
		return "", fmt.Errorf("the unit was not active with an invocation and a control group within %s (last error: %v; last state %+v)", emptyBound, err, st)
	}
	if q.first.InvocationID != "" && q.first.InvocationID != st.InvocationID {
		return "", fmt.Errorf("invocation %s differs from the one Start returned, %s", st.InvocationID, q.first.InvocationID)
	}
	q.started = st
	return "active, invocation " + st.InvocationID + ", control group " + st.ControlGroup, nil
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

// ours reports whether st is a unit that this run started: transient, with
// this run's epoch and intent in its description.
func (q *qualifier) ours(st leadunit.State) bool {
	return st.Ours && st.Epoch == 1 && st.Intent == q.intent
}

// cleanup always runs, on a context that outlives a cancelled run. It never
// signals a unit whose description is not this run's own intent, signals the
// unit it owns once its invocation is known, says what it did and did not do,
// and proves the control group empty or says it could not.
func (q *qualifier) cleanup(ctx context.Context) check {
	if !q.mayExist {
		return check{"cleanup", resultPass, "nothing was started"}
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupBound)
	defer cancel()
	// A run that stopped right after Start can find the unit loaded with no
	// invocation yet, which Signal refuses: wait, bounded, for it to be set.
	var cur leadunit.State
	ok, err := q.poll(cctx, emptyBound, func() (bool, error) {
		var ierr error
		cur, ierr = q.mgr.Inspect(cctx, q.unit)
		if ierr != nil {
			return false, ierr
		}
		return !cur.Loaded || !q.ours(cur) || cur.InvocationID != "", nil
	})
	if !ok {
		if err != nil {
			return check{"cleanup", resultFail, fmt.Sprintf("could not inspect %s, so it is not proven empty: %v", q.unit, err)}
		}
		return check{"cleanup", resultFail, fmt.Sprintf("not signalled: %s is loaded with no invocation id after %s, so it is not proven empty and ends by itself at the child's maximum life", q.unit, emptyBound)}
	}
	if cur.Loaded && !q.ours(cur) {
		return check{"cleanup", resultFail, fmt.Sprintf("not signalled: a unit named %s is loaded but its description %q is not this run's intent %q, so it was left alone and is not proven empty", q.unit, cur.Description, q.intent)}
	}
	prove := cur
	note := "the unit was already gone, nothing was sent"
	if !cur.Loaded && q.started.ControlGroup != "" {
		prove = q.started
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
		return check{"cleanup", resultFail, fmt.Sprintf("%s; the control group of %s could not be proven empty (last error: %v)", note, q.unit, err)}
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
