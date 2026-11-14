//go:build linux && amd64

package qualification

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type HostReport struct {
	Version          int    `json:"version"`
	Attempt          string `json:"attempt,omitempty"`
	Outcome          string `json:"outcome"`
	Phase            string `json:"phase,omitempty"`
	Stage            string `json:"stage"`
	ReasonCode       string `json:"reason_code"`
	NewStartsBlocked bool   `json:"new_starts_blocked"`
	Reason           string `json:"reason"`
}

func (c *Campaign) run(a *Attempt, h hostIO) error {
	a.Stage = "fresh-names"
	if err := c.Save(a, "prepared", ""); err != nil {
		return err
	}
	if err := h.freshNames(a); err != nil {
		return c.unknown(a, "launch-unknown", err)
	}
	if err := c.Save(a, "prepared", ""); err != nil {
		return err
	}
	// Intent is already synced. Every supervisor dispatch has its own preceding
	// durable state. Failed acknowledgement is never retried or time-expired.
	if err := c.Save(a, "launching-slice", ""); err != nil {
		return err
	}
	a.Stage = "slice-start"
	if _, err := h.ctl(a, "start", "--", a.Slice); err != nil {
		return c.unknown(a, "launch-unknown", err)
	}
	a.Stage = "parent-bind"
	parent, err := h.parent(a)
	if err != nil {
		return c.unknown(a, "launch-unknown", err)
	}
	a.Parent = parent
	if err = c.Save(a, "slice-bound", ""); err != nil {
		return err
	}
	if err = c.Save(a, "launching-service", ""); err != nil {
		return err
	}
	a.Stage = "service-start"
	s, _, err := subject(a.Config.SystemdRun, true, false, false, 64<<20)
	if err != nil || s != a.Subjects["systemd-run"] {
		return c.unknown(a, "launch-unknown", errors.New("systemd-run changed"))
	}
	if _, err = h.call(s.Path, a.Launch); err != nil {
		return c.unknown(a, "launch-unknown", err)
	}
	a.Stage = "injection-read"
	var i Injection
	for n := 0; n < 200; n++ {
		i, err = readInjection(filepath.Join(c.Path, a.ID, "injection.json"))
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			return c.unknown(a, "launch-unknown", err)
		}
		h.pause()
	}
	if err != nil {
		return c.unknown(a, "launch-unknown", err)
	}
	a.Stage = "service-bind"
	u, err := h.service(a)
	if err != nil {
		return c.unknown(a, "launch-unknown", err)
	}
	a.Stage = "injection-bind"
	if err = validateInjection(a, i, u); err != nil {
		return c.unknown(a, "launch-unknown", err)
	}
	if err = authenticateFD1Runtime(a, i.FD1Runtime); err != nil {
		return c.unknown(a, "launch-unknown", err)
	}
	a.Stage = "effective-budget"
	if err = h.effective(u); err != nil {
		return c.unknown(a, "cleanup-unknown", err)
	}
	a.Stage = "process-handle"
	fd, err := h.handle(i.Process)
	if err != nil {
		return c.unknown(a, "cleanup-unknown", err)
	}
	defer fd.Close()
	a.Workload = u
	a.FD1Runtime = i.FD1Runtime
	a.Injector = &i
	a.Target = &i.Process
	a.Stage = "live-parent"
	live, err := h.population(parent.Boundary)
	if err != nil || !live {
		return c.unknown(a, "cleanup-unknown", errors.New("parent live population unproved"))
	}
	a.PopulatedLive = true
	if _, err = h.parent(a); err != nil {
		return c.unknown(a, "cleanup-unknown", err)
	}
	if err = freshFD1Control(a, time.Now().Unix()); err != nil {
		return c.unknown(a, "launch-unknown", err)
	}
	if err = c.Save(a, "bound", ""); err != nil {
		return err
	}
	if err = c.Save(a, "released", ""); err != nil {
		return err
	}
	a.Stage = "release"
	if err = freshFD1Control(a, time.Now().Unix()); err != nil {
		return c.unknown(a, "launch-unknown", err)
	}
	if err = authenticateFD1Runtime(a, a.FD1Runtime); err != nil {
		return c.unknown(a, "launch-unknown", err)
	}
	r, _ := json.Marshal(release{1, a.ID, i.Invocation})
	if err = publishOwned(filepath.Join(c.Path, a.ID), "release.json", r); err != nil {
		return c.unknown(a, "launch-unknown", err)
	}
	if a.Config.Case == "hold" {
		a.Stage = "readiness"
		p, ready, err := c.awaitReady(a, h)
		if err != nil {
			// Already bound authority can clean up, but never turn failed readiness
			// into an observation. The injector/monitor handle remains retained.
			cleanupErr := c.cancel(a, h, fd)
			if cleanupErr != nil {
				return cleanupErr
			}
			return err
		}
		target, err := h.handle(p)
		if err != nil {
			return c.unknown(a, "cleanup-unknown", err)
		}
		defer target.Close()
		a.Target = &p
		a.Ready = &ready
		if err = c.Save(a, "released", ""); err != nil {
			return err
		}
		// Readiness is untrusted. Revalidate actual executable/starttime,
		// namespaces, exact containment and pidfd immediately before cancellation.
		actual, err := h.process(p.PID)
		dead, e := h.exit(target)
		if err != nil || e != nil || dead || !sameProcess(p, actual) {
			return c.unknown(a, "cleanup-unknown", errors.New("target no longer proved alive"))
		}
		if err = c.cancel(a, h, target); err != nil {
			return err
		}
	} else {
		// The retained injector pidfd follows exec into direct helper / bwrap
		// monitor. Combined with the retained parent-empty oracle, it proves
		// that chain ended, without guessing a fast helper's host PID.
		if err = c.finishFast(a, h, fd); err != nil {
			return err
		}
	}
	a.Stage = "collection"
	return c.collect(a)
}

func (c *Campaign) finishFast(a *Attempt, h hostIO, fd *os.File) error {
	a.Stage = "completion"
	for n := 0; n < 400; n++ {
		dead, err := h.exit(fd)
		if err != nil {
			return c.unknown(a, "cleanup-unknown", err)
		}
		pop, err := h.population(a.Parent.Boundary)
		if err != nil {
			return c.unknown(a, "cleanup-unknown", err)
		}
		if dead && !pop {
			if err = h.terminal(a); err != nil {
				return c.unknown(a, "cleanup-unknown", err)
			}
			if _, err = h.parent(a); err != nil {
				return c.unknown(a, "cleanup-unknown", err)
			}
			a.TargetExited = true
			a.Empty = true
			if err = c.Save(a, "empty", ""); err != nil {
				return err
			}
			if _, err = h.ctl(a, "stop", "--", a.Slice); err != nil {
				return c.unknown(a, "cleanup-unknown", err)
			}
			a.SliceStopped = true
			return c.Save(a, "complete", "")
		}
		h.pause()
	}
	return c.unknown(a, "cleanup-unknown", errors.New("finite completion observation unavailable"))
}

func (c *Campaign) awaitReady(a *Attempt, h hostIO) (Process, HoldFrame, error) {
	path := filepath.Join(c.Path, a.ID, "output")
	for n := 0; n < 100; n++ {
		// Live read is NOT CollectFlat and is NOT frozen final evidence. A prefix
		// can only trigger independent process validation, never a case result.
		b, err := readOwned(path, MaxOutputBytes)
		if err != nil {
			h.pause()
			continue
		}
		f, err := ReadHoldReady(bytes.NewReader(b))
		if err != nil {
			h.pause()
			continue
		}
		if f.RunID != a.Config.RunID || f.RequestID != a.Config.RequestID {
			return Process{}, f, errors.New("stale readiness IDs")
		}
		p, err := h.helper(a.Workload.Boundary, a.Snapshots["probe"].Identity)
		if err != nil {
			return p, f, err
		}
		if err = validateReady(a, f, p); err != nil {
			return p, f, err
		}
		u, err := h.service(a)
		if err != nil {
			return p, f, err
		}
		a.ExecMain = u.Main
		return p, f, nil
	}
	return Process{}, HoldFrame{}, errors.New("bounded readiness unavailable")
}

func validateReady(a *Attempt, f HoldFrame, p Process) error {
	if f.Kind != "ready" || f.RunID != a.Config.RunID || f.RequestID != a.Config.RequestID || f.Start != p.Start || f.Executable.Device != p.Executable.Device || f.Executable.Inode != p.Executable.Inode || f.PIDNamespace.Device != p.Namespaces["pid"].Device || f.PIDNamespace.Inode != p.Namespaces["pid"].Inode || p.Cgroup != a.Workload.Boundary.Path {
		return errors.New("readiness identity mismatch")
	}
	var nspid string
	for _, line := range strings.Split(p.Status, "\n") {
		if strings.HasPrefix(line, "NSpid:") {
			fields := strings.Fields(line)
			if len(fields) > 1 {
				nspid = fields[len(fields)-1]
			}
		}
	}
	if nspid != strconv.Itoa(f.PID) {
		return errors.New("readiness namespace pid mismatch")
	}
	if a.Config.Role != "direct" {
		for _, name := range []string{"pid", "mnt", "net", "user", "ipc", "uts"} {
			if p.Namespaces[name].Inode == 0 || p.Namespaces[name] == a.Injector.Process.Namespaces[name] {
				return errors.New("candidate namespace isolation unverified")
			}
		}
		for _, line := range []string{"NoNewPrivs:\t1", "Seccomp:\t2", "CapInh:\t0000000000000000", "CapAmb:\t0000000000000000", "CapEff:\t0000000000000000", "CapPrm:\t0000000000000000", "CapBnd:\t0000000000000000"} {
			if !strings.Contains(p.Status, line+"\n") {
				return errors.New("candidate privilege state unverified")
			}
		}
		if !readOnlyMount(p.Mounts.Root) || !readOnlyMount(p.Mounts.Probe) || !readOnlyMount(p.Mounts.Fixture) {
			return errors.New("readonly candidate mounts unverified")
		}
	}
	return nil
}

func (c *Campaign) collect(a *Attempt) error {
	if !a.Empty || !a.TargetExited || !a.PopulatedLive || !a.SliceStopped {
		return errors.New("collection without complete boundary proof")
	}
	if err := authenticateFD1Runtime(a, a.FD1Runtime); err != nil {
		return err
	}
	root, err := os.OpenRoot(filepath.Join(c.Path, a.ID))
	if err != nil {
		return err
	}
	defer root.Close()
	if a.DescriptorPolicy != "" {
		b, err := CollectFlat(root, "descriptor-policy.json", MaxOutputBytes, true)
		var r DescriptorReceipt
		if err != nil || decode(bytes.NewReader(b), MaxOutputBytes, &r) != nil || validateDescriptorReceipt(a, r) != nil {
			return errors.New("missing or invalid final descriptor preflight receipt")
		}
		a.DescriptorReceipt = &r
	}
	b, err := CollectFlat(root, "output", MaxOutputBytes, true)
	if err != nil {
		return err
	}
	if a.Config.Case == "hold" {
		lines := bytes.Split(bytes.TrimSuffix(b, []byte{'\n'}), []byte{'\n'})
		if len(lines) != 2 {
			return errors.New("hold requires exactly readiness and final frames")
		}
		r, err := decodeHold(bytes.NewReader(lines[0]))
		if err != nil {
			return err
		}
		f, err := decodeHold(bytes.NewReader(lines[1]))
		if err != nil {
			return err
		}
		if a.Ready == nil || !sameJSON(r, *a.Ready) || f.Kind != "final" || f.Reason != "cancelled" {
			return errors.New("missing cancellation final evidence")
		}
		x := f
		x.Kind = "ready"
		x.Reason = ""
		if !sameJSON(x, r) {
			return errors.New("final identity mismatch")
		}
		a.Final = &f
	} else {
		o, err := DecodeObservation(bytes.NewReader(b))
		if err != nil {
			return err
		}
		if o.RunID != a.Config.RunID || o.RequestID != a.Config.RequestID || o.Case != a.Request.Case {
			return errors.New("observation identity mismatch")
		}
		a.Observation = &o
		if a.DescriptorPolicy != "" {
			// Keep failed exclusion/recovery observations durably, including all
			// matching identities and reused numbers. Cleanup is already proved.
			if a.Config.Case == "inventory" {
				d := observeDescriptorIdentities(a, o)
				a.DescriptorObservation = &d
			}
			if err := c.Save(a, "complete", ""); err != nil {
				return err
			}
			if err := validateDescriptorObservations(a, o); err != nil {
				return err
			}
		}
		if a.Config.Case == "fake-fd" && a.Config.Role == "direct" && (o.Status != "observed" || factValue(o, "digest_match") != "true") {
			return errors.New("direct recovery not observed")
		}
	}
	return c.Save(a, "complete", "")
}

func RunHost(config HostConfig) HostReport {
	r := HostReport{Version: 1, Outcome: "inconclusive", Stage: "driver-identity", ReasonCode: "precondition-rejected", NewStartsBlocked: true, Reason: "host preconditions or evidence incomplete"}
	self, err := os.Executable()
	if err != nil || self != config.Driver {
		r.Reason = "configuration must name the running driver"
		return r
	}
	r.Stage = "campaign-open"
	c, err := OpenCampaign(config.Campaign)
	if err != nil {
		r.Reason = hostReason(err)
		return r
	}
	defer c.Close()
	r.Stage = "prepare"
	a, err := c.Prepare(config)
	if err != nil {
		if strings.HasPrefix(err.Error(), "unsupported:") {
			r.Outcome = "unsupported"
		}
		r.Reason = hostReason(err)
		var failure *hostFailure
		if errors.As(err, &failure) {
			r.Stage = failure.Stage
			r.ReasonCode = failure.Code
			if strings.HasPrefix(failure.Cause.Error(), "unsupported:") {
				r.Outcome = "unsupported"
			}
		}
		r.NewStartsBlocked = c.Blocked
		return r
	}
	r.Attempt = a.ID
	err = c.run(a, liveHost())
	r.Stage = a.Stage
	r.ReasonCode = "evidence-unproved"
	if err != nil {
		r.Reason = hostReason(err)
	}
	r.Phase = a.Phase
	r.NewStartsBlocked = c.Blocked || a.Phase != "complete"
	if err == nil && (a.Observation != nil && a.Observation.Status == "observed" || a.Final != nil) {
		r.Outcome = "observed"
		r.ReasonCode = "bounded-observation"
		r.Reason = "bounded case evidence retained; no matrix support assertion"
	}
	return r
}

func ReconcileHost(campaign, id string) HostReport {
	r := HostReport{Version: 1, Attempt: id, Outcome: "inconclusive", Stage: "campaign-open", ReasonCode: "reconciliation-only", NewStartsBlocked: true, Reason: "reconciliation only; no replacement or qualification"}
	c, err := OpenCampaign(campaign)
	if err != nil {
		r.Reason = hostReason(err)
		return r
	}
	defer c.Close()
	a := c.Records[id]
	if a == nil {
		return r
	}
	if err = c.reconcile(a, liveHost()); err != nil {
		r.Reason = hostReason(err)
		r.ReasonCode = "cleanup-unproved"
	}
	if a.FD1IntegrityFailure != "" {
		r.ReasonCode = "typed-runtime-integrity-failed"
		r.Reason = a.FD1IntegrityFailure
	}
	r.Stage = a.Stage
	r.Phase = a.Phase
	r.NewStartsBlocked = false
	for _, v := range c.Records {
		if v.Phase != "complete" {
			r.NewStartsBlocked = true
		}
	}
	if c.Blocked {
		r.NewStartsBlocked = true
	}
	return r
}

// Inspect never contacts the manager. Malformed records remain visible through
// the campaign fence, even if no trustworthy attempt can be decoded.
func InspectHost(path, id string, w io.Writer) error {
	c, err := OpenCampaign(path)
	if err != nil {
		return err
	}
	defer c.Close()
	if id != "" {
		a := c.Records[id]
		if a == nil {
			return errors.New("unknown or malformed attempt")
		}
		return json.NewEncoder(w).Encode(a)
	}
	ids := make([]string, 0, len(c.Records))
	for id := range c.Records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return json.NewEncoder(w).Encode(struct {
		Blocked  bool     `json:"new_starts_blocked"`
		Attempts []string `json:"attempts"`
	}{c.Blocked, ids})
}
