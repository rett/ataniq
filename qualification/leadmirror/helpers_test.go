package leadmirror

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"ataniq/internal/exchange/gitenv"
	"ataniq/internal/exchange/sandboxplan"
	"ataniq/internal/exchange/sandboxplan/plantest"
)

// These tests never start a sandbox. The planner is plantest.Fake, which runs
// the host's plain git-upload-pack and a plain env, with no namespace. The
// facts they check are the shape of the report and the refusals, not what a
// sandbox does.

func needGit(t *testing.T) gitenv.Git {
	t.Helper()
	g, err := gitenv.Find()
	if err != nil {
		t.Fatalf("these tests need an installed git: %v", err)
	}
	return g
}

// fakePlanner returns a Fake with an upload-pack, a measure of 1000 bytes
// (over the small MaxSource of the oversize case) and a verify prefix.
func fakePlanner(t *testing.T) *plantest.Fake {
	t.Helper()
	gup, err := plantest.GitUploadPack()
	if err != nil {
		t.Fatalf("these tests need git-upload-pack: %v", err)
	}
	return &plantest.Fake{
		UploadPack:   gup,
		MeasureBytes: 1000,
		VerifyPrefix: []string{"/usr/bin/env"},
		VerifyEnv: []string{
			"PATH=/usr/bin:/bin", "HOME=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0",
		},
	}
}

// newRoot returns a state root path that does not exist yet.
func newRoot(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "scratch")
}

func options(t *testing.T, p sandboxplan.Planner) Options {
	t.Helper()
	return Options{
		StateRoot: newRoot(t), Role: "coder", Project: "proj",
		Planner: p, Git: needGit(t), TemplateDigest: "test-digest",
	}
}

// runFake runs the whole harness with the given planner and returns the
// report and the options used.
func runFake(t *testing.T, p sandboxplan.Planner) (*Report, Options) {
	t.Helper()
	o := options(t, p)
	rep, err := Run(context.Background(), o)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return rep, o
}

// findFact returns the fact with the id, or fails the test.
func findFact(t *testing.T, rep *Report, id string) Fact {
	t.Helper()
	for _, c := range rep.Cases {
		for _, f := range c.Facts {
			if f.ID == id {
				return f
			}
		}
	}
	t.Fatalf("no fact %q", id)
	return Fact{}
}

// asJSON converts the report to generic JSON.
func asJSON(t *testing.T, rep *Report) map[string]any {
	t.Helper()
	data, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
