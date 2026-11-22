package switcher

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRunner records every command and fails those whose program's base name
// is in fail (for as many calls as the count says; a negative count fails
// every call).
//
// "nft" prints nftOut. The file given after --nft-listing is recorded as
// FILE in calls; its content and its mode and its folder's are kept in
// listing.
type fakeRunner struct {
	mu       sync.Mutex
	calls    []string
	stdin    [][]byte
	fail     map[string]int
	nftOut   string
	listing  []string // content, file mode, folder mode of each listing seen
	hook     func(argv []string)
	limits   *[]time.Duration // when set, the time limit of every health-probe and slow-probe run
	pid      int              // when set, given to a command's OnStart as its process id
	failPost bool             // the post-activation no-AI check fails
	extra    [][]*os.File     // the ExtraFiles of each command
}

const fakeRuleset = "table inet filter {\n}\n"

func (f *fakeRunner) Run(ctx context.Context, c Command) (Output, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	argv := append([]string(nil), c.Argv...)
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "--nft-listing" {
			data, err := os.ReadFile(argv[i+1])
			fi, _ := os.Stat(argv[i+1])
			di, _ := os.Stat(filepath.Dir(argv[i+1]))
			if err == nil && fi != nil && di != nil {
				f.listing = append(f.listing, string(data), fi.Mode().String(), di.Mode().String())
			}
			argv[i+1] = "FILE"
		}
	}
	f.calls = append(f.calls, strings.Join(argv, " "))
	f.stdin = append(f.stdin, c.Stdin)
	f.extra = append(f.extra, c.ExtraFiles)
	if c.OnStart != nil && f.pid != 0 {
		c.OnStart(f.pid)
	}
	if f.limits != nil && (c.Argv[0] == "health-probe" || c.Argv[0] == "slow-probe") {
		*f.limits = append(*f.limits, c.Timeout)
	}
	if f.hook != nil {
		f.hook(c.Argv)
	}
	name := filepath.Base(c.Argv[0])
	if f.failPost && len(c.Argv) > 2 && c.Argv[1] == "--phase" && c.Argv[2] == CheckPhasePost {
		return Output{Stderr: []byte(name + " said no")}, fmt.Errorf("%s: exit status 1", name)
	}
	if n, ok := f.fail[name]; ok && n != 0 {
		if n > 0 {
			f.fail[name] = n - 1
		}
		return Output{Stderr: []byte(name + " said no")}, fmt.Errorf("%s: exit status 1", name)
	}
	if name == "nft" {
		out := []byte(f.nftOut)
		if c.MaxOutput > 0 && len(out) > c.MaxOutput {
			return Output{Stdout: out[:c.MaxOutput], Truncated: true}, nil
		}
		return Output{Stdout: out}, nil
	}
	return Output{}, nil
}

func (f *fakeRunner) called(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if filepath.Base(strings.Fields(c)[0]) == name {
			n++
		}
	}
	return n
}

func (f *fakeRunner) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
	f.stdin = nil
	f.listing = nil
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) sleep(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type fakeVerifier struct {
	err       error
	calls     int
	data      []byte
	signers   string
	principal string
	sigs      []string
	// What the signers file the verifier was given held, and its mode
	// and its folder's, when it was called.
	signersData, signersMode, signersDirMode string
}

func (v *fakeVerifier) Verify(ctx context.Context, allowedSigners, principal string, data []byte, signaturePath string) error {
	v.calls++
	v.data = data
	v.signers, v.principal = allowedSigners, principal
	v.sigs = append(v.sigs, signaturePath)
	if b, err := os.ReadFile(allowedSigners); err == nil {
		v.signersData = string(b)
	}
	if fi, err := os.Stat(allowedSigners); err == nil {
		v.signersMode = fi.Mode().String()
	}
	if di, err := os.Stat(filepath.Dir(allowedSigners)); err == nil {
		v.signersDirMode = di.Mode().String()
	}
	return v.err
}

// policy writes the switch policy and an allowed signers file below the
// root.
func (e *env) policy(content string) {
	e.t.Helper()
	e.write(filepath.Join(e.root, "etc/authority-host/allowed_signers"), "release@example.invalid ssh-ed25519 AAAA\n", 0o644)
	e.write(filepath.Join(e.root, PolicyDir, PolicyName), content, 0o644)
}

const signedPolicy = `{"min_mode": "signed", "allowed_signers": "/etc/authority-host/allowed_signers", "principal": "release@example.invalid"}`

// env is a temporary root with a fake runner and clock.
type env struct {
	t      *testing.T
	root   string
	runner *fakeRunner
	clock  *fakeClock
	out    bytes.Buffer
	last   string // the output of the last switchTo or rollBack
	in     string // the folder incoming releases are written to
}

func newEnv(t *testing.T) *env {
	t.Helper()
	root := t.TempDir()
	// An imported release is read-only; make it writable again so that the
	// temporary folder can be removed (this runs before that removal).
	t.Cleanup(func() { makeWritable(root) })
	if err := os.MkdirAll(filepath.Join(root, BaseDir, "releases"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The host's own stop list, which every switch requires.
	if err := os.MkdirAll(filepath.Join(root, PolicyDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, PolicyDir, HostStopList), []byte("stop-all\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return &env{
		t:      t,
		root:   root,
		runner: &fakeRunner{fail: map[string]int{}, nftOut: fakeRuleset},
		clock:  &fakeClock{t: time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)},
	}
}

func (e *env) switcher(mod ...func(*Config)) *Switcher {
	cfg := Config{
		Root:      e.root,
		AllowExec: true,
		Runner:    e.runner,
		Out:       &e.out,
		Now:       e.clock.now,
		Sleep:     e.clock.sleep,
	}
	for _, m := range mod {
		m(&cfg)
	}
	return New(cfg)
}

func (e *env) switchTo(id string, mod ...func(*Config)) int {
	e.t.Helper()
	code := e.switcher(mod...).Switch(context.Background(), id)
	e.t.Logf("switch to %s: exit %d\n%s", id, code, e.out.String())
	e.last = e.out.String()
	e.out.Reset()
	return code
}

func (e *env) rollBack(which string) int {
	e.t.Helper()
	code := e.switcher().RollBack(context.Background(), which)
	e.t.Logf("roll back to %s: exit %d\n%s", which, code, e.out.String())
	e.last = e.out.String()
	e.out.Reset()
	return code
}

// resumeOnly is a recover that resumes a journal and then changes nothing:
// its snapshot is never the end of a package step, so it is refused once the
// journal is settled.
var resumeOnly = RecoverOptions{SetPackageState: true, PackageState: "no-such-snapshot"}

// recover runs recover and returns its exit code; its output is in last.
func (e *env) recover(o RecoverOptions) int {
	e.t.Helper()
	code := e.switcher().Recover(context.Background(), o)
	e.t.Logf("recover %+v: exit %d\n%s", o, code, e.out.String())
	e.last = e.out.String()
	e.out.Reset()
	return code
}

// status runs status and returns what it printed; it fails the test when
// status does not exit 0.
func (e *env) status(mod ...func(*Config)) string {
	e.t.Helper()
	code := e.switcher(mod...).Status()
	out := e.out.String()
	e.out.Reset()
	if code != ExitOK {
		e.t.Fatalf("status: exit %d\n%s", code, out)
	}
	return out
}

func (e *env) relDir(id string) string {
	return filepath.Join(e.root, BaseDir, "releases", id)
}

func (e *env) write(path, content string, perm fs.FileMode) {
	e.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		e.t.Fatal(err)
	}
}

// release writes a release folder with the given package snapshot and its
// SHA256SUMS, and returns its path.
func (e *env) release(id, snapshot string) string {
	e.t.Helper()
	return e.releaseIn(e.relDir(id), id, snapshot)
}

// incoming writes a release as release does, into a folder outside the
// root, as the deploying user would copy it there, and returns its path.
func (e *env) incoming(id, snapshot string) string {
	e.t.Helper()
	if e.in == "" {
		e.in = e.t.TempDir()
	}
	return e.releaseIn(filepath.Join(e.in, id), id, snapshot)
}

func (e *env) releaseIn(dir, id, snapshot string) string {
	e.t.Helper()
	rid, err := ParseID(id)
	if err != nil {
		e.t.Fatal(err)
	}
	e.write(filepath.Join(dir, ManifestFile), fmt.Sprintf(
		"id: %s\ncounter: %d\ncommit: %s0123456789\nbuilt_on: dev\ngo_version: fixture-go\npackage_snapshot: %s\n",
		id, rid.Counter, rid.Commit, snapshot), 0o644)
	e.write(filepath.Join(dir, NoAICheckProgram), "#!/bin/sh\nexit 0\n", 0o755)
	e.write(filepath.Join(dir, PackageStepProgram), "#!/bin/sh\nexit 0\n", 0o755)
	e.write(filepath.Join(dir, "bin/app"), "app of "+id+"\n", 0o755)
	e.write(filepath.Join(dir, "etc/systemd/app.service"), "[Service]\n", 0o644)
	e.write(filepath.Join(dir, ValidatorsList), "# units\nvalidate-units verify {release}/etc/systemd/app.service\nvalidate-rules -c\n", 0o644)
	e.write(filepath.Join(dir, ReloadList), "reload-units\n", 0o644)
	e.write(filepath.Join(dir, HealthCmd), "health-probe http://127.0.0.1/healthz\n", 0o644)
	e.write(filepath.Join(dir, StopList), "stop-app\n", 0o644)
	e.sums(dir)
	return dir
}

// sums writes SHA256SUMS over every regular file of dir but the sums and the
// signature.
func (e *env) sums(dir string) {
	e.t.Helper()
	var lines []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		if rel == SumsFile || rel == SignatureFile {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		lines = append(lines, hex.EncodeToString(sum[:])+"  "+filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		e.t.Fatal(err)
	}
	sort.Strings(lines)
	e.write(filepath.Join(dir, SumsFile), strings.Join(lines, "\n")+"\n", 0o644)
	e.stamp(dir)
}

// stamp writes, for a release folder of releases/, the provenance record
// its import would have made, as the tree is now: tests write release
// folders in place of an import. A folder outside releases/, or one that
// does not verify, is left as it is.
func (e *env) stamp(dir string) {
	e.t.Helper()
	if filepath.Dir(dir) != filepath.Join(e.root, BaseDir, "releases") {
		return
	}
	id, err := ParseID(filepath.Base(dir))
	if err != nil {
		return
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		e.t.Fatal(err)
	}
	defer root.Close()
	rel, err := newRelease(id, root, dir, dir, nil, nil)
	if err != nil {
		return
	}
	data, _ := json.Marshal(provenance{ID: id.Name, Digest: rel.digest, Files: rel.files, Time: "2026-11-21T12:00:00Z"})
	e.write(filepath.Join(e.root, StateDir, ImportedDir, id.Name), string(data), 0o644)
}

func (e *env) link(name string) string {
	e.t.Helper()
	target, err := os.Readlink(filepath.Join(e.root, BaseDir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return target
}

func (e *env) record() Record {
	e.t.Helper()
	data, err := os.ReadFile(filepath.Join(e.root, StateDir, DeployFile))
	if err != nil {
		e.t.Fatal(err)
	}
	var rec Record
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rec); err != nil {
		e.t.Fatal(err)
	}
	return rec
}

// deployJSON returns deploy.json as text.
func (e *env) deployJSON() string {
	e.t.Helper()
	data, err := os.ReadFile(filepath.Join(e.root, StateDir, DeployFile))
	if err != nil {
		e.t.Fatal(err)
	}
	return string(data)
}

func (e *env) highest() string {
	e.t.Helper()
	data, err := os.ReadFile(filepath.Join(e.root, StateDir, HighestFile))
	if errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

// digest is a digest of the whole tree below root: every path, its type and
// mode, a file's content and a link's target.
func digest(t *testing.T, root string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		fmt.Fprintf(h, "%s\x00%v\x00", rel, info.Mode())
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(h, "%s\x00", target)
		case info.Mode().IsRegular():
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			h.Write(data)
			h.Write([]byte{0})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// makeWritable adds the owner's write bit to every folder below root.
func makeWritable(root string) {
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			os.Chmod(path, 0o755)
		}
		return nil
	})
}

// mode returns the permission and special bits of path.
func mode(t *testing.T, path string) fs.FileMode {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode() & (fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky)
}

// preCall and postCall are the calls of the no-AI check of the release dir
// in root, before and after activation, as the fake runner records them.
func preCall(prog, root, dir string) string {
	return prog + " --phase pre --root " + root + " --release " + dir + " --require-policy"
}

func postCall(prog, root, dir string) string {
	return prog + " --phase post --root " + root + " --release " + dir + " --require-policy --nft-listing FILE"
}
