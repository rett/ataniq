package main

import (
	"errors"
	"strings"
	"testing"

	"ataniq/internal/exchange/boundedexec"
)

func TestVerifyStoresTheGatesAsShellCommands(t *testing.T) {
	w := newWorld(t)
	fr := &fakeRunner{res: boundedexec.Result{Stdout: []byte("fine\n")}}
	w.runner = fr
	w.initMirror()
	base := w.lead(testRole)
	w.fetch(testRole)
	args := w.verifyArgs(testRole, "main", "make test && echo 'a=b'")
	args = append(args, "--gate", "second=echo two", "--timeout", "5m")
	out := w.mustOK(w.sub("verify", args...))
	if !strings.Contains(out, "Gates: 2, plan digest ") || fr.calls != 1 {
		t.Fatalf("calls %d\n%s", fr.calls, out)
	}
	recs, err := w.store().Verifications(base)
	if err != nil || len(recs) != 1 {
		t.Fatalf("records: %v %v", recs, err)
	}
	g := recs[0].Gates
	if len(g) != 2 || g[0].Name != "check" || strings.Join(g[0].Argv, "|") != "/bin/sh|-c|make test && echo 'a=b'" ||
		g[1].Name != "second" || strings.Join(g[1].Argv, "|") != "/bin/sh|-c|echo two" {
		t.Fatalf("stored gates: %+v", g)
	}
	// The sandbox was asked for the mirror and two fresh folders of the work
	// folder, nothing of the state's lead folders.
	reqs := w.planner.VerifyRequests()
	if len(reqs) != 1 || reqs[0].Mirror != w.mirrorPath() || !strings.HasPrefix(reqs[0].Scratch, w.work+"/") || !strings.HasPrefix(reqs[0].Home, w.work+"/") {
		t.Fatalf("verify requests: %+v", reqs)
	}
}

func TestVerifyThatDoesNotPassExitsOne(t *testing.T) {
	cases := map[string]struct {
		res  boundedexec.Result
		err  error
		want string
	}{
		"failed gate": {res: boundedexec.Result{Exit: 1, Stdout: []byte("bad\n")}, err: &boundedexec.ExitError{Exit: 1}, want: "Outcome: fail"},
		"timeout":     {res: boundedexec.Result{Stdout: []byte("slow\n")}, err: boundedexec.ErrTimeout, want: "Stopped: timeout"},
		"too much":    {res: boundedexec.Result{Stdout: []byte("loud\n")}, err: boundedexec.ErrOutputLimit, want: "Output: cut"},
		"broken":      {res: boundedexec.Result{Exit: -1}, err: errors.New("the runner failed"), want: "Stopped: error"},
	}
	for name, c := range cases {
		w := newWorld(t)
		w.runner = &fakeRunner{res: c.res, err: c.err}
		w.initMirror()
		base := w.lead(testRole)
		w.fetch(testRole)
		code, out, errb := w.sub("verify", w.verifyArgs(testRole, "main", "true")...)
		if code != exitRefused || !strings.Contains(out, c.want) || !strings.Contains(out, "Outcome: fail") {
			t.Errorf("%s: exit %d\n%s\n%s", name, code, out, errb)
		}
		recs, err := w.store().Verifications(base)
		if err != nil || len(recs) != 1 || recs[0].Passed {
			t.Errorf("%s: records %+v %v", name, recs, err)
		}
	}
}

func TestVerifyOfAnUnknownBranchStartsNothing(t *testing.T) {
	w := newWorld(t)
	fr := &fakeRunner{}
	w.runner = fr
	w.initMirror()
	w.lead(testRole)
	w.fetch(testRole)
	code, out, errb := w.sub("verify", w.verifyArgs(testRole, "nosuch", "true")...)
	if code != exitRefused || out != "" || fr.calls != 0 || len(w.planner.VerifyRequests()) != 0 {
		t.Fatalf("exit %d, %d runs\n%s\n%s", code, fr.calls, out, errb)
	}
}

func TestReviewRefusesWhatIsNotAFastForward(t *testing.T) {
	w := newWorld(t)
	w.initMirror()
	w.lead(testRole)
	w.branch(testRole, "side")
	w.write(testRole, map[string]string{"side.txt": "side\n"})
	w.commit(testRole, "side")
	// The upstream branch moves on by a commit that the lead does not have.
	w.mustOK(w.sub("fetch", "--role", testRole, "--sandbox", w.template()))
	fixtureGit(t, w.git, w.tree(testRole), gitEnv(), "checkout", "--quiet", "main")
	w.write(testRole, map[string]string{"up.txt": "up\n"})
	up := w.commit(testRole, "upstream moves")
	fixtureGit(t, w.git, w.tree(testRole), gitEnv(), "push", "--quiet", w.mirrorPath(), up+":refs/heads/main")
	before := w.refs()
	code, out, errb := w.sub("review", "--role", testRole, "--branch", "side", "--target", "main")
	if code != exitRefused || out != "" || !strings.Contains(errb, "not an ancestor") {
		t.Fatalf("exit %d\n%s\n%s", code, out, errb)
	}
	if w.refs() != before {
		t.Fatal("refs changed")
	}
}

func TestReviewOfAnUnknownBranchIsRefused(t *testing.T) {
	w := newWorld(t)
	w.initMirror()
	w.lead(testRole)
	w.fetch(testRole)
	for _, args := range [][]string{
		{"--role", testRole, "--branch", "nosuch", "--target", "main"},
		{"--role", testRole, "--branch", "main", "--target", "nosuch"},
		{"--role", "nobody", "--branch", "main", "--target", "main"},
	} {
		code, out, errb := w.sub("review", args...)
		if code != exitRefused || out != "" || errb == "" {
			t.Errorf("%v: exit %d\n%s\n%s", args, code, out, errb)
		}
	}
}
