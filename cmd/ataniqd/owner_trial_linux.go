//go:build linux && ataniq_ownerchannel

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"ataniq/internal/approval/hostadapter"
	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/chat/standin"
	"ataniq/internal/clientapi"
	"ataniq/internal/leaddaemon"
)

// This file is the phone trial of the command, in a build with the
// ataniq_ownerchannel tag: "ataniqd trial init", which writes the trial's
// settings, the flag --trial of run (defined with --owner-channel in
// owner_linux.go), which serves them, and "ataniqd trial submit", which asks
// the running daemon for sample cards through the operation trial.submit. It
// is a STAND-IN rig for scratch state only; package hostadapter (trial.go)
// says what is real in it and what is a sample. owner_off_trial_linux.go has
// the same names for every other build.

// trialNote is the line the daemon prints after the STAND-IN banner when it
// serves the trial.
const trialNote = "owner channel: TRIAL: sample cards only, made by ataniqd trial submit; an approval applies nothing"

// trialChatNote is the line the trial's gate writes after its start line: its
// chat is the STAND-IN's.
const trialChatNote = "chat: STAND-IN sample leads and conversations of the phone trial, held in memory; no lead is read or reached"

// trialChat gives the trial's gate the STAND-IN chat port (package
// chat/standin): three sample leads and their sample conversations, made once
// from the clock and held in memory, so the owner can try the seat's lead
// pages. Only the trial's gate gets it; every other gate serves no chat.
func trialChat(gate *leaddaemon.SeatGate) {
	gate.Chat = standin.New(time.Now)
	gate.ChatNote = trialChatNote
}

// trialOpSubmit is the operation that makes sample cards. Only a daemon run
// with --trial serves it.
const trialOpSubmit = "trial.submit"

// trialMaxCount is the most sample cards one trial.submit makes.
const trialMaxCount = 10

// trialCallTimeout bounds a trial submit call, dial included.
const trialCallTimeout = 30 * time.Second

// trialGuard is the guard of the trial's builder: the settings are a
// trial's, and the state folder is a scratch one.
var trialGuard = &ownerGuardSet{
	check: func(stateDir string, getenv func(string) string) error {
		_, err := checkTrialScratch(stateDir, getenv)
		return err
	},
	checkRuntime: checkTrialRuntime,
	banner:       ownerBanner,
	note:         trialNote,
}

// checkTrialScratch reads the settings of the state folder and applies the
// trial's guards: they are a trial's ("trial": true, which LoadScratch checks
// as such), and the state folder is not, lies not inside and does not contain
// a default folder of the account.
func checkTrialScratch(stateDir string, getenv func(string) string) (hostadapter.Scratch, error) {
	sc, err := hostadapter.LoadScratch(filepath.Join(stateDir, hostadapter.OwnerDirName, hostadapter.ScratchName))
	if err != nil {
		return hostadapter.Scratch{}, err
	}
	if !sc.Trial {
		return hostadapter.Scratch{}, errors.New(`--trial serves only the settings ataniqd trial init writes ("trial": true), and these are not a trial's`)
	}
	if _, _, _, err := checkScratchState(stateDir, getenv); err != nil {
		return hostadapter.Scratch{}, err
	}
	return sc, nil
}

// checkTrialRuntime refuses a runtime folder that is, lies inside or contains a
// default state or runtime folder of the account, as the state folder is
// refused: the trial serves its socket in the runtime folder and must not take
// the place of the account's own daemon there (that includes the default
// runtime folder a run without --runtime-dir uses).
func checkTrialRuntime(runtimeDir string, getenv func(string) string) error {
	defaults, err := defaultFolders(getenv)
	if err != nil {
		return err
	}
	run, err := resolvePath(runtimeDir)
	if err != nil {
		return fmt.Errorf("the phone trial cannot resolve the runtime folder: %v", err)
	}
	for _, d := range defaults {
		c, err := resolvePath(d)
		if err != nil {
			return fmt.Errorf("cannot resolve the default folder %q: %v", d, err)
		}
		if related(run, c) {
			return fmt.Errorf("the phone trial refuses the runtime folder %q: it is, lies inside or contains the default folder %q; give --runtime-dir a scratch folder", runtimeDir, d)
		}
	}
	return nil
}

// trialRig is the trial of one run: its builder keeps the trial it built, so
// that the extension can make sample cards on it.
type trialRig struct {
	mu    sync.Mutex
	trial *hostadapter.Trial
}

// build is the trial's [ownerServiceBuilder]: it opens the trial in the state
// folder with the owner's key list and the settings' kind policy.
func (r *trialRig) build(stateDir string) (leaddaemon.OwnerService, func() error, error) {
	sc, err := checkTrialScratch(stateDir, ownerGetenv)
	if err != nil {
		return nil, nil, err
	}
	keys := openOwnerKeys(stateDir)
	tr, err := hostadapter.OpenTrial(context.Background(), hostadapter.TrialConfig{
		StateDir: stateDir, Scratch: sc, Now: time.Now,
		Notice: func(line string) { ownerNotice(line) },
		Service: ownerchannel.Config{
			Keys: keys, KindPolicy: scratchKindPolicy(sc),
			SeatLog: func(line string) { ownerNotice("seat gate: " + line) },
		},
	})
	if err != nil {
		if keys != nil {
			_ = keys.Close()
		}
		return nil, nil, err
	}
	r.mu.Lock()
	r.trial = tr
	r.mu.Unlock()
	return tr.Service, tr.Close, nil
}

// serve wraps the owner channel's serve hook: once it has built the service,
// the extension of trial.submit is added beside the owner's.
func (r *trialRig) serve(next func(net.Listener, *clientapi.Options) (net.Listener, func(), error)) func(net.Listener, *clientapi.Options) (net.Listener, func(), error) {
	return func(l net.Listener, opts *clientapi.Options) (net.Listener, func(), error) {
		wrapped, done, err := next(l, opts)
		if err != nil {
			return nil, nil, err
		}
		r.mu.Lock()
		tr := r.trial
		r.mu.Unlock()
		if tr == nil {
			if done != nil {
				done()
			}
			return nil, nil, errors.New("the phone trial was not built")
		}
		opts.Extra = append(opts.Extra, &trialExt{trial: tr})
		return wrapped, done, nil
	}
}

// trialSubmitParams are the params of trial.submit.
type trialSubmitParams struct {
	Kind  string `json:"kind"`
	Count int    `json:"count,omitempty"`
}

// trialSubmitResult is its result: one entry per sample, in order. When the
// daemon stopped making samples after some (the call was cancelled, or a sample
// failed), Cards holds those already made and Stopped says why in one fixed
// line, so that the caller sees which cards exist.
type trialSubmitResult struct {
	Cards   []hostadapter.TrialCard `json:"cards"`
	Stopped string                  `json:"stopped,omitempty"`
}

// The fixed messages of trial.submit's errors.
const (
	msgTrialBadParams = "trial.submit takes {\"kind\": \"finish\" or \"branch-prune\", \"count\": 1 to 10}"
	msgTrialFailed    = "the trial could not make a sample card"
	msgTrialCanceled  = "the call ended before every sample card was made"
)

// trialExt serves trial.submit on the daemon's socket.
//
// The operation needs no decider's place, which the owner operations that
// decide a card ask for. It decides nothing and applies nothing: it only asks
// the trial's stand-in door for a sample card, the way a lead's request would,
// and a card made that way still waits for the owner's decision with a passkey
// (the phone, through the gate) before anything of the trial happens, and then
// applies nothing. The guard of the operation is the server's peer check:
// every connection to the daemon's socket is read for its user id before a
// byte is, and a user other than the daemon's own is answered unauthorized and
// closed. Any process of that user can open the socket (the daemon's
// documented limit for every operation), and so can make sample cards: at most
// trialMaxCount a call, within the stand-in door's own caps (6 requests a
// minute, 8 not yet final), and a refused one leaves nothing in the host's
// memory. That is all the operation gives such a process, which is why it needs
// no decider.
type trialExt struct {
	trial *hostadapter.Trial
	// submit, when set, makes the sample card instead of the trial: a test
	// uses it to stop the call between two cards.
	submit func(ctx context.Context, kind string) (hostadapter.TrialCard, error)
}

// makeCard makes one sample card of the kind.
func (x *trialExt) makeCard(ctx context.Context, kind string) (hostadapter.TrialCard, error) {
	if x.submit != nil {
		return x.submit(ctx, kind)
	}
	return x.trial.Submit(ctx, kind)
}

func (x *trialExt) Operations() []string { return []string{trialOpSubmit} }

func (x *trialExt) Mutating() bool { return true }

func (x *trialExt) Handle(ctx context.Context, op string, params json.RawMessage) (any, *clientapi.ErrorBody) {
	if op != trialOpSubmit {
		return nil, &clientapi.ErrorBody{Code: clientapi.CodeUnsupportedOperation, Message: msgTrialBadParams}
	}
	bad := &clientapi.ErrorBody{Code: clientapi.CodeInvalidRequest, Message: msgTrialBadParams}
	var p trialSubmitParams
	dec := json.NewDecoder(bytes.NewReader(params))
	dec.DisallowUnknownFields()
	if len(params) == 0 || dec.Decode(&p) != nil {
		return nil, bad
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, bad
	}
	if p.Count == 0 {
		p.Count = 1
	}
	if (p.Kind != hostadapter.TrialFinish && p.Kind != hostadapter.TrialBranchPrune) || p.Count < 1 || p.Count > trialMaxCount {
		return nil, bad
	}
	out := trialSubmitResult{Cards: []hostadapter.TrialCard{}}
	for i := 0; i < p.Count; i++ {
		c, err := x.makeCard(ctx, p.Kind)
		if err != nil {
			ownerNotice(fmt.Sprintf("trial.submit: after %d of %d cards: %s", len(out.Cards), p.Count, leaddaemon.SafeText(err.Error())))
			if len(out.Cards) == 0 {
				return nil, &clientapi.ErrorBody{Code: clientapi.CodeInternal, Message: msgTrialFailed}
			}
			// The cards already made exist on the phone: say which.
			out.Stopped = msgTrialFailed
			if ctx.Err() != nil {
				out.Stopped = msgTrialCanceled
			}
			return out, nil
		}
		out.Cards = append(out.Cards, c)
	}
	return out, nil
}

// trialCommand handles "ataniqd trial init" and "ataniqd trial submit".
func (a *app) trialCommand(args []string) (code int, ok bool) {
	if len(args) == 0 || args[0] != "trial" {
		return 0, false
	}
	if len(args) < 2 || (args[1] != "init" && args[1] != "submit") {
		fmt.Fprint(a.stderr, usage)
		return leaddaemon.ExitUsage, true
	}
	if args[1] == "init" {
		return a.trialInit(args[2:]), true
	}
	return a.trialSubmit(args[2:]), true
}

// trialInit writes the trial's settings into a state folder that is a
// scratch one, and says what it wrote and what comes next.
func (a *app) trialInit(args []string) int {
	var stateDir string
	if code := a.flags("trial init", args, func(fs *flag.FlagSet) {
		fs.StringVar(&stateDir, "state-dir", "", "state folder of the trial")
	}); code >= 0 {
		return code
	}
	if stateDir == "" {
		fmt.Fprint(a.stderr, "ataniqd trial init: --state-dir DIR is required: the trial never uses the account's default state folder\n")
		return leaddaemon.ExitUsage
	}
	if !filepath.IsAbs(stateDir) || filepath.Clean(stateDir) != stateDir {
		fmt.Fprintf(a.stderr, "ataniqd trial init: the state folder %q is not a clean absolute path\n", leaddaemon.SafeText(stateDir))
		return leaddaemon.ExitConfig
	}
	if _, _, _, err := checkScratchState(stateDir, a.getenv); err != nil {
		fmt.Fprintf(a.stderr, "ataniqd trial init: %s\n", leaddaemon.SafeText(err.Error()))
		return leaddaemon.ExitConfig
	}
	sc, err := hostadapter.WriteTrialScratch(stateDir)
	if errors.Is(err, hostadapter.ErrScratchExists) {
		fmt.Fprintf(a.stderr, "ataniqd trial init: %s already holds scratch settings, which are never overwritten; use another state folder\n",
			leaddaemon.SafeText(filepath.Join(stateDir, hostadapter.OwnerDirName, hostadapter.ScratchName)))
		return leaddaemon.ExitFailure
	}
	if err != nil {
		fmt.Fprintf(a.stderr, "ataniqd trial init: %s\n", leaddaemon.SafeText(err.Error()))
		return leaddaemon.ExitFailure
	}
	fmt.Fprintf(a.stdout, "ataniqd trial init: wrote %s: STAND-IN door, project %s, role %s, target %s, kinds %s\n",
		leaddaemon.SafeText(filepath.Join(stateDir, hostadapter.OwnerDirName, hostadapter.ScratchName)),
		sc.Project, sc.Role, sc.Target, strings.Join(sc.Kinds, ", "))
	fmt.Fprintf(a.stdout, "next: ataniqd seat-gate init --state-dir %s, pin the seat's key, then ataniqd run --owner-channel --trial --state-dir %s --seat-gate ADDR\n",
		leaddaemon.SafeText(stateDir), leaddaemon.SafeText(stateDir))
	return leaddaemon.ExitOK
}

// trialSubmit asks the running trial daemon for sample cards and prints one
// line for each.
func (a *app) trialSubmit(args []string) int {
	var kind, socket string
	var count int
	if code := a.flags("trial submit", args, func(fs *flag.FlagSet) {
		fs.StringVar(&kind, "kind", "", "finish or branch-prune")
		fs.IntVar(&count, "count", 1, "how many sample cards, 1 to 10")
		fs.StringVar(&socket, "socket", "", "socket path")
	}); code >= 0 {
		return code
	}
	if (kind != hostadapter.TrialFinish && kind != hostadapter.TrialBranchPrune) || count < 1 || count > trialMaxCount {
		fmt.Fprintf(a.stderr, "ataniqd trial submit: --kind is finish or branch-prune, and --count 1 to %d\n", trialMaxCount)
		return leaddaemon.ExitUsage
	}
	if socket == "" {
		var err error
		if socket, err = leaddaemon.DefaultSocketPath(a.getenv); err != nil {
			fmt.Fprintf(a.stderr, "ataniqd: %s\n", leaddaemon.SafeText(err.Error()))
			return leaddaemon.ExitUsage
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), trialCallTimeout)
	defer cancel()
	cl, err := clientapi.Dial(ctx, socket)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOTDIR) {
			fmt.Fprintf(a.stderr, "ataniqd trial submit: no daemon answers at %s\n", leaddaemon.SafeText(socket))
			return leaddaemon.ExitNoDaemon
		}
		fmt.Fprintf(a.stderr, "ataniqd trial submit: cannot connect to %s: %s\n", leaddaemon.SafeText(socket), leaddaemon.SafeText(err.Error()))
		return leaddaemon.ExitFailure
	}
	defer cl.Close()
	var res trialSubmitResult
	if err := cl.Call(ctx, trialOpSubmit, trialSubmitParams{Kind: kind, Count: count}, &res); err != nil {
		var ce *clientapi.Error
		switch {
		case errors.As(err, &ce) && ce.Code == clientapi.CodeUnauthorized:
			fmt.Fprintln(a.stderr, "ataniqd trial submit: the daemon refused this user (unauthorized)")
			return leaddaemon.ExitUnauthorized
		case errors.As(err, &ce) && ce.Code == clientapi.CodeUnsupportedOperation:
			fmt.Fprintln(a.stderr, "ataniqd trial submit: the daemon does not serve the trial (it was not started with --owner-channel --trial)")
			return leaddaemon.ExitProtocol
		case errors.As(err, &ce) && ce.Code == clientapi.CodeInternal:
			fmt.Fprintf(a.stderr, "ataniqd trial submit: %s\n", leaddaemon.SafeText(ce.Message))
			return leaddaemon.ExitFailure
		case errors.As(err, &ce), errors.Is(err, clientapi.ErrBadResponse), errors.Is(err, clientapi.ErrResponseTooLarge):
			fmt.Fprintf(a.stderr, "ataniqd trial submit: protocol error: %s\n", leaddaemon.SafeText(err.Error()))
			return leaddaemon.ExitProtocol
		}
		fmt.Fprintf(a.stderr, "ataniqd trial submit: %s\n", leaddaemon.SafeText(err.Error()))
		return leaddaemon.ExitFailure
	}
	code := leaddaemon.ExitOK
	for _, c := range res.Cards {
		what := c.Kind
		if c.Feature != "" {
			what += " " + c.Feature
		}
		if c.State == string(ownerchannel.RequestPending) {
			fmt.Fprintf(a.stdout, "card %s: %s (request %s)\n", leaddaemon.SafeText(c.PendingID), leaddaemon.SafeText(what), leaddaemon.SafeText(c.RequestID))
			continue
		}
		code = leaddaemon.ExitFailure
		fmt.Fprintf(a.stdout, "no card: %s (request %s) %s: %s: %s\n", leaddaemon.SafeText(what), leaddaemon.SafeText(c.RequestID),
			leaddaemon.SafeText(c.State), leaddaemon.SafeText(c.Code), leaddaemon.SafeText(c.Detail))
	}
	if res.Stopped != "" {
		fmt.Fprintf(a.stderr, "ataniqd trial submit: stopped after %d of %d cards: %s\n", len(res.Cards), count, leaddaemon.SafeText(res.Stopped))
		return leaddaemon.ExitFailure
	}
	if len(res.Cards) != count {
		fmt.Fprintf(a.stderr, "ataniqd trial submit: the daemon answered %d cards for %d\n", len(res.Cards), count)
		return leaddaemon.ExitProtocol
	}
	return code
}
