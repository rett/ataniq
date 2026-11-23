//go:build linux && ataniq_ownerchannel

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"ataniq/internal/approval/hostadapter"
	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/clientapi"
)

// keysPathOf is the file of the list of keys in a state folder.
func keysPathOf(state string) string {
	return filepath.Join(state, hostadapter.OwnerDirName, ownerjournal.KeysFileName)
}

// keyWords is a fingerprint of as many words as the list of keys holds, so
// that the test follows the list's count and names no count of its own.
func keyWords() string {
	pool := []string{"acorn", "bridge", "cedar", "delta", "ember", "falcon", "garden", "harbor", "island", "jungle"}
	return strings.Join(pool[:ownerjournal.FingerprintWords], " ")
}

// noticed collects what the builder tells the operator, in place of the
// standard error.
func noticed(t *testing.T) func() string {
	t.Helper()
	old := ownerNotice
	var mu sync.Mutex
	var lines []string
	ownerNotice = func(line string) {
		mu.Lock()
		lines = append(lines, line)
		mu.Unlock()
	}
	t.Cleanup(func() { ownerNotice = old })
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(lines, "\n")
	}
}

// keyOps is the service as the daemon calls it for the list of keys.
type keyOps interface {
	Keys(ctx context.Context) (ownerwire.KeysView, error)
	PairStatus(ctx context.Context) (ownerwire.PairStatus, error)
}

// The builder opens the list of keys in the owner folder, makes the file private
// when it is new, gives it to the service, and the service's close frees it.
func TestTheBuilderGivesTheServiceTheListOfKeys(t *testing.T) {
	useSuite(t, cmdSuite{})
	notes := noticed(t)
	state, _ := scratchSettings(t, nil)
	svc, closer, err := buildOwnerService(state)
	if err != nil {
		t.Fatal(err)
	}
	path := keysPathOf(state)
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 || !st.Mode().IsRegular() {
		t.Fatalf("the file of the list: %v, %v", st, err)
	}
	view, err := svc.(keyOps).Keys(context.Background())
	if err != nil || len(view.Keys) != 0 {
		t.Fatalf("Keys = %+v, %v", view, err)
	}
	if _, err := ownerjournal.OpenKeys(path); !errors.Is(err, ownerjournal.ErrLocked) {
		t.Fatalf("the list was opened a second time while the service holds it: %v", err)
	}
	if err := closer(); err != nil {
		t.Fatal(err)
	}
	k, err := ownerjournal.OpenKeys(path)
	if err != nil {
		t.Fatalf("the list is still held after the close: %v", err)
	}
	// What a key put there is back at the next build.
	if _, err := k.AddKey(ownerjournal.NewKey{
		CredentialID: []byte(strings.Repeat("c", 16)), PublicKey: []byte("k"), Kind: "unverified",
		PairedAt: time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC), Words: keyWords(),
	}); err != nil {
		t.Fatal(err)
	}
	k.Close()
	svc, closer, err = buildOwnerService(state)
	if err != nil {
		t.Fatal(err)
	}
	defer closer()
	view, err = svc.(keyOps).Keys(context.Background())
	if err != nil || len(view.Keys) != 1 || view.Keys[0].Kind != "unverified" {
		t.Fatalf("Keys after a build = %+v, %v", view, err)
	}
	if n := notes(); n != "" {
		t.Fatalf("the builder said %q", n)
	}
}

// A list that cannot be opened does not stop the daemon: the service is built,
// the approvals are served, the operation on the keys is refused, and the file
// is left as it is. The operator is told once, and the folder of the path is the
// owner's own, so no reader of the notice is told of another's.
func TestTheBuilderGivesTheServiceNoListWhenTheFileCannotBeOpened(t *testing.T) {
	useSuite(t, cmdSuite{})
	for name, damage := range map[string]func(t *testing.T, path string){
		"a damaged file": func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("not a list of keys\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"an empty file": func(t *testing.T, path string) {
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"a file the group can read": func(t *testing.T, path string) {
			if err := os.Chmod(path, 0o640); err != nil {
				t.Fatal(err)
			}
		},
		"a link": func(t *testing.T, path string) {
			if err := os.Rename(path, path+".moved"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(path+".moved", path); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			notes := noticed(t)
			state, _ := scratchSettings(t, nil)
			path := keysPathOf(state)
			k, err := ownerjournal.OpenKeys(path)
			if err != nil {
				t.Fatal(err)
			}
			k.Close()
			damage(t, path)
			before, _ := os.ReadFile(path)
			svc, closer, err := buildOwnerService(state)
			if err != nil {
				t.Fatalf("the daemon's service was not built for want of a list: %v", err)
			}
			defer closer()
			// The approvals are served.
			if _, err := svc.List(context.Background()); err != nil {
				t.Fatalf("List: %v", err)
			}
			// The keys are refused, and the status, which needs no list, is not.
			if _, err := svc.(keyOps).Keys(context.Background()); !errors.Is(err, ownerchannel.ErrKeysUnavailable) {
				t.Fatalf("Keys: %v, want %v", err, ownerchannel.ErrKeysUnavailable)
			}
			if st, err := svc.(keyOps).PairStatus(context.Background()); err != nil || st.Phase != ownerwire.PairNone {
				t.Fatalf("PairStatus: %+v, %v", st, err)
			}
			if n := notes(); strings.Count(n, "cannot be opened") != 1 {
				t.Fatalf("the notice is %q", n)
			}
			if after, _ := os.ReadFile(path); string(after) != string(before) {
				t.Fatal("the file was changed")
			}
		})
	}
}

// A second daemon on the same state folder cannot take the list: the service of
// the first holds it, and the second is built with none and says so.
func TestTheBuilderOfASecondServiceOnTheSameListGetsNone(t *testing.T) {
	useSuite(t, cmdSuite{})
	state, _ := scratchSettings(t, nil)
	first, closer, err := buildOwnerService(state)
	if err != nil {
		t.Fatal(err)
	}
	defer closer()
	_ = first
	// The journal is held too, so a second build is refused at the journal; the
	// list's own refusal is what the notice would say first.
	notes := noticed(t)
	if _, _, err := buildOwnerService(state); err == nil {
		t.Fatal("a second service was built on a state folder that is held")
	}
	if !strings.Contains(notes(), "cannot be opened") {
		t.Fatalf("the notice is %q", notes())
	}
}

// The daemon with the flag serves the pairing operations over the real service
// and the real list: the owner starts a pairing and reads the status and the
// empty list, a write from a connection that is not the decider is refused, and
// the file is private and free after the daemon stops.
func TestOwnerRunServesThePairingOperationsOverTheListInTheStateFolder(t *testing.T) {
	useSuite(t, cmdSuite{})
	old := ownerInspector
	ownerInspector = ownerCmdTerminal{}
	t.Cleanup(func() { ownerInspector = old })
	state, _ := scratchSettings(t, nil)
	runtime := filepath.Dir(state) + "/r"
	if err := os.Mkdir(runtime, 0o700); err != nil {
		t.Fatal(err)
	}
	ta := newTestApp(t, nil)
	sock := runtime + "/daemon.sock"
	done := startApp(t, ta, []string{"--state-dir", state, "--runtime-dir", runtime, "--owner-channel"}, sock)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := clientapi.Dial(ctx, sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var keys ownerwire.KeysView
	if err := c.Call(ctx, ownerwire.OpKeys, nil, &keys); err != nil || len(keys.Keys) != 0 {
		t.Fatalf("owner.keys: %+v, %v", keys, err)
	}
	// Not the decider yet: a write is refused.
	var ce *clientapi.Error
	if err := c.Call(ctx, ownerwire.OpPairStart, ownerwire.PairStartParams{}, nil); !errors.As(err, &ce) || ce.Code != "not-decider" {
		t.Fatalf("owner.pair-start before the decider's place: %v", err)
	}
	var dr ownerwire.DeciderResult
	if err := c.Call(ctx, ownerwire.OpDecider, ownerwire.DeciderParams{Client: "ataniq-console 0.0.11", Session: "0123456789abcdef"}, &dr); err != nil || !dr.Decider {
		t.Fatalf("owner.decider: %+v, %v", dr, err)
	}
	if err := c.Call(ctx, ownerwire.OpBeat, nil, nil); err != nil {
		t.Fatal(err)
	}
	var start ownerwire.PairStartResult
	if err := c.Call(ctx, ownerwire.OpPairStart, ownerwire.PairStartParams{}, &start); err != nil || len(start.Code) != ownerwire.PairCodeLength {
		t.Fatalf("owner.pair-start: %v, %v", start, err)
	}
	var st ownerwire.PairStatus
	if err := c.Call(ctx, ownerwire.OpPairStatus, nil, &st); err != nil || st.Phase != ownerwire.PairWaiting {
		t.Fatalf("owner.pair-status: %+v, %v", st, err)
	}
	// seat.pair has no seat source in this build.
	over := ownerwire.SeatPairParams{Code: start.Code, CredentialID: make([]byte, 32), PublicKey: make([]byte, 91)}
	if err := c.Call(ctx, ownerwire.OpSeatPair, over, nil); !errors.As(err, &ce) || ce.Code != "unavailable" {
		t.Fatalf("seat.pair: %v", err)
	}
	if strings.Contains(ta.err.String(), start.Code) {
		t.Fatalf("the code is in the daemon's log:\n%s", ta.err.String())
	}
	c.Close()
	ta.signals <- syscall.SIGTERM
	if code := <-done; code != 0 {
		t.Fatalf("exit %d: %s", code, ta.err.String())
	}
	k, err := ownerjournal.OpenKeys(keysPathOf(state))
	if err != nil {
		t.Fatalf("the list is held after the daemon stopped: %v", err)
	}
	k.Close()
}
