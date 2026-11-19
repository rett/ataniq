package main

import (
	"context"
	"strconv"
	"time"

	"ataniq/internal/consoleclient"
	"ataniq/internal/terminalconsole"
)

// The daemon's view. With --daemon the cockpit shows what the daemon says of
// its leads, read-only: the reading is consoleclient's, the drawing is the
// console's, and this file is the only place where the two meet, because the
// console may not import a client. It maps consoleclient's plain values onto
// the console's projection and changes nothing anywhere.

// daemonLabel is what the cockpit's header and session frame say with
// --daemon.
const daemonLabel = "the daemon's view, read-only and provisional"

// noOwnerChannel is the reason every approval control carries with --daemon.
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
	p := daemonBase()
	p.Problem = "the daemon has not been asked yet"
	return p
}

// Refresh asks the daemon and maps its answer. It never fails: when the
// daemon cannot be read the projection carries the plain state and its next
// step, which the cockpit shows where the leads would be.
func (d daemonSource) Refresh(ctx context.Context) (terminalconsole.Projection, error) {
	return daemonProjection(d.read(ctx, d.dir)), nil
}

// daemonBase is what every projection of the daemon's view holds: no control
// is available, and each says why.
func daemonBase() terminalconsole.Projection {
	readOnly := "The daemon's view is read-only."
	return terminalconsole.Projection{
		ControlUnavailable: "The daemon's view is read-only: " + noOwnerChannel + ".",
		Gate:               terminalconsole.Gate{State: "None shown: " + noOwnerChannel},
		Contract: []terminalconsole.Check{
			{Name: "Daemon view", State: "provisional, unqualified"},
			{Name: "Owner channel", State: "unavailable: " + noOwnerChannel},
		},
		Controls: []terminalconsole.Control{
			{Name: "Approve pending gate", Key: "Ctrl+K", Reason: "There is " + noOwnerChannel + "."},
			{Name: "Deny pending gate", Key: "Ctrl+K", Reason: "There is " + noOwnerChannel + "."},
			{Name: "Full attach", Key: "a", Reason: readOnly + " It attaches to no session."},
			{Name: "Send to lead", Key: "Ctrl+K", Reason: readOnly + " It sends nothing to a lead."},
			{Name: "Interrupt lead", Key: "Ctrl+K", Reason: readOnly + " It sends nothing to a lead."},
			{Name: "Detach", Key: "Ctrl+K", Reason: readOnly + " There is no attachment to end."},
		},
	}
}

// daemonProjection maps what consoleclient read onto the console's
// projection. A result without a view becomes the plain state and its next
// step, both fixed texts of consoleclient. A view becomes projects with their
// lead, the coordinators under the lead, and the daemon's own facts.
//
// consoleclient has cleaned and bounded every string of a view, and the
// console cleans and bounds its copy again, so nothing here needs to.
func daemonProjection(r consoleclient.Result) terminalconsole.Projection {
	p := daemonBase()
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

// daemonModel returns the console for the daemon's view: the cockpit with the
// daemon as a source it asks again on its tick.
func daemonModel(cfg config) *terminalconsole.Model {
	return daemonModelWith(cfg, consoleclient.Read)
}

// daemonModelWith is daemonModel with the function that asks the daemon: the
// command line gives consoleclient.Read and a test its own.
func daemonModelWith(cfg config, read func(ctx context.Context, dir string) consoleclient.Result) *terminalconsole.Model {
	o := cfg.options
	o.Label = daemonLabel
	return terminalconsole.NewWithOptions(daemonSource{dir: cfg.daemon, read: read}, o)
}
