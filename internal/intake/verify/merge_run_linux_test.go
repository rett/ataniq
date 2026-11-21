package verify

import (
	"context"
	"strings"
	"testing"

	"ataniq/internal/exchange/sandboxplan/plantest"
)

func mergeRequest(m mergeMirror, work string, p *plantest.Fake, r Runner, gates ...Gate) Request {
	req := request(m.mirror, work, p, r, gates...)
	req.TipRef, req.Tip, req.Merge, req.Base = testPin, m.merge, true, m.target
	return req
}

// TestRunMergeModePasses: the result carries the mode and the base, and the
// gates ran on the merge.
func TestRunMergeModePasses(t *testing.T) {
	m := newMergeMirror(t)
	work := newWork(t)
	res, err := Run(context.Background(), mergeRequest(m, work, fakePlanner(t), ExecRunner{},
		sh("files", "test -f side.txt && test -f target.txt && echo both")))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Passed || res.Exit != 0 || !strings.Contains(string(res.Output), "both\n") {
		t.Fatalf("result: %+v\n%s", res, res.Output)
	}
	if !res.Merge || res.Base != m.target || res.Tip != m.merge || res.TipRef != testPin {
		t.Fatalf("result identity: %+v", res)
	}
	requireEmpty(t, work)
}

// TestRunLeadModeResultHasNoMergeFields: a lead-mode result is as before.
func TestRunLeadModeResultHasNoMergeFields(t *testing.T) {
	m := newMirror(t)
	work := newWork(t)
	res, err := Run(context.Background(), request(m, work, fakePlanner(t), ExecRunner{}, sh("one", "true")))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Passed || res.Merge || res.Base != "" {
		t.Fatalf("result: %+v", res)
	}
}

// TestRunMergeModeFailsClosedOnAnotherBase: a merge whose first parent is not
// the recorded base is not a pass and no gate runs.
func TestRunMergeModeFailsClosedOnAnotherBase(t *testing.T) {
	m := newMergeMirror(t)
	work := newWork(t)
	req := mergeRequest(m, work, fakePlanner(t), ExecRunner{}, sh("one", "echo ran"))
	req.Base = m.root // the target was here when the merge was NOT built
	res, err := Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Passed || res.Exit != ExitNotMerge {
		t.Fatalf("result: %+v\n%s", res, res.Output)
	}
	if strings.Contains(string(res.Output), "ran") || strings.Contains(string(res.Output), MarkerPrefix) {
		t.Fatalf("a gate ran: %s", res.Output)
	}
	if !res.Merge || res.Base != m.root {
		t.Fatalf("result: %+v", res)
	}
	requireEmpty(t, work)
}

// TestRunMergeModeRefusals: the modes are not mixed. Nothing runs.
func TestRunMergeModeRefusals(t *testing.T) {
	m := newMergeMirror(t)
	work := newWork(t)
	cases := map[string]func(r *Request){
		"pin in lead mode":            func(r *Request) { r.Merge, r.Base = false, "" },
		"lead ref in merge":           func(r *Request) { r.TipRef = testRef },
		"merge with no base":          func(r *Request) { r.Base = "" },
		"merge, short base":           func(r *Request) { r.Base = "abc" },
		"merge, base of other format": func(r *Request) { r.Base = strings.Repeat("a", 64) },
	}
	for name, mod := range cases {
		fr := &fakeRunner{}
		p := fakePlanner(t)
		req := mergeRequest(m, work, p, fr, sh("one", "true"))
		mod(&req)
		if _, err := Run(context.Background(), req); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if fr.count() != 0 || len(p.VerifyRequests()) != 0 {
			t.Errorf("%s: something ran", name)
		}
	}
	// A lead-mode request with a base, and one naming a pin.
	for name, mod := range map[string]func(r *Request){
		"lead mode with a base": func(r *Request) { r.Base = m.target },
		"lead mode with a pin":  func(r *Request) { r.TipRef = testPin },
	} {
		fr := &fakeRunner{}
		p := fakePlanner(t)
		req := request(m.mirror, work, p, fr, sh("one", "true"))
		mod(&req)
		if _, err := Run(context.Background(), req); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if fr.count() != 0 || len(p.VerifyRequests()) != 0 {
			t.Errorf("%s: something ran", name)
		}
	}
	requireEmpty(t, work)
}
