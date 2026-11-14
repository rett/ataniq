//go:build linux && amd64

package qualification

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
)

// This is an entry-only lifecycle, not a workflow engine for memory campaigns.
// All manager work and frozen subject checks precede wrapper entry. Afterwards
// readiness and actor binding use direct bounded metadata observations only.
func (c *Campaign) runEntry(a *Attempt, h hostIO) (result error) {
	if a.Phase != "prepared" || a.EntryRuntime != nil || a.EntryIntegrityFailure != "" {
		return errors.New("entry run cannot replay")
	}
	start := entryNow(h)
	// 45s controller operation bound leaves 15s within the immutable attempt
	// horizon for stop/collection. Wrapper 10s, actors 8/10s, Tool guard 30s.
	deadline := start.Add(45 * time.Second)
	if end := time.Unix(a.NotAfter-10, 0); end.Before(deadline) {
		deadline = end
	}
	if !start.Before(deadline) {
		return errors.New("entry attempt horizon exhausted")
	}
	cleanupIO := entryOperationIO(h, time.Unix(a.NotAfter, 0))
	h = entryOperationIO(h, deadline)
	defer func() {
		if result == nil {
			return
		}
		// Record direct observation failures before cleanup can change progress
		// or reason. A failed Save must not suppress the original error or stop.
		result = errors.Join(result, c.Save(a, a.Phase, hostReason(result)))
		if a.Parent == nil || a.Phase == "complete" {
			return
		}
		// Cleanup authority survives failed observations and integrity incidents.
		// No retry or publication is possible on the reconcile path.
		result = errors.Join(result, c.reconcileEntry(a, cleanupIO))
	}()
	if err := validatePlan(a, filepath.Join(c.Path, a.ID)); err != nil {
		return err
	}
	a.Stage = "entry-preflight"
	if err := c.Save(a, "prepared", ""); err != nil {
		return err
	}
	if err := h.freshNames(a); err != nil {
		return c.unknown(a, "launch-unknown", err)
	}
	if err := c.publishEntryContext(a); err != nil {
		return err
	}
	if err := h.entryPreflight(a); err != nil {
		return err
	}
	a.Stage = "retained-parent-intent"
	a.EntryRuntime.ParentStartIntent = true
	if err := c.Save(a, "launching-slice", ""); err != nil {
		return err
	}
	if err := h.startEntryParent(a); err != nil {
		return c.unknown(a, "launch-unknown", err)
	}
	parent, err := h.parent(a)
	if err != nil {
		return c.unknown(a, "launch-unknown", err)
	}
	a.Parent = parent
	if err := c.Save(a, "slice-bound", ""); err != nil {
		return err
	}
	admission := a.EntryRuntime.Admission
	if admission == nil || parent.Boundary.Path != filepath.Dir(admission.User.Identity.Cgroup)+"/"+a.Slice {
		return errors.New("entry parent outside observed user root")
	}
	policy, err := h.entryPolicyUnit(a, false, parent.Boundary.Path, a.Slice)
	if err != nil {
		return err
	}
	if policy.boundary() != parent.Boundary || policy.Invocation != parent.Properties["InvocationID"] {
		return errors.New("entry parent policy generation changed")
	}
	pop, err := h.population(parent.Boundary)
	if err != nil || pop {
		return c.unknown(a, "cleanup-unknown", errors.New("fresh entry parent not empty"))
	}
	a.Stage = "tool-start-intent"
	a.EntryRuntime.ToolDispatchIntent = true
	if err := c.Save(a, "launching-service", ""); err != nil {
		return err
	}
	s, _, err := subject(a.Config.SystemdRun, true, false, false, 64<<20)
	if err != nil || s != a.Subjects["systemd-run"] {
		return c.unknown(a, "launch-unknown", errors.New("systemd-run changed"))
	}
	if !entryNow(h).Before(deadline.Add(-30 * time.Second)) {
		return c.unknown(a, "cleanup-unknown", errors.New("insufficient entry launch budget"))
	}
	if _, err := h.call(s.Path, a.Launch); err != nil {
		return c.unknown(a, "launch-unknown", err)
	}
	path := filepath.Join(c.Path, a.ID)
	var injection Injection
	wrapperDeadline := entryNow(h).Add(8 * time.Second)
	for n := 0; n < 800; n++ {
		injection, err = readInjection(filepath.Join(path, "injection.json"))
		if err == nil {
			break
		}
		if !os.IsNotExist(err) || !entryNow(h).Before(wrapperDeadline) {
			return c.unknown(a, "launch-unknown", err)
		}
		h.pause()
	}
	if err != nil {
		return c.unknown(a, "launch-unknown", err)
	}
	u, err := h.service(a)
	if err != nil {
		return c.unknown(a, "launch-unknown", err)
	}
	if err := validateInjection(a, injection, u); err != nil {
		return c.unknown(a, "launch-unknown", err)
	}
	a.Workload, a.Injector, a.Target = u, &injection, &injection.Process
	// Stop authority precedes admission observations; failure of any of those
	// observations does not erase the independently verified original bindings.
	if err := c.Save(a, "launching-service", ""); err != nil {
		return err
	}
	if err := h.entryEffective(u, false); err != nil {
		return c.unknown(a, "cleanup-unknown", err)
	}
	monitor, err := h.handle(injection.Process)
	if err != nil {
		return c.unknown(a, "cleanup-unknown", err)
	}
	defer monitor.Close()
	pop, err = h.population(parent.Boundary)
	if err != nil || !pop {
		return c.unknown(a, "cleanup-unknown", errors.New("entry live parent unproved"))
	}
	a.PopulatedLive = true
	if _, err := h.parent(a); err != nil {
		return c.unknown(a, "cleanup-unknown", err)
	}
	if err := c.Save(a, "bound", ""); err != nil {
		return err
	}
	if err := h.entryTree(a.Parent.Boundary, a.Workload.Boundary); err != nil {
		return err
	}
	// Validate device again at dispatch, not just during preparation. The
	// injector/final reexec also authenticate controls and this literal device.
	null, err := h.entryNull()
	if err != nil || null != a.EntryStatic.Null {
		c.failEntryIntegrity(a)
		return c.unknown(a, "cleanup-unknown", errors.New("dispatch null device changed"))
	}
	if !entryNow(h).Before(wrapperDeadline) {
		return errors.New("wrapper gate budget exhausted")
	}
	if err := h.observeEntryAdmission(a, true); err != nil {
		return err
	}
	if err := c.Save(a, "bound", ""); err != nil {
		return err
	}
	if !entryNow(h).Before(wrapperDeadline) {
		return errors.New("wrapper admission budget exhausted")
	}
	if err := c.wrapperEntry(a); err != nil {
		return err
	}
	a.Stage = "entry-readiness"
	ready, receipt, err := c.awaitEntryReady(a, h, entryNow(h).Add(R1ReadyWait))
	if err != nil {
		return err
	}
	handles, err := c.observeEntry(a, h, ready, receipt)
	defer func() {
		for _, fd := range handles {
			fd.Close()
		}
	}()
	if err != nil {
		return err
	}
	a.Stage = "entry-natural-timeout"
	// Independent child tail can outlive main. Keep all original pidfds and
	// wait for natural termination; no 4s legacy completion shortcut.
	naturalDeadline := entryNow(h).Add(12 * time.Second)
	if naturalDeadline.After(deadline) {
		naturalDeadline = deadline
	}
	for n := 0; n < 1200; n++ {
		if err := h.entryControllerAlive(a); err != nil {
			return err
		}
		if err := authenticateEntry(a, true); err != nil {
			c.failEntryIntegrity(a)
			return err
		}
		dead, err := h.exit(monitor)
		if err != nil {
			return err
		}
		for _, fd := range handles {
			ended, err := h.exit(fd)
			if err != nil {
				return err
			}
			dead = dead && ended
		}
		pop, err := h.population(parent.Boundary)
		if err != nil {
			return err
		}
		if dead && !pop {
			// Terminal/empty cleanup is independent of the expected exit oracle.
			if err := h.terminal(a); err != nil {
				return err
			}
			exitErr := h.entryTerminalExit(a)
			if _, err := h.parent(a); err != nil {
				return err
			}
			pop, err := h.population(parent.Boundary)
			if err != nil || pop {
				return errors.New("entry empty parent changed")
			}
			if err := h.terminal(a); err != nil {
				return err
			}
			r := a.EntryRuntime
			r.Terminal, r.Natural, r.ExpectedExit = true, true, exitErr == nil
			a.Empty, a.TargetExited = true, true
			if exitErr != nil {
				// Preserve the specific service outcome before collection can
				// report the more general incomplete-entry verdict.
				if err := c.Save(a, a.Phase, hostReason(exitErr)); err != nil {
					return errors.Join(exitErr, err)
				}
			}
			if err := c.stopEntryParent(a, h); err != nil {
				return err
			}
			return errors.Join(exitErr, c.collectEntry(a))
		}
		if !entryNow(h).Before(naturalDeadline) {
			break
		}
		h.pause()
	}
	return errors.New("bounded natural entry termination unavailable")
}

func entryOperationIO(h hostIO, deadline time.Time) hostIO {
	if !h.entryUntil.IsZero() && h.entryUntil.Before(deadline) {
		deadline = h.entryUntil
	}
	h.entryUntil = deadline
	if h.realHost {
		h.deadlineCall = func(path string, args []string, until time.Time) (string, error) {
			return hostCallBus(path, args, until, true)
		}
	}
	call := h.call
	h.call = func(path string, args []string) (string, error) {
		if !entryNow(h).Before(deadline) {
			return "", errors.New("entry controller operation deadline")
		}
		if h.deadlineCall != nil {
			return h.deadlineCall(path, args, deadline)
		}
		return call(path, args)
	}
	return h
}

func (c *Campaign) wrapperEntry(a *Attempt) error {
	if err := entryAction(a, "wrapper-entry"); err != nil {
		return err
	}
	if err := authenticateEntry(a, true); err != nil {
		c.failEntryIntegrity(a)
		return err
	}
	a.Stage = "wrapper-entry-intent-pressure-release-prohibited"
	a.EntryRuntime.WrapperIntent = true
	if err := c.Save(a, "released", ""); err != nil {
		return err
	}
	r, _ := json.Marshal(release{1, a.ID, a.Injector.Invocation})
	if err := c.entryPublish(filepath.Join(c.Path, a.ID), "release.json", r); err != nil {
		c.failEntryIntegrity(a)
		return c.unknown(a, "launch-unknown", err)
	}
	return nil
}

// Incremental bounded reader: only complete newline frames are provisional
// evidence. Revalidate stream identity, detect truncation and never accept EOF
// as a complete frame. Final collection still requires stopped writers.
func (c *Campaign) awaitEntryReady(a *Attempt, h hostIO, deadline time.Time) (R1Evidence, time.Time, error) {
	f, err := openAbsolute(filepath.Join(c.Path, a.ID, "output"), 0)
	if err != nil {
		return R1Evidence{}, time.Time{}, err
	}
	defer f.Close()
	var prefix []byte
	var chunk [R1MaxFrame]byte
	for n := 0; n < 200; n++ {
		if !entryNow(h).Before(deadline) {
			break
		}
		fi, err := f.Stat()
		if err != nil || checkRegular(fi, MaxOutputBytes) != nil || identity(fi) != a.Streams["output"] || fi.Size() < int64(len(prefix)) {
			return R1Evidence{}, time.Time{}, errors.New("entry output identity/truncation")
		}
		nread, err := f.Read(chunk[:])
		if err != nil && err != io.EOF {
			return R1Evidence{}, time.Time{}, err
		}
		prefix = append(prefix, chunk[:nread]...)
		if len(prefix) > R1MaxFrame {
			return R1Evidence{}, time.Time{}, errors.New("entry ready frame excessive")
		}
		if at := bytes.IndexByte(prefix, '\n'); at >= 0 {
			if !entryNow(h).Before(deadline) {
				return R1Evidence{}, time.Time{}, errors.New("entry readiness budget exhausted")
			}
			e, err := DecodeR1Evidence(prefix[:at])
			if err != nil {
				return e, time.Time{}, err
			}
			if e.Stage != "ready" || e.Ready == nil {
				return e, time.Time{}, errors.New("entry did not reach two-actor readiness")
			}
			return e, entryNow(h), nil
		}
		if !entryNow(h).Before(deadline) {
			break
		}
		h.pause()
	}
	return R1Evidence{}, time.Time{}, errors.New("bounded entry readiness unavailable")
}

func (c *Campaign) collectEntry(a *Attempt) (result error) {
	defer func() {
		c.checkEntryIntegrity(a)
		if a.EntryIntegrityFailure != "" {
			result = errors.Join(result, errors.New(entryIntegrityFailure))
		}
		if a.Phase == "complete" {
			reason := ""
			if result != nil {
				reason = hostReason(result)
			}
			result = errors.Join(result, c.Save(a, "complete", reason))
			// Catch the same post-authentication final Save race as FD1. Sticky
			// integrity survives restoration and cannot upgrade a later inspect.
			c.checkEntryIntegrity(a)
			if a.EntryIntegrityFailure != "" {
				result = errors.Join(result, errors.New(entryIntegrityFailure), c.Save(a, "complete", entryIntegrityFailure))
			}
		}
	}()
	if !a.Empty || !a.TargetExited || !a.PopulatedLive || !a.SliceStopped || a.EntryRuntime == nil || !a.EntryRuntime.Terminal {
		return errors.New("entry collection without stopped writers")
	}
	if err := authenticateEntry(a, true); err != nil {
		c.failEntryIntegrity(a)
		return err
	}
	root, err := os.OpenRoot(filepath.Join(c.Path, a.ID))
	if err != nil {
		return err
	}
	defer root.Close()
	for _, name := range []string{"output", "stderr"} {
		s, _, err := subject(filepath.Join(c.Path, a.ID, name), false, false, true, MaxOutputBytes)
		if err != nil || s.Identity != a.Streams[name] {
			c.failEntryIntegrity(a)
			return errors.New("owned entry stream changed")
		}
	}
	b, err := CollectFlat(root, "descriptor-policy.json", MaxOutputBytes, true)
	var receipt DescriptorReceipt
	if err != nil || decode(bytes.NewReader(b), MaxOutputBytes, &receipt) != nil || validateDescriptorReceipt(a, receipt) != nil {
		return errors.New("entry descriptor preflight receipt absent/invalid")
	}
	a.DescriptorReceipt = &receipt
	b, err = CollectFlat(root, "output", MaxOutputBytes, true)
	if err != nil {
		return err
	}
	if len(b) == 0 || b[len(b)-1] != '\n' {
		return errors.New("partial entry output")
	}
	lines := bytes.Split(b[:len(b)-1], []byte{'\n'})
	for _, line := range lines {
		e, err := DecodeR1Evidence(line)
		if err == nil && (e.Pressure != nil || e.Touched != nil || e.Completion != nil || e.Failure != nil && (e.Failure.ReleaseSHA256 != "" || e.Failure.FinishSHA256 != "" || e.Failure.TouchedBytes != 0)) {
			c.failEntryIntegrity(a)
			return errors.New("prohibited pressure evidence")
		}
	}
	if len(lines) != 2 {
		return errors.New("entry requires exactly ready and expected failure")
	}
	ready, err := DecodeR1Evidence(lines[0])
	if err != nil {
		return err
	}
	failure, err := DecodeR1Evidence(lines[1])
	if err != nil {
		return err
	}
	if ready.Pressure != nil || failure.Pressure != nil || failure.Touched != nil || failure.Completion != nil || failure.Failure != nil && (failure.Failure.ReleaseSHA256 != "" || failure.Failure.TouchedBytes != 0) {
		c.failEntryIntegrity(a)
		return errors.New("prohibited pressure evidence")
	}
	if a.EntryRuntime.Ready == nil || !sameJSON(ready, *a.EntryRuntime.Ready) {
		return errors.New("entry provisional/frozen readiness differs")
	}
	if err := expectedEntryTrace(a, ready, failure); err != nil {
		return err
	}
	a.EntryRuntime.Failure = &failure
	a.EntryRuntime.OutputSHA256 = syntheticDigest(b)
	if !entryObserved(a) {
		return errors.New("entry observation incomplete")
	}
	return nil
}
