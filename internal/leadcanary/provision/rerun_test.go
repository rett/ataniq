//go:build linux && ataniq_canary

package provision

import (
	"errors"
	"os"
	"testing"

	"ataniq/internal/leadcanary/provision/provisiontest"
)

func TestAfterAFailedRunTheAdviceOfThePackageCommentLeadsToAGoodRun(t *testing.T) {
	// A run that fails late: everything is made, the check of the result
	// refuses, and there is no marker.
	w := provisiontest.New(t)
	p := mustPaths(t, w)
	old := GitPath
	GitPath = tamperingGit(t, "clone", "    mkdir -p .git/objects/info && printf '%s\\n' /nonexistent/objects > .git/objects/info/alternates || exit 1")
	_, err := provisionWorld(w)
	GitPath = old
	if err == nil || exists(p.Marker) {
		t.Fatalf("the first run was to fail without a marker: %v", err)
	}
	// As it is, a second run is refused for what the first left.
	before := snapshot(t, w.State)
	if _, err := provisionWorld(w); err == nil || errors.Is(err, ErrFirstStart) {
		t.Fatalf("the second run: %v, want a refusal of what the first run left", err)
	}
	if snapshot(t, w.State) != before {
		t.Fatal("the refused second run changed something")
	}
	// The advice: remove the lead's folder, its mailbox and published
	// folders and the runtime root. Not the state root, which holds the
	// mirror.
	for _, d := range []string{p.LeadDir, p.Mailbox, p.Published, p.RuntimeRoot} {
		if !exists(d) && d != p.RuntimeRoot {
			t.Fatalf("%s was not made by the failed run; the test shows less than it says", d)
		}
		if err := os.RemoveAll(d); err != nil {
			t.Fatal(err)
		}
	}
	lines, err := provisionWorld(w)
	if err != nil {
		t.Fatalf("the run after the advice: %v\n%q", err, lines)
	}
	if !exists(p.Marker) {
		t.Fatal("no marker")
	}
	if got := provisiontest.Git(t, p.Tree, "rev-parse", "HEAD"); got != w.Head {
		t.Fatalf("the tree is at %s, the mirror's branch at %s", got, w.Head)
	}
}
