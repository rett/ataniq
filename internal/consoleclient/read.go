package consoleclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"net"
	"time"

	"ataniq/internal/clientapi"
)

// DefaultTimeout bounds [Read] when its context has no deadline.
const DefaultTimeout = 10 * time.Second

// defaultTimeout is DefaultTimeout as Read uses it. It is a variable so that
// a test can lower it and not wait ten seconds.
var defaultTimeout = DefaultTimeout

// Read asks the daemon whose socket is in the folder dir for its leads, and
// returns a view of them or a state that says why there is none. It never
// returns an error: every failure is one of the states, with a text and a
// next step written here.
//
// Read dials the socket, calls "hello" and then "status" and nothing else,
// and closes the connection before it returns. The context's deadline bounds
// the whole of it; a context without a deadline is given [DefaultTimeout].
// When the answer to "hello" does not list this protocol, "status" is not
// asked.
func Read(ctx context.Context, dir string) Result {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultTimeout)
		defer cancel()
	}
	c, err := clientapi.Dial(ctx, clientapi.SocketPath(dir))
	if err != nil {
		return state(dialState(err))
	}
	defer c.Close()

	hello, err := c.Hello(ctx)
	if err != nil {
		return state(callState(err))
	}
	if !speaksProtocol(hello.Protocols) {
		return state(StateOtherProtocol)
	}
	status, err := c.Status(ctx)
	if err != nil {
		return state(callState(err))
	}
	return Result{State: StateOK, View: buildView(hello, status)}
}

// state returns the result for a state without a view.
func state(s State) Result {
	t := stateTexts[s]
	return Result{State: s, Text: t[0], NextStep: t[1]}
}

// speaksProtocol reports whether the protocols a peer listed include this
// one.
func speaksProtocol(protocols []string) bool {
	for _, p := range protocols {
		if p == clientapi.Protocol {
			return true
		}
	}
	return false
}

// timedOut reports whether err says that time ran out.
func timedOut(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// errRefused is the error of a dial to a socket file that nothing listens
// on, or nil on a system for which the package does not know it. It is set
// in a file for Linux, the one system the daemon runs on: the error's name is
// in the syscall package, which does not have it on every system this
// package builds for. Where it is nil such a dial is [StateUnavailable].
var errRefused error

// dialState is the state for a failed dial.
func dialState(err error) State {
	switch {
	case timedOut(err):
		return StateSlow
	case errors.Is(err, context.Canceled):
		return StateUnavailable
	case errors.Is(err, fs.ErrNotExist), errRefused != nil && errors.Is(err, errRefused):
		// No socket file, or a file that nothing listens on.
		return StateNoDaemon
	}
	return StateUnavailable
}

// callState is the state for a failed call.
func callState(err error) State {
	var e *clientapi.Error
	if errors.As(err, &e) {
		switch e.Code {
		case clientapi.CodeUnauthorized:
			return StateRefused
		case clientapi.CodeUnsupportedProtocol, clientapi.CodeUnsupportedOperation:
			return StateOtherProtocol
		case clientapi.CodeBusy, clientapi.CodeInternal:
			return StateUnavailable
		}
		// A code this client's requests cannot earn, or one it does not know.
		return StateBadAnswer
	}
	switch {
	case timedOut(err):
		return StateSlow
	case errors.Is(err, context.Canceled), errors.Is(err, clientapi.ErrClosed):
		return StateUnavailable
	}
	// Over the cap, not a valid response, or a connection that ended in
	// place of a response.
	return StateBadAnswer
}

// buildView turns the two results into a view, cleaning every string and
// keeping to the bounds.
func buildView(hello clientapi.HelloResult, status clientapi.StatusResult) View {
	_, owner := ownerOperationsListed(hello.Operations)
	v := View{OwnerChannel: owner, Daemon: Daemon{
		Version:            clean(hello.Version),
		StartedAt:          status.Daemon.StartedAt,
		ReconcileCompleted: status.Daemon.LastReconcileCompleted,
	}}
	if status.Daemon.PID > 0 {
		v.Daemon.PID = status.Daemon.PID
	}
	// index maps a project's name as the daemon sent it to the project's
	// place in v.Projects. The cleaned name is not the key: two names can
	// clean to the same text and are still two projects.
	index := make(map[string]int)
	for i := range status.Leads {
		raw := status.Leads[i].Project
		e, ok := entry(&status.Leads[i])
		if !ok {
			v.LeftOut++
			continue
		}
		if e.Kind == KindSupervisor {
			if v.HasSupervisor {
				v.LeftOut++
				continue
			}
			v.HasSupervisor, v.Supervisor = true, e
			continue
		}
		at, known := index[raw]
		if !known {
			if e.Project == "" || len(v.Projects) >= MaxProjects {
				v.LeftOut++
				continue
			}
			at = len(v.Projects)
			index[raw] = at
			v.Projects = append(v.Projects, Project{Name: e.Project, ID: scratchID(raw)})
		}
		p := &v.Projects[at]
		switch {
		case e.Kind == KindLead && !p.HasLead:
			p.HasLead, p.Lead = true, e
		case e.Kind == KindCoordinator && len(p.Coordinators) < MaxCoordinators:
			p.Coordinators = append(p.Coordinators, e)
		default:
			v.LeftOut++
		}
	}
	return v
}

// scratchID gives the local inventory a bounded comparison key from the
// entire wire identifier, before display cleaning or truncation.
func scratchID(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// entry returns the cleaned entry for one entry of the status result, and
// false when its kind is not one this package knows.
func entry(l *clientapi.LeadStatus) (Entry, bool) {
	var kind Kind
	switch l.Kind {
	case clientapi.KindLead:
		kind = KindLead
	case clientapi.KindSupervisor:
		kind = KindSupervisor
	case clientapi.KindCoordinator:
		kind = KindCoordinator
	default:
		return Entry{}, false
	}
	e := Entry{
		ID:       scratchID(l.Role),
		Kind:     kind,
		Name:     clean(l.Role),
		State:    entryState(l.State),
		Desired:  clean(l.Desired),
		Reason:   clean(l.ReasonCode),
		Detail:   clean(l.ReasonDetail),
		Since:    l.Since,
		Epoch:    l.Epoch,
		Manager:  clean(l.Manager),
		Provider: clean(l.Requested.Provider),
		Model:    clean(l.Requested.Model),
		Effort:   clean(l.Requested.Effort),
	}
	if kind != KindSupervisor {
		e.Project = clean(l.Project)
	}
	if kind == KindCoordinator {
		e.Feature = clean(l.Feature)
		e.Parent = clean(l.Parent)
	}
	return e, true
}

// entryState returns the shown state for the daemon's word, and EntryUnknown
// for a word that is not a shown state.
func entryState(s string) EntryState {
	switch st := EntryState(s); st {
	case EntryDesired, EntryStarting, EntryReady, EntryDegraded, EntryStalled,
		EntryStopping, EntryStopped, EntryEnded:
		return st
	}
	return EntryUnknown
}
