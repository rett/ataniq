package plantest

import (
	"bytes"
	"errors"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"ataniq/internal/exchange/sandboxplan"
)

func gitEnv() []string {
	return []string{
		"PATH=/usr/bin:/bin", "HOME=/nonexistent", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
	}
}

func runCmd(t *testing.T, c sandboxplan.Command) []byte {
	t.Helper()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(c.Argv[0], c.Argv[1:]...)
	cmd.Env = c.Env
	cmd.Dir = t.TempDir()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%v: %v", c.Argv, err)
	}
	return out
}

func TestFakeServe(t *testing.T) {
	f := &Fake{
		UploadPack:   []string{"/usr/bin/git-upload-pack"},
		MeasureBytes: 4096,
	}
	req := sandboxplan.ServeRequest{Role: "lead", Project: "proj", Tree: "/srv/tree"}
	p, err := f.Serve(req)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.UploadPack, []string{"/usr/bin/git-upload-pack"}) || p.Repository != "/srv/tree" {
		t.Fatalf("plan: %#v", p)
	}
	n, err := sandboxplan.ParseMeasure(runCmd(t, p.Measure))
	if err != nil || n != 4096 {
		t.Fatalf("measure printed %d, %v", n, err)
	}
	if got := f.ServeRequests(); !reflect.DeepEqual(got, []sandboxplan.ServeRequest{req}) {
		t.Fatalf("requests: %#v", got)
	}

	// An explicit repository and measure command win.
	f.Repository = "/inside"
	f.Measure = sandboxplan.Command{Argv: []string{"/bin/echo", "7"}}
	p, err = f.Serve(req)
	if err != nil || p.Repository != "/inside" || p.Measure.Argv[0] != "/bin/echo" {
		t.Fatalf("explicit plan: %#v, %v", p, err)
	}
	if len(f.ServeRequests()) != 2 {
		t.Fatalf("requests: %#v", f.ServeRequests())
	}
}

func TestFakeServeFails(t *testing.T) {
	boom := errors.New("boom")
	f := &Fake{UploadPack: []string{"/x"}, ServeErr: boom}
	req := sandboxplan.ServeRequest{Role: "lead", Project: "proj", Tree: "/t"}
	if _, err := f.Serve(req); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	if len(f.ServeRequests()) != 1 {
		t.Fatal("a failed call is not recorded")
	}
	// A plan that Validate refuses is an error, not a plan.
	for name, g := range map[string]*Fake{
		"zero":         {},
		"relative":     {UploadPack: []string{"git-upload-pack"}},
		"bad measure":  {UploadPack: []string{"/x"}, Measure: sandboxplan.Command{Argv: []string{"du"}}},
		"bad measure2": {UploadPack: []string{"/x"}, Measure: sandboxplan.Command{Argv: []string{"/du"}, Env: []string{"X"}}},
	} {
		if _, err := g.Serve(req); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestFakeRefusesBadRequests(t *testing.T) {
	f := &Fake{UploadPack: []string{"/x"}, VerifyPrefix: []string{"/y"}}
	serveOK := sandboxplan.ServeRequest{Role: "lead", Project: "proj", Tree: "/t"}
	if _, err := f.Serve(serveOK); err != nil {
		t.Fatal(err)
	}
	for name, mod := range map[string]func(*sandboxplan.ServeRequest){
		"empty role":    func(r *sandboxplan.ServeRequest) { r.Role = "" },
		"upper role":    func(r *sandboxplan.ServeRequest) { r.Role = "Lead" },
		"empty project": func(r *sandboxplan.ServeRequest) { r.Project = "" },
		"relative tree": func(r *sandboxplan.ServeRequest) { r.Tree = "t" },
		"root tree":     func(r *sandboxplan.ServeRequest) { r.Tree = "/" },
		"brace tree":    func(r *sandboxplan.ServeRequest) { r.Tree = "/t/{x}" },
		"dotdot tree":   func(r *sandboxplan.ServeRequest) { r.Tree = "/t/../u" },
	} {
		r := serveOK
		mod(&r)
		if _, err := f.Serve(r); err == nil {
			t.Errorf("serve %s: accepted", name)
		}
	}
	verifyOK := sandboxplan.VerifyRequest{Project: "proj", Mirror: "/m", Scratch: "/s", Home: "/h"}
	if _, err := f.Verify(verifyOK); err != nil {
		t.Fatal(err)
	}
	for name, mod := range map[string]func(*sandboxplan.VerifyRequest){
		"empty project":   func(r *sandboxplan.VerifyRequest) { r.Project = "" },
		"relative mirror": func(r *sandboxplan.VerifyRequest) { r.Mirror = "m" },
		"root mirror":     func(r *sandboxplan.VerifyRequest) { r.Mirror = "/" },
		"same folder":     func(r *sandboxplan.VerifyRequest) { r.Home = r.Scratch },
		"home in scratch": func(r *sandboxplan.VerifyRequest) { r.Home = r.Scratch + "/h" },
		"brace scratch":   func(r *sandboxplan.VerifyRequest) { r.Scratch = "/s/{x}" },
	} {
		r := verifyOK
		mod(&r)
		if _, err := f.Verify(r); err == nil {
			t.Errorf("verify %s: accepted", name)
		}
	}
	// An error the test set comes first.
	boom := errors.New("boom")
	g := &Fake{ServeErr: boom, VerifyErr: boom}
	if _, err := g.Serve(sandboxplan.ServeRequest{}); !errors.Is(err, boom) {
		t.Errorf("serve with ServeErr: %v", err)
	}
	if _, err := g.Verify(sandboxplan.VerifyRequest{}); !errors.Is(err, boom) {
		t.Errorf("verify with VerifyErr: %v", err)
	}
	// Failed calls are recorded too.
	if got := len(f.ServeRequests()); got != 8 {
		t.Errorf("serve requests recorded: %d", got)
	}
	if got := len(f.VerifyRequests()); got != 7 {
		t.Errorf("verify requests recorded: %d", got)
	}
}

func TestFakeVerify(t *testing.T) {
	f := &Fake{VerifyPrefix: []string{"/usr/bin/env"}, VerifyEnv: []string{"PATH=/usr/bin:/bin"}}
	req := sandboxplan.VerifyRequest{Project: "proj", Mirror: "/m", Scratch: "/s", Home: "/h"}
	p, err := f.Verify(req)
	if err != nil {
		t.Fatal(err)
	}
	want := sandboxplan.VerifyPlan{
		Prefix: []string{"/usr/bin/env"}, Env: []string{"PATH=/usr/bin:/bin"},
		Mirror: "/m", Scratch: "/s", Home: "/h",
	}
	if !reflect.DeepEqual(p, want) {
		t.Fatalf("got %#v", p)
	}
	cmd, err := p.Command([]string{"/bin/echo", "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if out := runCmd(t, cmd); !bytes.Equal(out, []byte("hi\n")) {
		t.Fatalf("output %q", out)
	}
	if got := f.VerifyRequests(); !reflect.DeepEqual(got, []sandboxplan.VerifyRequest{req}) {
		t.Fatalf("requests: %#v", got)
	}

	f.VerifyMirror, f.VerifyScratch, f.VerifyHome = "/in/m", "/in/s", "/in/h"
	p, err = f.Verify(req)
	if err != nil || p.Mirror != "/in/m" || p.Scratch != "/in/s" || p.Home != "/in/h" {
		t.Fatalf("inside paths: %#v, %v", p, err)
	}

	boom := errors.New("boom")
	f.VerifyErr = boom
	if _, err := f.Verify(req); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	if len(f.VerifyRequests()) != 3 {
		t.Fatalf("requests: %#v", f.VerifyRequests())
	}
	if _, err := (&Fake{}).Verify(req); err == nil {
		t.Fatal("a fake with no prefix returned a plan")
	}
}

func TestFakeRecordsCopies(t *testing.T) {
	f := &Fake{UploadPack: []string{"/x"}}
	f.Serve(sandboxplan.ServeRequest{Role: "a", Project: "p", Tree: "/t"})
	got := f.ServeRequests()
	got[0].Role = "changed"
	if f.ServeRequests()[0].Role != "a" {
		t.Fatal("recorded request changed through the copy")
	}
	p, _ := f.Serve(sandboxplan.ServeRequest{Role: "a", Project: "p", Tree: "/t"})
	p.UploadPack[0] = "/changed"
	if f.UploadPack[0] != "/x" {
		t.Fatal("field changed through the plan")
	}
}

func TestFakeConcurrent(t *testing.T) {
	f := &Fake{UploadPack: []string{"/x"}, VerifyPrefix: []string{"/y"}}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.Serve(sandboxplan.ServeRequest{Role: "a", Project: "p", Tree: "/t"})
			f.Verify(sandboxplan.VerifyRequest{Project: "p", Mirror: "/m", Scratch: "/s", Home: "/h"})
		}()
	}
	wg.Wait()
	if len(f.ServeRequests()) != 8 || len(f.VerifyRequests()) != 8 {
		t.Fatal("lost requests")
	}
}

func TestGitUploadPackServesATemporaryRepository(t *testing.T) {
	argv, err := GitUploadPack()
	if err != nil {
		t.Skipf("no git-upload-pack: %v", err)
	}
	repo := filepath.Join(t.TempDir(), "repo")
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Env = gitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", repo)
	run("-C", repo, "commit", "-q", "--allow-empty", "-m", "one")

	f := &Fake{UploadPack: argv}
	p, err := f.Serve(sandboxplan.ServeRequest{Role: "lead", Project: "proj", Tree: repo})
	if err != nil {
		t.Fatal(err)
	}
	quoted, err := sandboxplan.ShellQuote(p.UploadPack)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "clone")
	run("clone", "-q", "--upload-pack", quoted, p.Repository, dst)
	rev := exec.Command("git", "-C", dst, "rev-parse", "HEAD")
	rev.Env = gitEnv()
	if _, err := rev.Output(); err != nil {
		t.Fatalf("clone has no HEAD: %v", err)
	}
}
