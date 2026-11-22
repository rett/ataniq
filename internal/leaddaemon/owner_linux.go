//go:build linux && ataniq_ownerchannel

package leaddaemon

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"time"

	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/clientapi"
)

// OwnerTag is the build tag of the owner channel. A binary built without it
// has no owner connection: nothing in this file, or in the files that go with
// it, is built, and the daemon's hello lists no owner operation.
const OwnerTag = "ataniq_ownerchannel"

// OwnerService is what the daemon asks of the host's side of a card: the
// operations of the owner connection, each as package ownerchannel's service
// has it. A *ownerchannel.Service is one. It is an interface so that the tests
// of the daemon need no door, no merge and no journal.
type OwnerService interface {
	List(ctx context.Context) (ownerwire.Listing, error)
	Show(ctx context.Context, id string) (ownerwire.ShowResult, error)
	Paths(ctx context.Context, p ownerwire.PageParams) (ownerwire.PathsPage, error)
	Patch(ctx context.Context, p ownerwire.PageParams) (ownerwire.PatchPage, error)
	Decide(ctx context.Context, src ownerchannel.Source, f ownerwire.DecisionFrame) (ownerwire.DecideResult, error)
	Beat(src ownerchannel.Source) error
	Limit(ctx context.Context, project string) (ownerwire.LimitView, error)
	LimitSet(ctx context.Context, src ownerchannel.Source, p ownerwire.LimitSetParams) (ownerwire.LimitView, error)
	Looked(ctx context.Context) error
	PairStart(ctx context.Context, src ownerchannel.Source, p ownerwire.PairStartParams) (ownerwire.PairStartResult, error)
	PairStatus(ctx context.Context) (ownerwire.PairStatus, error)
	PairConfirm(ctx context.Context, src ownerchannel.Source, p ownerwire.PairPendingParams) (ownerwire.KeyItem, error)
	PairReject(ctx context.Context, src ownerchannel.Source, p ownerwire.PairPendingParams) error
	PairPresent(ctx context.Context, src ownerchannel.Source, p ownerwire.SeatPairParams) (ownerwire.SeatPairResult, error)
	Keys(ctx context.Context) (ownerwire.KeysView, error)
	KeyRevoke(ctx context.Context, src ownerchannel.Source, p ownerwire.KeyRevokeParams) (ownerwire.KeysView, error)
	KeysOff(ctx context.Context, src ownerchannel.Source) (ownerwire.KeysView, error)
}

var _ OwnerService = (*ownerchannel.Service)(nil)

// PeerRef names the process on the other end of a connection as the daemon saw
// it when it accepted the connection: the process id from SO_PEERCRED, and the
// time, by the monotonic clock, of the accept.
type PeerRef struct {
	PID        int
	AcceptedAt time.Time
}

// PeerProcess is what an inspector found of a peer.
type PeerProcess struct {
	// TTY is the device path of the peer's controlling terminal, "" when it
	// has none.
	TTY string
}

// PeerInspector looks at the process behind a connection. It is injected, so
// that no test needs a real terminal; [ProcInspector] is the real one. An
// error means the peer could not be judged, and the daemon then does not grant
// the decider's place: an inspector that cannot answer never grants it.
type PeerInspector interface {
	Inspect(p PeerRef) (PeerProcess, error)
}

// DefaultDeciderIdle is the default idle time of the decider's connection. It
// is longer than the server's, so that the place is not lost while the owner
// reads a card; a connection that holds it and sends nothing for this long is
// closed, and the place is free again.
const DefaultDeciderIdle = 10 * time.Minute

// DecisionRefusalBound is how many decision frames from a connection that is
// not the decider are handed to the service, which journals each as refused.
// The next one is refused without the journal and ends the connection.
const DecisionRefusalBound = 16

// OwnerChannel is the owner connection of one daemon: the operations
// "owner.*" of package ownerwire, served on the same socket and the same
// protocol, "ataniq.client/1", as everything else, through the extension seam
// of package clientapi. Set [Config].ServeHook to its Serve method to register
// it; a daemon without that is as it was.
//
// The decider's place, the one connection that may send a decision, is given
// to a connection whose peer runs as the owner (the server's own check), names
// the console and has a controlling terminal, checked through Inspector. One
// connection holds it; a second console is read-only. A decision frame from
// any other connection is refused and counted.
type OwnerChannel struct {
	// Build makes the service. The daemon calls it once, when it holds its
	// locks and listens, and calls the returned function (when not nil) after
	// the server has ended. An error stops the daemon. Required.
	Build func() (OwnerService, func() error, error)
	// Inspector looks at the peer of a connection that asks to be the
	// decider. Required; [ProcInspector] is the real one.
	Inspector PeerInspector
	// Now is the clock of the accept times; default time.Now.
	Now func() time.Time
	// DeciderIdle is how long the connection that holds the decider's place
	// may wait for its next request before the server closes it, which frees
	// the place; the server's own idle time, 30 s by default, applies to every
	// other connection. Default [DefaultDeciderIdle].
	DeciderIdle time.Duration
	// Log receives one line for each refusal and each grant, with no text a
	// peer sent but its client name through [SafeText]; nil discards them.
	Log func(format string, a ...any)

	// seat decides whether a connection is a seat and, when it is, the
	// source the seat's calls are handed to the service with. It is nil: no
	// seat source exists yet, so seat.pair is refused to every connection,
	// and no code outside this package can set it. A test of the package
	// sets it to show what the daemon does once a source exists.
	seat func(v ownerView) (ownerchannel.Source, bool)

	counts OwnerCounts
}

// OwnerCounts count what the owner connection refused and granted. Each is
// read with its Load method.
type OwnerCounts struct {
	// DeciderGranted counts grants of the decider's place.
	DeciderGranted atomic.Uint64
	// SecondConsole counts requests for the place while another connection
	// held it.
	SecondConsole atomic.Uint64
	// NotConsole counts requests from a client that did not name the console.
	NotConsole atomic.Uint64
	// NoTerminal counts requests from a peer with no controlling terminal.
	NoTerminal atomic.Uint64
	// NotInspected counts requests whose peer the inspector could not judge.
	NotInspected atomic.Uint64
	// SessionChanged counts owner.decider calls that named another session or client
	// than the connection's first.
	SessionChanged atomic.Uint64
	// DecisionNotDecider counts decision frames from a connection that was
	// not the decider.
	DecisionNotDecider atomic.Uint64
	// DecisionInvalid counts decision frames from the decider that did not
	// pass the frame's own form.
	DecisionInvalid atomic.Uint64
	// DecisionFlooded counts connections closed because a connection that
	// was not the decider sent more than [DecisionRefusalBound] decision
	// frames.
	DecisionFlooded atomic.Uint64
	// NoConnection counts requests that could not be tied to a connection.
	NoConnection atomic.Uint64
	// PairNotDecider counts writes of the pairing operations from a
	// connection that was not the decider. They count against the same
	// bound as decision frames ([DecisionRefusalBound]).
	PairNotDecider atomic.Uint64
	// SeatRefused counts seat.pair calls, which this build refuses to every
	// connection: it has no seat source.
	SeatRefused atomic.Uint64
}

// Counts returns the counters. The same value is returned each time.
func (o *OwnerChannel) Counts() *OwnerCounts { return &o.counts }

func (o *OwnerChannel) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o *OwnerChannel) deciderIdle() time.Duration {
	if o.DeciderIdle > 0 {
		return o.DeciderIdle
	}
	return DefaultDeciderIdle
}

func (o *OwnerChannel) logf(format string, a ...any) {
	if o.Log != nil {
		o.Log(format, a...)
	}
}

// Serve is the [Config].ServeHook of the owner channel: it builds the service,
// makes the extension, wraps the listener so that every connection is known,
// and adds the extension and the peer check that goes with the wrapper to the
// server's options.
func (o *OwnerChannel) Serve(l net.Listener, opts *clientapi.Options) (net.Listener, func(), error) {
	if o.Build == nil || o.Inspector == nil {
		return nil, nil, errors.New("owner channel: no way to build the service or to inspect a peer")
	}
	svc, closeSvc, err := o.Build()
	if err != nil {
		return nil, nil, errors.New("owner channel: the service could not be built: " + SafeText(err.Error()))
	}
	if svc == nil {
		if closeSvc != nil {
			_ = closeSvc()
		}
		return nil, nil, errors.New("owner channel: the service could not be built")
	}
	reg := newOwnerConns(o.deciderIdle())
	ext := &ownerExt{oc: o, svc: svc, reg: reg, seat: o.seat}
	opts.Extra = append(opts.Extra, ext)
	base := opts.PeerUID
	if base == nil {
		base = clientapi.PeerUID
	}
	// The server reads the peer's credentials from the connection it is
	// given, which is the wrapper: it reads them from the socket inside.
	opts.PeerUID = func(c net.Conn) (uint32, error) {
		if oc, ok := c.(*ownerConn); ok {
			c = oc.Conn
		}
		return base(c)
	}
	done := func() {
		if closeSvc == nil {
			return
		}
		if err := closeSvc(); err != nil {
			o.logf("owner channel: closing the service: %s", SafeText(err.Error()))
		}
	}
	return &ownerListener{Listener: l, reg: reg, now: o.now}, done, nil
}
