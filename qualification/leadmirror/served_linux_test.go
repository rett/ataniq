package leadmirror

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"ataniq/internal/exchange/sandboxplan"
)

// These tests read the inode of a folder, which needs syscall.Stat_t, so they
// are in a Linux file.

// snapPlanner passes requests to a planner. At every serve request it
// snapshots the clone that is asked for, so that a test can tell whether the
// harness changed a clone between two requests that name it, or after the last
// one. The clone of the planted case is checked at every request: nothing
// writes it between the requests. In the S16 case the first request comes
// before the plants, so only the last snapshot of each clone is compared.
type snapPlanner struct {
	t     *testing.T
	inner sandboxplan.Planner
	mu    sync.Mutex
	snaps map[uint64]map[string]string // inode of the clone folder -> last snapshot
}

func (p *snapPlanner) Serve(q sandboxplan.ServeRequest) (sandboxplan.ServePlan, error) {
	if fi, err := os.Lstat(q.Tree); err == nil && fi.IsDir() {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			now := snapshotTree(q.Tree)
			p.mu.Lock()
			if p.snaps == nil {
				p.snaps = map[uint64]map[string]string{}
			}
			if before, ok := p.snaps[st.Ino]; ok && strings.Contains(q.Tree, "/cases/planted/") {
				if d := diffSnapshots(before, now); d != "" {
					p.t.Errorf("%s was written between two requests: %s", q.Tree, d)
				}
			}
			p.snaps[st.Ino] = now
			p.mu.Unlock()
		}
	}
	return p.inner.Serve(q)
}

// diffSnapshots names the first difference of two snapshots, or "".
func diffSnapshots(want, got map[string]string) string {
	for name, w := range want {
		if got[name] != w {
			return fmt.Sprintf("%s was %q, is %q", name, w, got[name])
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			return name + " was added"
		}
	}
	return ""
}

func (p *snapPlanner) Verify(q sandboxplan.VerifyRequest) (sandboxplan.VerifyPlan, error) {
	return p.inner.Verify(q)
}

// snapshotTree describes every entry below dir (not dir itself): its kind,
// size and modification time. Folder times change when a name is added or
// removed, so a file made and removed again shows.
func snapshotTree(dir string) map[string]string {
	snap := map[string]string{}
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == dir {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		snap[rel] = fmt.Sprintf("%v %d %d", fi.Mode(), fi.Size(), fi.ModTime().UnixNano())
		return nil
	})
	return snap
}

// TestNothingIsWrittenIntoAServedClone: after a request for a clone that the
// planner answered, the harness does not write into that clone, by path or
// otherwise. The clone may be moved away whole.
func TestNothingIsWrittenIntoAServedClone(t *testing.T) {
	p := &snapPlanner{t: t, inner: fakePlanner(t)}
	_, o := runFake(t, p)
	if len(p.snaps) < 4 {
		t.Fatalf("only %d clones were requested", len(p.snaps))
	}
	found := 0
	_ = filepath.WalkDir(o.StateRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			return nil
		}
		want, ok := p.snaps[st.Ino]
		if !ok {
			return nil
		}
		found++
		if d := diffSnapshots(want, snapshotTree(path)); d != "" {
			t.Errorf("%s was written after the last request: %s", path, d)
		}
		return nil
	})
	if found != len(p.snaps) {
		t.Errorf("found %d of the %d requested clones in the state root", found, len(p.snaps))
	}
}
