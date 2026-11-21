//go:build linux && ataniq_canary

package provision

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/leadcanary/provision/provisiontest"
	"ataniq/internal/leadcanary/standin"
)

func TestPathsOfNamesTheLayoutAndTheSources(t *testing.T) {
	w := provisiontest.New(t)
	p, err := PathsOf(w.Cfg)
	if err != nil {
		t.Fatal(err)
	}
	lead := filepath.Join(w.State, "leads", "throwaway-one-lead")
	for name, c := range map[string][2]string{
		"holder":       {p.Holder, lead + "/holder"},
		"tree":         {p.Tree, lead + "/tree"},
		"gitmeta":      {p.GitMeta, lead + "/gitmeta"},
		"mirror":       {p.Mirror, filepath.Join(w.State, "mirrors", "throwaway-one.git")},
		"mailbox":      {p.Mailbox, filepath.Join(w.State, "mailbox", "throwaway-one-lead")},
		"published":    {p.Published, filepath.Join(w.State, "pub", "throwaway-one")},
		"roles":        {p.RolesDir, filepath.Join(w.State, "roles")},
		"role folder":  {p.RoleDir, filepath.Join(w.State, "roles", "throwaway-one")},
		"role lock":    {p.RoleLock, filepath.Join(w.State, "roles", "throwaway-one", "lead.lock")},
		"marker":       {p.Marker, filepath.Join(w.State, "canary-root.json")},
		"runtime":      {p.RuntimeFolder, filepath.Join(w.Runtime, "leads", "throwaway-one-lead")},
		"guard hook":   {p.GuardHook, lead + "/qargi-install/guard-hook"},
		"git identity": {p.GitIdentityFile, lead + "/git-identity/config"},
		"replay":       {p.ReplayDir, lead + "/home/canary-replay"},
		"switch":       {p.ReplaySwitch, lead + "/home/canary-replay/" + standin.SwitchFile},
	} {
		if c[0] != c[1] {
			t.Errorf("%s: %s, want %s", name, c[0], c[1])
		}
	}
	// Every path is absolute and clean, and the sources lie in the lead's
	// folder, outside the folders the lead writes.
	for _, s := range []string{p.LeadSkills, p.InstructionFile, p.GitIdentityDir, p.QargiInstall, p.AssignmentDir, p.ProjectionDir, p.ManagedSettings} {
		if !strings.HasPrefix(s, lead+"/") || filepath.Clean(s) != s {
			t.Errorf("source %s", s)
		}
		for _, w := range []string{p.Home, p.ConfigRoot, p.Tree, p.Records, p.Requests, p.Mail, p.Holder} {
			if s == w || strings.HasPrefix(s, w+"/") {
				t.Errorf("source %s lies in %s", s, w)
			}
		}
	}
}

func TestPathsOfRefusesARuntimeTheProfileDoesNotKnow(t *testing.T) {
	w := provisiontest.New(t)
	w.Cfg.Agent.Runtime = "sample-runtime"
	if _, err := PathsOf(w.Cfg); err == nil {
		t.Fatal("accepted")
	}
	if err := Provision(context.Background(), w.Cfg, Options{Getenv: w.Getenv()}); err == nil {
		t.Fatal("Provision accepted")
	}
	if exists(filepath.Join(w.State, "leads")) {
		t.Fatal("made something")
	}
}

func TestEveryFileOfThePackageCarriesTheBuildTag(t *testing.T) {
	const want = "//go:build linux && ataniq_canary"
	for _, dir := range []string{".", "provisiontest"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("%s: %v %v", dir, files, err)
		}
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			if first, _, _ := strings.Cut(string(data), "\n"); first != want {
				t.Errorf("%s starts with %q", f, first)
			}
		}
	}
}

func TestARuntimeVersionTheProfileRefusesIsRefusedBeforeAnythingIsMade(t *testing.T) {
	w := provisiontest.New(t)
	w.Cfg.Agent.RuntimeVersion = "not a version"
	before := snapshot(t, w.State)
	lines, err := provisionWorld(w)
	if err == nil || !strings.Contains(err.Error(), "profile") {
		t.Fatalf("Provision: %v", err)
	}
	if len(lines) != 0 || snapshot(t, w.State) != before || exists(w.Runtime) {
		t.Fatal("the refused run made something")
	}
}
