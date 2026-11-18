//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadsandbox/mountpoints"
	"ataniq/internal/leadsandbox/plan"
	"ataniq/internal/leadsandbox/profile"
)

// The plan writes the pass-through step and this command reads it. The two
// share no code, so these tests join them on real folders: the step of a plan
// built by plan.Build, from the walks of mountpoints, is executed by the test
// binary as the probe, with the test agent in the place of bubblewrap. No
// sandbox tool is started: what is shown is what the step hands to its
// command, not what bubblewrap does with it.

// planFixture is a set of profile inputs whose sources exist under
// t.TempDir(), and the plan built for them.
type planFixture struct {
	in   profile.Inputs
	plan *plan.Plan
	fifo string
}

func newPlanFixture(t *testing.T, rt profile.Runtime, seccomp bool) planFixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	owned := filepath.Join(base, "owned")
	in := profile.Inputs{
		StateRoot:             filepath.Join(base, "state"),
		Role:                  "arc-lead",
		Project:               "demo",
		Runtime:               rt,
		RuntimeVersion:        "2.1.0",
		Identity:              profile.IdentityRoot,
		AgentInstall:          filepath.Join(owned, "agent", string(rt), "2.1.0"),
		ProbeBinary:           filepath.Join(owned, "bin/probe"),
		SpecDir:               filepath.Join(owned, "spec/arc-lead"),
		ManagedSettings:       filepath.Join(owned, "managed/arc-lead/settings.json"),
		ManagedSettingsSHA256: strings.Repeat("c1", 32),
		LeadSkills:            filepath.Join(owned, "skills/arc-lead"),
		InstructionFile:       filepath.Join(owned, "instructions/arc-lead", rt.InstructionFileName()),
		GitIdentityDir:        filepath.Join(owned, "git/arc-lead"),
		GitMeta:               filepath.Join(base, "state/leads/arc-lead/gitmeta"),
		QargiInstall:          filepath.Join(owned, "qargi"),
		QargiRecordsInside:    "/home/lead/.qargi",
		AssignmentDir:         filepath.Join(owned, "assignment/demo"),
		ProjectionDir:         filepath.Join(owned, "projection/demo"),
		ResolvConf:            filepath.Join(owned, "resolv/arc-lead/resolv.conf"),
		ExtraPlaceholders:     []profile.Placeholder{{Rel: ".envrc", Source: filepath.Join(owned, "placeholders/envrc")}},
		NeverMounted:          []string{"/home/owner/work/demo", "/home/owner/.claude", "/home/owner/.claude.json", "/home/owner/.codex", "/home/owner/.ssh"},
		Term:                  "xterm-256color",
		ColorTerm:             "truecolor",
		Lang:                  "C.UTF-8",
		TZ:                    "UTC",
	}
	if rt == profile.Codex {
		in.ManagedSettingsInside = "/etc/codex/managed-config.toml"
	}
	if seccomp {
		in.SeccompFile = filepath.Join(owned, "seccomp/lead.bpf")
		in.SeccompSHA256 = strings.Repeat("5e", 32)
		if err := os.MkdirAll(filepath.Dir(in.SeccompFile), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(in.SeccompFile, []byte("filter"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Every source Ataniq supplies, made as its walk says it must be.
	walks, err := mountpoints.SourceWalks(in)
	if err != nil {
		t.Fatalf("SourceWalks: %v", err)
	}
	for _, w := range walks {
		if w.Dir {
			err = os.MkdirAll(w.Path(), 0o700)
		} else if err = os.MkdirAll(filepath.Dir(w.Path()), 0o700); err == nil {
			err = os.WriteFile(w.Path(), []byte(w.Inside), 0o600)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	// The report channel: a FIFO in a folder of the holder's own.
	run := filepath.Join(base, "run")
	if err := os.Mkdir(run, 0o700); err != nil {
		t.Fatal(err)
	}
	f := planFixture{in: in, fifo: filepath.Join(run, "report.fifo")}
	if err := unix.Mkfifo(f.fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	// The rows of the host are text only here: the step does not touch them.
	table, err := in.Binds()
	if err != nil {
		t.Fatal(err)
	}
	host := map[string]string{}
	for _, b := range table {
		if b.Kind == profile.KindRoBind && b.SourceRole == profile.RoleHost && !b.Optional {
			host[b.Inside] = b.Inside
		}
	}
	given, err := mountpoints.PlanWalks(in)
	if err != nil {
		t.Fatal(err)
	}
	opt := plan.Options{
		PastaPath: "/usr/bin/pasta", BwrapPath: "/usr/bin/bwrap",
		AgentArgv:   []string{"/ataniq/agent/bin/" + string(rt)},
		HostSources: host, SourceWalks: given,
		ReportPath: f.fifo, ReportDir: run, ReportFD: 4,
	}
	if seccomp {
		opt.SeccompFD = 3
	}
	p, err := plan.Build(in, opt)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	f.plan = p
	return f
}

// runStep executes the pass-through step of the plan with the test binary as
// the probe and the test agent as the command, the holder's end of the report
// channel open.
func (f planFixture) runStep(t *testing.T) *probeRun {
	t.Helper()
	rd, err := unix.Open(f.fifo, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Close(rd) })
	step := f.plan.PassFDs
	if step[0] != f.in.ProbeBinary || step[1] != modePassFDs || step[len(step)-1] != optEnd {
		t.Fatalf("the step is not the probe in its pass-through mode: %q", step)
	}
	return startPass(t, runOpts{mode: modeFake, leak: 3}, step[2:len(step)-1]...)
}

func TestTheStepOfAPlanHandsOnEveryBindSourceAsADescriptor(t *testing.T) {
	for _, rt := range profile.Runtimes() {
		for _, seccomp := range []bool{false, true} {
			t.Run(string(rt)+" seccomp="+strconv.FormatBool(seccomp), func(t *testing.T) {
				f := newPlanFixture(t, rt, seccomp)
				if len(f.plan.Walks) != 24 {
					t.Fatalf("%d walks, want the 23 rows Ataniq supplies and the placeholder", len(f.plan.Walks))
				}
				r := f.runStep(t)
				if code := r.wait(); code != 0 {
					t.Fatalf("exit %d: %s", code, r.stderr)
				}
				a := r.agent()
				for _, w := range f.plan.Walks {
					wantWalked(t, a, w.FD, w.Path(), w.Dir)
				}
				for _, file := range f.plan.Files {
					if got, ok := fdByNumber(a, file.FD); !ok || got.Link != file.Path {
						t.Errorf("the %s descriptor %d is %+v, want %s", file.Purpose, file.FD, got, file.Path)
					}
				}
				if want := len(f.plan.Walks) + len(f.plan.Files); len(a.FDs) != want {
					t.Errorf("the command has %d descriptors above 2, the plan lists %d", len(a.FDs), want)
				}
				// Every number bubblewrap is told to bind from is a descriptor
				// the command holds, and names the source of that row.
				source := map[string]string{}
				for _, m := range f.plan.Mounts {
					source[m.Inside] = m.Source
				}
				binds := 0
				for i, arg := range f.plan.Bwrap {
					if arg != "--ro-bind-fd" && arg != "--bind-fd" {
						continue
					}
					binds++
					fd, err := strconv.Atoi(f.plan.Bwrap[i+1])
					if err != nil {
						t.Fatalf("%s %s", arg, f.plan.Bwrap[i+1])
					}
					got, ok := fdByNumber(a, fd)
					if !ok {
						t.Errorf("%s %d %s: the command holds no such descriptor", arg, fd, f.plan.Bwrap[i+2])
						continue
					}
					if dev, ino := identity(t, source[f.plan.Bwrap[i+2]]); got.Dev != dev || got.Ino != ino {
						t.Errorf("%s %d %s: the descriptor names %s, the row's source is %s", arg, fd, f.plan.Bwrap[i+2], got.Link, source[f.plan.Bwrap[i+2]])
					}
				}
				if binds != len(f.plan.Walks) {
					t.Errorf("%d binds from a descriptor, %d walks", binds, len(f.plan.Walks))
				}
			})
		}
	}
}

// A source of the plan that is a link when the step runs ends the step: the
// command, which would be bubblewrap, is not started.
func TestTheStepOfAPlanRefusesAPlantedLink(t *testing.T) {
	f := newPlanFixture(t, profile.Claude, false)
	owner := filepath.Join(filepath.Dir(f.in.StateRoot), "owner-folder")
	if err := os.Mkdir(owner, 0o700); err != nil {
		t.Fatal(err)
	}
	// The git hooks folder of Ataniq's copy, swapped for a link: the row is
	// bound read-only over the tree.
	hooks := profile.GitMetaSource(f.in.GitMeta, ".git/hooks")
	if err := os.Rename(hooks, hooks+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(owner, hooks); err != nil {
		t.Fatal(err)
	}
	r := f.runStep(t)
	if code := r.wait(); code != exitWalk {
		t.Fatalf("exit %d, want %d (stderr %s)", code, exitWalk, r.stderr)
	}
	oneLine(t, r.stderr.String())
	for _, want := range []string{"--walk-dir ", "symbolic link", strconv.Quote(f.in.StateRoot), "gitmeta/hooks"} {
		if !strings.Contains(r.stderr.String(), want) {
			t.Errorf("stderr %q does not contain %q", r.stderr, want)
		}
	}
	r.noAgent()
}
