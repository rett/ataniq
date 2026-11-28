package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"ataniq/internal/authorityhost/decisionguard"
	"ataniq/internal/seatapp"
	"ataniq/internal/seatlink"
)

// defaultGuardDir is the release switch's state folder on the authority host,
// where it keeps decisions.inhibit (see deploy/authority-host/README.md, "The
// decision inhibit").
const defaultGuardDir = "/var/lib/authority-host"

// guardedDaemon is the daemon port behind the authority host's decision
// contract: before it relays a decision's challenge or a decision, it asks
// decisionguard.Check of the state folder, every time, and on any error
// relays nothing and answers seatapp.ErrDecisionsInhibited. Pairing, login,
// the reading of cards and chat, and the new-project interview decide nothing and go
// through unchanged.
type guardedDaemon struct {
	seatapp.Daemon
	dir string
	// check is decisionguard.Check; a test may replace it.
	check func(string) error
	// refused, when not nil, is told the fixed code of each refusal.
	refused func(code string)
}

// newGuardedDaemon puts d behind the guard of the state folder dir.
func newGuardedDaemon(d seatapp.Daemon, dir string, refused func(string)) guardedDaemon {
	return guardedDaemon{Daemon: d, dir: dir, check: decisionguard.Check, refused: refused}
}

// allowed asks the guard. Any error refuses: one that is not the package's
// own *Error is reported with a fixed code too.
func (g guardedDaemon) allowed() error {
	err := g.check(g.dir)
	if err == nil {
		return nil
	}
	code := "decision-guard-error"
	var ge *decisionguard.Error
	if errors.As(err, &ge) {
		code = ge.Code
	}
	if g.refused != nil {
		g.refused(code)
	}
	return fmt.Errorf("%w (%s)", seatapp.ErrDecisionsInhibited, code)
}

// Offer asks the guard too, read-only: while decisions are refused the card
// page is offered no decision, only the notice that they are paused. It
// logs nothing, since no decision was refused; the card stays readable, and
// DecideChallenge and Decide stay the enforcement.
func (g guardedDaemon) Offer(id, bindingSHA256 string, r seatapp.Reading, now time.Time) seatapp.Offer {
	if g.check(g.dir) != nil {
		return seatapp.Offer{Inhibited: true}
	}
	return g.Daemon.Offer(id, bindingSHA256, r, now)
}

// DecideChallenge asks the guard first, and asks the daemon for no challenge
// while decisions are refused.
func (g guardedDaemon) DecideChallenge(ctx context.Context, r seatapp.DecideChallengeRequest) (seatapp.DecideChallenge, error) {
	if err := g.allowed(); err != nil {
		return seatapp.DecideChallenge{}, err
	}
	return g.Daemon.DecideChallenge(ctx, r)
}

// Decide asks the guard again, right before the decision would be sent: the
// inhibit may have come since the challenge. A refused decision is never
// sent, so its challenge is dropped here (the app takes the call as sent and
// drops nothing itself).
func (g guardedDaemon) Decide(ctx context.Context, r seatapp.DecideRequest) (seatapp.DecideResult, error) {
	if err := g.allowed(); err != nil {
		g.DropChallenge(r.ChallengeID)
		return seatapp.DecideResult{}, err
	}
	return g.Daemon.Decide(ctx, r)
}

// DropChallenge passes the drop on to a daemon port that keeps room for
// challenges (seatapp.ChallengeDropper), and does nothing for one that does
// not.
func (g guardedDaemon) DropChallenge(id string) {
	if d, ok := g.Daemon.(seatapp.ChallengeDropper); ok && id != "" {
		d.DropChallenge(id)
	}
}

// Watch passes the link's watched state on (seatlink.WatchPort), and says
// nothing is watched for a daemon port that does not watch. The guard
// concerns decisions; the state decides nothing, so it is not guarded.
func (g guardedDaemon) Watch() seatlink.WatchState {
	if d, ok := g.Daemon.(seatlink.WatchPort); ok {
		return d.Watch()
	}
	return seatlink.WatchState{}
}

// WatchChanged passes the link's channel on, and returns a channel that is
// never closed (a nil one) for a daemon port that does not watch.
func (g guardedDaemon) WatchChanged() <-chan struct{} {
	if d, ok := g.Daemon.(seatlink.WatchPort); ok {
		return d.WatchChanged()
	}
	return nil
}

// ChatHint passes the link's chat hint on (seatapp.ChatWatcher), for the
// Leads pages' stream, and says it is not live for a daemon port that learns
// none. Like the watched state it is a hint that decides nothing, so it is
// not guarded.
func (g guardedDaemon) ChatHint() seatapp.ChatHint {
	if d, ok := g.Daemon.(seatapp.ChatWatcher); ok {
		return d.ChatHint()
	}
	return seatapp.ChatHint{}
}

// ChatHintChanged passes the link's channel on, and returns a channel that is
// never closed (a nil one) for a daemon port that learns no hint.
func (g guardedDaemon) ChatHintChanged() <-chan struct{} {
	if d, ok := g.Daemon.(seatapp.ChatWatcher); ok {
		return d.ChatHintChanged()
	}
	return nil
}

// Genesis passes a call of the guided new-project interview on to a daemon
// port that runs it (seatapp.Genesis, which the gate link is), and answers
// seatapp.ErrNotOffered for one that does not, so the phone then shows no
// "New project". An answer fills a field of a project's brief and decides
// nothing, so the guard does not ask: the interview goes on while decisions
// are paused.
func (g guardedDaemon) Genesis(ctx context.Context, c seatapp.GenesisCall) (seatapp.GenesisView, error) {
	if d, ok := g.Daemon.(seatapp.Genesis); ok {
		return d.Genesis(ctx, c)
	}
	return seatapp.GenesisView{}, seatapp.ErrNotOffered
}

// ChatOffered reports the connected link's offer without a gate call.
func (g guardedDaemon) ChatOffered() bool {
	d, ok := g.Daemon.(seatapp.Chat)
	return ok && d.ChatOffered()
}

// Chat passes the two read operations through while decisions are paused.
func (g guardedDaemon) Chat(ctx context.Context, c seatapp.ChatCall) (seatapp.ChatView, error) {
	if d, ok := g.Daemon.(seatapp.Chat); ok {
		return d.Chat(ctx, c)
	}
	return seatapp.ChatView{}, seatapp.ErrNotOffered
}

var (
	_ seatlink.WatchPort  = guardedDaemon{}
	_ seatapp.Watcher     = guardedDaemon{}
	_ seatapp.ChatWatcher = guardedDaemon{}
	_ seatapp.Genesis     = guardedDaemon{}
	_ seatapp.Chat        = guardedDaemon{}
)
