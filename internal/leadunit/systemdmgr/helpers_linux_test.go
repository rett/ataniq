package systemdmgr

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/leadunit"
	"ataniq/internal/systemd"
)

const (
	unitName = "ataniq-lead-demo.service"
	inv1     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	inv2     = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	bootID   = "01234567-89ab-cdef-0123-456789abcdef"
	cgBase   = "/user.slice/user-1000.slice/user@1000.service"
	cgUnit   = cgBase + "/app.slice/" + unitName
	jobReply = "o \"/org/freedesktop/systemd1/job/42\"\n"
)

var (
	idA = systemd.FileIdentity{Device: 7, Inode: 100, UID: 1000}
	idB = systemd.FileIdentity{Device: 7, Inode: 200, UID: 1000}
)

// fakeRunner records every argv and answers from handle. No tool is run.
type fakeRunner struct {
	mu     sync.Mutex
	calls  [][]string
	handle func(argv []string) (string, error)
}

func (r *fakeRunner) Run(ctx context.Context, argv []string) (string, error) {
	r.mu.Lock()
	r.calls = append(r.calls, append([]string(nil), argv...))
	h := r.handle
	r.mu.Unlock()
	if h == nil {
		return "", errors.New("no handler")
	}
	return h(argv)
}

func (r *fakeRunner) recorded() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]string(nil), r.calls...)
}

// blockingRunner returns only when its context ends.
type blockingRunner struct {
	mu    sync.Mutex
	calls int
}

func (r *blockingRunner) Run(ctx context.Context, argv []string) (string, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	<-ctx.Done()
	return "", ctx.Err()
}

// fakeKernel is the cgroup file system and boot identity of the tests.
type fakeKernel struct {
	mu        sync.Mutex
	groups    map[string]systemd.FileIdentity
	populated map[string]bool
	lookupErr map[string]error
	popErr    error
	named     map[string][]string
	findErr   error
	boot      string
	bootErr   error
	lookups   []string
	finds     []string
	popCalls  int
}

func newFakeKernel() *fakeKernel {
	return &fakeKernel{
		groups:    map[string]systemd.FileIdentity{},
		populated: map[string]bool{},
		lookupErr: map[string]error{},
		named:     map[string][]string{},
		boot:      bootID,
	}
}

func (k *fakeKernel) Lookup(p string) (systemd.Boundary, bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.lookups = append(k.lookups, p)
	if err := k.lookupErr[p]; err != nil {
		return systemd.Boundary{}, false, err
	}
	id, ok := k.groups[p]
	if !ok {
		return systemd.Boundary{}, false, nil
	}
	return systemd.Boundary{Path: p, Identity: id}, true, nil
}

func (k *fakeKernel) Populated(b systemd.Boundary) (bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.popCalls++
	if k.popErr != nil {
		return false, k.popErr
	}
	id, ok := k.groups[b.Path]
	if !ok || id != b.Identity {
		return false, errors.New("cgroup identity missing or changed")
	}
	return k.populated[b.Path], nil
}

func (k *fakeKernel) FindNamed(base, name string) ([]string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.finds = append(k.finds, base+" "+name)
	if k.findErr != nil {
		return nil, k.findErr
	}
	return k.named[base+" "+name], nil
}

func (k *fakeKernel) BootID() (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.boot, k.bootErr
}

// show renders canned "systemctl show --all" output in the order systemctl
// uses, which differs from the order of the request.
func show(kv map[string]string) string {
	order := []string{"Id", "LoadState", "ActiveState", "SubState", "InvocationID", "ControlGroup", "MainPID", "Description", "Transient", "BindsTo"}
	var b strings.Builder
	for _, k := range order {
		if v, ok := kv[k]; ok {
			fmt.Fprintf(&b, "%s=%s\n", k, v)
		}
	}
	return b.String()
}

func goodDesc() string { return validSpec().Description() }

// loadedProps are the properties of a running unit of ours.
func loadedProps() map[string]string {
	return map[string]string{
		"Id": unitName, "LoadState": "loaded", "ActiveState": "active", "SubState": "running",
		"InvocationID": inv1, "ControlGroup": cgUnit, "MainPID": "321",
		"Description": goodDesc(), "Transient": "yes", "BindsTo": "",
	}
}

func notFoundProps() map[string]string {
	return map[string]string{
		"Id": unitName, "LoadState": "not-found", "ActiveState": "inactive", "SubState": "dead",
		"InvocationID": "", "ControlGroup": "", "MainPID": "0",
		"Description": unitName, "Transient": "no", "BindsTo": "",
	}
}

func with(kv map[string]string, over ...string) map[string]string {
	out := map[string]string{}
	for k, v := range kv {
		out[k] = v
	}
	for i := 0; i+1 < len(over); i += 2 {
		out[over[i]] = over[i+1]
	}
	return out
}

func validSpec() leadunit.Spec {
	return leadunit.Spec{
		Unit:    unitName,
		Epoch:   3,
		Intent:  "intent-7",
		Argv:    []string{"/usr/bin/lead", "--name", "a b", "$HOME", "%h"},
		WorkDir: "/srv/work",
		Env:     []string{"A=1", "PATH=/usr/bin:/bin"},
	}
}

// rig is a Manager over a scripted runner and a fake kernel.
type rig struct {
	t      *testing.T
	run    *fakeRunner
	k      *fakeKernel
	m      *Manager
	show   map[string]string // canned show output by unit; "-.slice" too
	showFn func() (string, error)
	start  func() (string, error)
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, run: &fakeRunner{}, k: newFakeKernel(), show: map[string]string{}}
	r.show[unitName] = show(loadedProps())
	r.show["-.slice"] = "ControlGroup=" + cgBase + "\n"
	r.k.groups[cgUnit] = idA
	r.k.groups[cgBase] = idB
	r.start = func() (string, error) { return jobReply, nil }
	r.run.handle = func(argv []string) (string, error) {
		switch {
		case argv[0] == "busctl":
			return r.start()
		case argv[0] == "systemctl" && argv[2] == "show":
			if r.showFn != nil {
				return r.showFn()
			}
			out, ok := r.show[argv[len(argv)-1]]
			if !ok {
				return "", errors.New("unexpected unit")
			}
			return out, nil
		case argv[0] == "systemctl" && argv[2] == "kill":
			return "", errors.New("systemctl kill is refused")
		}
		return "", errors.New("unexpected call")
	}
	r.m = newManager(r.run, r.k, 0)
	return r
}

func (r *rig) setUnit(kv map[string]string) { r.show[unitName] = show(kv) }

func (r *rig) callNames() []string {
	var out []string
	for _, c := range r.run.recorded() {
		name := c[0]
		if name != "busctl" {
			name += " " + c[2]
		}
		out = append(out, name)
	}
	return out
}

func mustEqual(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %#v\nwant %#v", got, want)
	}
}

var errBoom = errors.New("boom")

func exitErr() error { return errors.New("exit status 1") }

func notExist() error { return fs.ErrNotExist }

func deadlineCtx(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}
