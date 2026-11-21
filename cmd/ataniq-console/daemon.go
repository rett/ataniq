package main

import (
	"context"
	"strconv"
	"time"

	"ataniq/internal/consoleclient"
	"ataniq/internal/terminalconsole"
)

// The daemon's view. With --daemon the cockpit shows what the daemon says of
// its leads, and its approvals in the approvals view when the daemon serves the
// owner channel: the reading is consoleclient's, the drawing is the
// console's, and this file is the only place where the two meet, because the
// console may not import a client. It maps consoleclient's plain values onto
// the console's projection and changes nothing anywhere.

// daemonLabel is what the cockpit's header and session frame say with
// --daemon.
const daemonLabel = "the daemon's view, read-only and provisional"

// noOwnerChannel is the reason every approval control carries with --daemon
// until the daemon's hello lists the owner channel.
const noOwnerChannel = "no owner channel in this build"

// daemonSource is the console's source with --daemon. Snapshot opens nothing:
// it is the view before the daemon has been asked. Refresh asks the daemon,
// on the console's tick.
type daemonSource struct {
	// dir is the folder that holds the daemon's socket.
	dir string
	// read asks the daemon: consoleclient.Read, or a test's own.
	read func(ctx context.Context, dir string) consoleclient.Result
}

// Snapshot returns the view shown until the first answer.
func (d daemonSource) Snapshot() terminalconsole.Projection {
	p := daemonBase(false)
	p.Problem = "the daemon has not been asked yet"
	return p
}

// Refresh asks the daemon and maps its answer. It never fails: when the
// daemon cannot be read the projection carries the plain state and its next
// step, which the cockpit shows where the leads would be.
func (d daemonSource) Refresh(ctx context.Context) (terminalconsole.Projection, error) {
	return daemonProjection(d.read(ctx, d.dir)), nil
}

// daemonBase is what every projection of the daemon's view holds: no session
// control is available, and each says why. The texts follow the daemon, not
// the build: owner is true only when the daemon's hello listed the owner
// channel, and then the approval controls point at the approvals view. Until
// the daemon has answered, and for a daemon that lists none, they say there
// is no owner channel.
func daemonBase(owner bool) terminalconsole.Projection {
	readOnly := "The daemon's view is read-only."
	approve := "There is " + noOwnerChannel + "."
	p := terminalconsole.Projection{
		ControlUnavailable: "The daemon's view is read-only: " + noOwnerChannel + ".",
		Gate:               terminalconsole.Gate{State: "None shown: " + noOwnerChannel},
		Contract: []terminalconsole.Check{
			{Name: "Daemon view", State: "provisional, unqualified"},
			{Name: "Owner channel", State: "unavailable: " + noOwnerChannel},
		},
	}
	if owner {
		approve = "Open the approvals view with g."
		p.ControlUnavailable = "The daemon's view has no session control: it attaches to no session and sends nothing to a lead."
		p.Gate = terminalconsole.Gate{State: "Shown in the approvals view (g)"}
		p.Contract[1] = terminalconsole.Check{Name: "Owner channel", State: "provisional, unqualified: asked of the daemon"}
	}
	p.Controls = []terminalconsole.Control{
		{Name: "Approve pending gate", Key: "Ctrl+K", Reason: approve, Approval: true},
		{Name: "Deny pending gate", Key: "Ctrl+K", Reason: approve, Approval: true},
		{Name: "Full attach", Key: "a", Reason: readOnly + " It attaches to no session."},
		{Name: "Send to lead", Key: "Ctrl+K", Reason: readOnly + " It sends nothing to a lead."},
		{Name: "Interrupt lead", Key: "Ctrl+K", Reason: readOnly + " It sends nothing to a lead."},
		{Name: "Detach", Key: "Ctrl+K", Reason: readOnly + " There is no attachment to end."},
	}
	return p
}

// daemonProjection maps what consoleclient read onto the console's
// projection. A result without a view becomes the plain state and its next
// step, both fixed texts of consoleclient. A view becomes projects with their
// lead, the coordinators under the lead, and the daemon's own facts.
//
// consoleclient has cleaned and bounded every string of a view, and the
// console cleans and bounds its copy again, so nothing here needs to.
func daemonProjection(r consoleclient.Result) terminalconsole.Projection {
	p := daemonBase(r.State == consoleclient.StateOK && r.View.OwnerChannel)
	if r.State != consoleclient.StateOK {
		p.Problem, p.NextStep = r.Text, r.NextStep
		if p.Problem == "" {
			p.Problem = "the daemon could not be asked"
		}
		return p
	}
	v := r.View
	p.Facts = []terminalconsole.Check{
		{Name: "Daemon version", State: orUnknown(v.Daemon.Version)},
		{Name: "Daemon process", State: pidText(v.Daemon.PID)},
		{Name: "Daemon started", State: timeText(v.Daemon.StartedAt)},
		{Name: "Last reconcile", State: reconcileText(v.Daemon.ReconcileCompleted)},
	}
	supervisor := "none listed"
	if v.HasSupervisor {
		supervisor = stateText(v.Supervisor)
	}
	p.Facts = append(p.Facts, terminalconsole.Check{Name: "Supervisor", State: supervisor})
	if v.LeftOut > 0 {
		p.Facts = append(p.Facts, terminalconsole.Check{Name: "Entries left out of this view", State: strconv.Itoa(v.LeftOut)})
	}
	for _, pr := range v.Projects {
		lead := terminalconsole.Lead{Name: "no lead listed"}
		if pr.HasLead {
			lead = daemonLead(pr.Lead)
		}
		for _, c := range pr.Coordinators {
			lead.Subagents = append(lead.Subagents, "coordinator "+orUnknown(c.Feature)+" · "+string(c.State))
		}
		p.Projects = append(p.Projects, terminalconsole.Project{Name: pr.Name, Leads: []terminalconsole.Lead{lead}})
	}
	return p
}

// daemonLead maps a lead's entry. The profile is what was requested, not
// what was observed, and the text says so.
func daemonLead(e consoleclient.Entry) terminalconsole.Lead {
	requested := orUnknown(e.Provider)
	if e.Model != "" {
		requested += " / " + e.Model
	}
	if e.Effort != "" {
		requested += " / " + e.Effort
	}
	return terminalconsole.Lead{
		Name:  e.Name,
		State: string(e.State),
		Session: terminalconsole.Session{
			Provider:   requested + " (requested)",
			Runtime:    orUnknown(e.Manager),
			Authority:  "None (read-only view)",
			ID:         "epoch " + strconv.FormatUint(e.Epoch, 10),
			Attachment: "None: the daemon's view attaches to no session",
			Binding:    stateText(e),
			Execution:  "desired " + orUnknown(e.Desired) + ", since " + timeText(e.Since),
		},
	}
}

// stateText is an entry's state with its reason and the reason's detail.
func stateText(e consoleclient.Entry) string {
	s := string(e.State)
	if e.Reason != "" {
		s += " (" + e.Reason + ")"
	}
	if e.Detail != "" {
		s += ": " + e.Detail
	}
	return s
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func pidText(pid int) string {
	if pid <= 0 {
		return "unknown"
	}
	return "pid " + strconv.Itoa(pid)
}

func timeText(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.UTC().Format("2006-01-02 15:04:05Z")
}

func reconcileText(completed bool) string {
	if completed {
		return "completed"
	}
	return "not completed"
}

// daemonRig is what outlives one console of --daemon: the daemon's approvals
// backend, and the lead client when the daemon has lead control. The loop
// makes a console from it each time it opens one, so that an attach, which
// ends a console, leaves the connections and the lead client's unconfirmed
// commands where they were.
type daemonRig struct {
	cfg config
	// read asks the daemon for its view: consoleclient.Read, or a test's own.
	read func(ctx context.Context, dir string) consoleclient.Result
	// leads is the adapter of the lead client, nil when the daemon has no
	// lead control.
	leads  *leadsAdapter
	closes []func()
}

// newDaemonRig makes the rig for --daemon as the command line makes it: the
// daemon's approvals as the backend, and, when the daemon's hello lists the
// lead control's operations, the lead client as the leads view's backend. Every
// build has the approvals backend; whether the daemon has an owner channel or
// lead control is found when it is asked. mk makes the lead client. Close
// ends what the rig holds.
func newDaemonRig(cfg config, mk newLeadsFunc) (*daemonRig, error) {
	backend, closeOwner, err := ownerBackend(cfg.daemon)
	if err != nil {
		return nil, err
	}
	r := &daemonRig{cfg: cfg, read: consoleclient.Read, closes: []func(){closeOwner}}
	r.cfg.options.Backend = backend
	if r.leads = detectLeads(cfg.daemon, mk); r.leads != nil {
		r.cfg.options.Leads = r.leads
		c := r.leads.c
		r.closes = append(r.closes, func() { c.Close() })
	}
	return r, nil
}

// model returns a new console for the daemon's view.
func (r *daemonRig) model() *terminalconsole.Model { return daemonModelWith(r.cfg, r.read) }

// attachKind is the kind of attach a request of the leads view is for.
func (r *daemonRig) attachKind(req terminalconsole.AttachRequest) attachKind {
	if r.leads == nil {
		return ""
	}
	return r.leads.attachKind(req)
}

// Close ends the backends.
func (r *daemonRig) Close() {
	for _, c := range r.closes {
		c()
	}
}

// daemonConsole returns the console for --daemon as the command line makes
// it for one run: the daemon's view, its approvals as the backend, and its
// lead control as the leads view's when it has one. The returned function ends
// what the backends hold; the caller calls it when the console is done.
func daemonConsole(cfg config) (*terminalconsole.Model, func(), error) {
	r, err := newDaemonRig(cfg, newLeadsClient)
	if err != nil {
		return nil, nil, err
	}
	return r.model(), r.Close, nil
}

// daemonModelWith returns the console for the daemon's view: the cockpit with
// the daemon as a source it asks again on its tick, by the function that asks
// the daemon: the command line gives consoleclient.Read and a test its own.
func daemonModelWith(cfg config, read func(ctx context.Context, dir string) consoleclient.Result) *terminalconsole.Model {
	o := cfg.options
	o.Label = daemonLabel
	return terminalconsole.NewWithOptions(daemonSource{dir: cfg.daemon, read: read}, o)
}
