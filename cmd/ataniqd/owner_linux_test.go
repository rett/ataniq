//go:build linux && ataniq_ownerchannel

package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
	"ataniq/internal/clientapi"
	"ataniq/internal/leaddaemon"
)

// ownerCmdService is an OwnerService that records the decisions it is given.
type ownerCmdService struct {
	mu       sync.Mutex
	decided  []ownerchannel.Source
	closed   int
	stateDir string
}

func (s *ownerCmdService) List(context.Context) (ownerwire.Listing, error) {
	return ownerwire.Listing{Pending: []ownerwire.PendingItem{}, Recent: []ownerwire.RecentItem{}, Rules: []ownerwire.Rule{}}, nil
}
func (s *ownerCmdService) Show(context.Context, string) (ownerwire.ShowResult, error) {
	return ownerwire.ShowResult{}, errors.New("not here")
}
func (s *ownerCmdService) Paths(context.Context, ownerwire.PageParams) (ownerwire.PathsPage, error) {
	return ownerwire.PathsPage{}, errors.New("not here")
}
func (s *ownerCmdService) Patch(context.Context, ownerwire.PageParams) (ownerwire.PatchPage, error) {
	return ownerwire.PatchPage{}, errors.New("not here")
}
func (s *ownerCmdService) Decide(_ context.Context, src ownerchannel.Source, _ ownerwire.DecisionFrame) (ownerwire.DecideResult, error) {
	s.mu.Lock()
	s.decided = append(s.decided, src)
	s.mu.Unlock()
	return ownerwire.DecideResult{State: record.StateApplied, Line: "applied"}, nil
}
func (s *ownerCmdService) Beat(ownerchannel.Source) error { return nil }
func (s *ownerCmdService) Limit(context.Context, string) (ownerwire.LimitView, error) {
	return ownerwire.LimitView{}, errors.New("not here")
}
func (s *ownerCmdService) LimitSet(context.Context, ownerchannel.Source, ownerwire.LimitSetParams) (ownerwire.LimitView, error) {
	return ownerwire.LimitView{}, errors.New("not here")
}
func (s *ownerCmdService) Looked(context.Context) error { return nil }
func (s *ownerCmdService) PairStart(context.Context, ownerchannel.Source, ownerwire.PairStartParams) (ownerwire.PairStartResult, error) {
	return ownerwire.PairStartResult{}, errors.New("not here")
}
func (s *ownerCmdService) PairStatus(context.Context) (ownerwire.PairStatus, error) {
	return ownerwire.PairStatus{Phase: ownerwire.PairNone}, nil
}
func (s *ownerCmdService) PairConfirm(context.Context, ownerchannel.Source) (ownerwire.KeyItem, error) {
	return ownerwire.KeyItem{}, errors.New("not here")
}
func (s *ownerCmdService) PairReject(context.Context, ownerchannel.Source) error {
	return errors.New("not here")
}
func (s *ownerCmdService) PairPresent(context.Context, ownerchannel.Source, ownerwire.SeatPairParams) (ownerwire.SeatPairResult, error) {
	return ownerwire.SeatPairResult{}, errors.New("not here")
}
func (s *ownerCmdService) Keys(context.Context) (ownerwire.KeysView, error) {
	return ownerwire.KeysView{Keys: []ownerwire.KeyItem{}}, nil
}
func (s *ownerCmdService) KeyRevoke(context.Context, ownerchannel.Source, ownerwire.KeyRevokeParams) (ownerwire.KeysView, error) {
	return ownerwire.KeysView{}, errors.New("not here")
}
func (s *ownerCmdService) KeysOff(context.Context, ownerchannel.Source) (ownerwire.KeysView, error) {
	return ownerwire.KeysView{}, errors.New("not here")
}

// ownerCmdTerminal is a PeerInspector that finds every peer on a terminal.
type ownerCmdTerminal struct{}

func (ownerCmdTerminal) Inspect(leaddaemon.PeerRef) (leaddaemon.PeerProcess, error) {
	return leaddaemon.PeerProcess{TTY: "/dev/pts/3"}, nil
}

// ownerCmdSetup puts a fake builder and a fake inspector in place of the
// command's, and returns the service the builder hands out and a count of the
// builder's calls.
func ownerCmdSetup(t *testing.T) (svc *ownerCmdService, builds func() int) {
	t.Helper()
	svc = &ownerCmdService{}
	var mu sync.Mutex
	n := 0
	oldBuild, oldInspect, oldGuard := ownerServiceBuilder, ownerInspector, ownerGuard
	ownerGuard = nil
	ownerServiceBuilder = func(stateDir string) (leaddaemon.OwnerService, func() error, error) {
		mu.Lock()
		n++
		mu.Unlock()
		svc.mu.Lock()
		svc.stateDir = stateDir
		svc.mu.Unlock()
		return svc, func() error { svc.mu.Lock(); svc.closed++; svc.mu.Unlock(); return nil }, nil
	}
	ownerInspector = ownerCmdTerminal{}
	t.Cleanup(func() { ownerServiceBuilder, ownerInspector, ownerGuard = oldBuild, oldInspect, oldGuard })
	return svc, func() int { mu.Lock(); defer mu.Unlock(); return n }
}

func ownerCmdDirs(t *testing.T) (state, runtime string) {
	t.Helper()
	base := shortDir(t)
	if err := os.Mkdir(base+"/r", 0o700); err != nil {
		t.Fatal(err)
	}
	return base + "/s", base + "/r"
}

func ownerCmdHello(t *testing.T, socket string) clientapi.HelloResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := clientapi.Dial(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	h, err := c.Hello(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// TestOwnerRunWithTheFlagServesTheOwnerConnection: with --owner-channel the
// daemon builds the service in its state folder, lists the owner operations
// beside the base two, gives the decider's place to a console, passes its
// decision to the service with the connection's process and terminal, and
// closes the service when it stops.
func TestOwnerRunWithTheFlagServesTheOwnerConnection(t *testing.T) {
	svc, builds := ownerCmdSetup(t)
	state, runtime := ownerCmdDirs(t)
	ta := newTestApp(t, nil)
	done := startApp(t, ta, []string{"--state-dir", state, "--runtime-dir", runtime, "--owner-channel"}, runtime+"/daemon.sock")

	h := ownerCmdHello(t, runtime+"/daemon.sock")
	want := append([]string{"hello", "status"}, ownerwire.Operations()...)
	if strings.Join(h.Operations, " ") != strings.Join(want, " ") || !h.Mutations {
		t.Fatalf("operations %v mutations %v", h.Operations, h.Mutations)
	}
	if builds() != 1 {
		t.Fatalf("the service was built %d times", builds())
	}
	svc.mu.Lock()
	if svc.stateDir != state {
		t.Fatalf("the service was built for %q, want the state folder %q", svc.stateDir, state)
	}
	svc.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := clientapi.Dial(ctx, runtime+"/daemon.sock")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var dr ownerwire.DeciderResult
	if err := c.Call(ctx, ownerwire.OpDecider, ownerwire.DeciderParams{Client: "ataniq-console 0.0.11", Session: "0123456789abcdef"}, &dr); err != nil || !dr.Decider {
		t.Fatalf("owner.decider: %+v, %v", dr, err)
	}
	frame := ownerwire.DecisionFrame{ID: "ap-one", BindingSHA256: strings.Repeat("ab", 32), Decision: record.DecisionApprove, SignedOff: []record.SignOff{}}
	var out ownerwire.DecideResult
	if err := c.Call(ctx, ownerwire.OpDecide, frame, &out); err != nil || out.State != record.StateApplied {
		t.Fatalf("owner.decide: %+v, %v", out, err)
	}
	svc.mu.Lock()
	got := append([]ownerchannel.Source(nil), svc.decided...)
	svc.mu.Unlock()
	wantSrc := ownerchannel.Source{
		Surface: "console", Session: "0123456789abcdef", Decider: true,
		PID: int64(os.Getpid()), TTY: "/dev/pts/3", Client: "ataniq-console 0.0.11",
	}
	if len(got) != 1 || got[0] != wantSrc {
		t.Fatalf("the service saw %+v, want %+v", got, wantSrc)
	}

	c.Close()
	ta.signals <- syscall.SIGTERM
	if code := <-done; code != 0 {
		t.Fatalf("exit %d: %s", code, ta.err.String())
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.closed != 1 {
		t.Fatalf("the service was closed %d times", svc.closed)
	}
	if !strings.Contains(ta.err.String(), "the decider's place is given to peer pid") {
		t.Fatalf("the log has no line for the grant:\n%s", ta.err.String())
	}
}

// TestOwnerRunWithTheTagButWithoutTheFlagRegistersNothing: a build with the tag
// that is started without --owner-channel lists what a release build lists,
// does not call the builder, and refuses an owner operation as unsupported.
func TestOwnerRunWithTheTagButWithoutTheFlagRegistersNothing(t *testing.T) {
	_, builds := ownerCmdSetup(t)
	state, runtime := ownerCmdDirs(t)
	ta := newTestApp(t, nil)
	done := startApp(t, ta, []string{"--state-dir", state, "--runtime-dir", runtime}, runtime+"/daemon.sock")
	h := ownerCmdHello(t, runtime+"/daemon.sock")
	if strings.Join(h.Operations, " ") != "hello status" || h.Mutations {
		t.Fatalf("operations %v mutations %v", h.Operations, h.Mutations)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := clientapi.Dial(ctx, runtime+"/daemon.sock")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	err = c.Call(ctx, ownerwire.OpList, nil, nil)
	var ce *clientapi.Error
	if !errors.As(err, &ce) || ce.Code != clientapi.CodeUnsupportedOperation {
		t.Fatalf("owner.list: %v", err)
	}
	if builds() != 0 {
		t.Fatalf("the builder was called %d times", builds())
	}
	ta.signals <- syscall.SIGTERM
	if code := <-done; code != 0 {
		t.Fatalf("exit %d", code)
	}
}

// TestOwnerRunWithTheFlagWhenThisBuildCannotBuildTheService: the flag is
// refused with the config exit code before anything is made, while no builder
// is set.
func TestOwnerRunWithTheFlagWhenThisBuildCannotBuildTheService(t *testing.T) {
	old := ownerServiceBuilder
	ownerServiceBuilder = nil
	t.Cleanup(func() { ownerServiceBuilder = old })
	state, runtime := ownerCmdDirs(t)
	ta := newTestApp(t, nil)
	code := ta.run([]string{"run", "--state-dir", state, "--runtime-dir", runtime, "--owner-channel"})
	if code != leaddaemon.ExitConfig || !strings.Contains(ta.err.String(), "--owner-channel: this build cannot build the host's side of a card yet") {
		t.Fatalf("exit %d: %q", code, ta.err.String())
	}
	if _, err := os.Lstat(state); err == nil {
		t.Fatal("a state folder was made")
	}
	if _, err := os.Lstat(runtime + "/daemon.sock"); err == nil {
		t.Fatal("a socket was made")
	}
}

// TestOwnerRunWhenTheBuilderFails: a builder that fails stops the daemon with
// the serve step's exit code, with the builder's words through SafeText.
func TestOwnerRunWhenTheBuilderFails(t *testing.T) {
	old, oldGuard := ownerServiceBuilder, ownerGuard
	ownerGuard = nil
	t.Cleanup(func() { ownerGuard = oldGuard })
	ownerServiceBuilder = func(string) (leaddaemon.OwnerService, func() error, error) {
		return nil, nil, errors.New("the journal is locked")
	}
	t.Cleanup(func() { ownerServiceBuilder = old })
	state, runtime := ownerCmdDirs(t)
	ta := newTestApp(t, nil)
	code := ta.run([]string{"run", "--state-dir", state, "--runtime-dir", runtime, "--owner-channel"})
	if code != leaddaemon.ExitServe || !strings.Contains(ta.err.String(), "the journal is locked") {
		t.Fatalf("exit %d: %q", code, ta.err.String())
	}
}

// TestOwnerUsageNamesTheFlagAfterTheReleaseText: the usage of a tagged build
// opens with the release text byte for byte and then says where it does not
// hold; the flag is in the text.
func TestOwnerUsageNamesTheFlagAfterTheReleaseText(t *testing.T) {
	ta := newTestApp(t, nil)
	if code := ta.run([]string{"--help"}); code != 0 {
		t.Fatal(code)
	}
	got := ta.out.String()
	if !strings.HasPrefix(got, baseUsage) {
		t.Fatalf("the usage does not open with the release text:\n%s", got)
	}
	for _, want := range []string{"In this build (tag ataniq_ownerchannel) the text above holds only without", "ataniqd run --owner-channel", "controlling terminal"} {
		if !strings.Contains(got, want) {
			t.Errorf("the usage lacks %q", want)
		}
	}
	if !strings.HasSuffix(got, ownerUsage) {
		t.Errorf("the owner part is not the last part of the usage")
	}
}
