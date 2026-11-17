package plan

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"ataniq/internal/leadsandbox/profile"
)

// arity of the options the mutation helpers walk over.
func width(opt string) int {
	switch opt {
	case "--uid", "--gid", "--cap-drop", "--seccomp", "--chdir", "--remount-ro", "--tmpfs", "--dev", "--proc":
		return 2
	case "--setenv", "--ro-bind", "--bind", "--symlink":
		return 3
	}
	return 1
}

// find returns the index in p.Bwrap of the option opt whose first operand is
// key (any when key is empty), or fails the test.
func find(t *testing.T, p *Plan, opt, key string) int {
	t.Helper()
	for i := 1; i < len(p.Bwrap)-1; i += width(p.Bwrap[i]) {
		if p.Bwrap[i] == opt && (key == "" || p.Bwrap[i+1] == key) {
			return i
		}
	}
	t.Fatalf("no %s %s in the plan", opt, key)
	return -1
}

// cut removes the option at i from Bwrap.
func cut(p *Plan, i int) {
	n := width(p.Bwrap[i])
	p.Bwrap = append(p.Bwrap[:i], p.Bwrap[i+n:]...)
}

// insert adds arguments to Bwrap at index i.
func insert(p *Plan, i int, args ...string) {
	tail := append([]string(nil), p.Bwrap[i:]...)
	p.Bwrap = append(append(p.Bwrap[:i], args...), tail...)
}

// beforeEnd adds arguments just before the final "--" of Bwrap.
func beforeEnd(p *Plan, args ...string) { insert(p, len(p.Bwrap)-1, args...) }

type mutation struct {
	name string
	want ProblemKind
	// seccomp makes the base plan one that names a seccomp file.
	seccomp bool
	do      func(t *testing.T, p *Plan)
}

func TestVerifyMutations(t *testing.T) {
	muts := []mutation{
		// The parts.
		{"argv differs from the parts", ProbShape, false, func(t *testing.T, p *Plan) { p.Argv[0] = "/bin/other" }},
		{"argv shorter than the parts", ProbShape, false, func(t *testing.T, p *Plan) { p.Argv = p.Argv[:len(p.Argv)-1] }},
		{"empty inner", ProbShape, false, func(t *testing.T, p *Plan) { p.Inner = nil }},
		{"relative pasta", ProbExecutable, false, func(t *testing.T, p *Plan) { p.Pasta[0] = "pasta" }},
		{"relative bwrap", ProbExecutable, false, func(t *testing.T, p *Plan) { p.Bwrap[0] = "bwrap" }},
		{"pasta argument dropped", ProbPasta, false, func(t *testing.T, p *Plan) { p.Pasta = append(p.Pasta[:6], p.Pasta[8:]...) }},
		{"pasta forwarding allowed", ProbPasta, false, func(t *testing.T, p *Plan) { p.Pasta[5] = "22" }},
		{"pasta argument added", ProbPasta, false, func(t *testing.T, p *Plan) { p.Pasta = append(p.Pasta[:len(p.Pasta)-1], "-t", "8080", "--") }},
		{"pasta terminator dropped", ProbPasta, false, func(t *testing.T, p *Plan) { p.Pasta = p.Pasta[:len(p.Pasta)-1] }},

		// Forbidden and unknown options.
		{"disable-userns", ProbForbidden, false, func(t *testing.T, p *Plan) { beforeEnd(p, "--disable-userns") }},
		{"assert-userns-disabled", ProbForbidden, false, func(t *testing.T, p *Plan) { beforeEnd(p, "--assert-userns-disabled") }},
		{"new-session", ProbForbidden, false, func(t *testing.T, p *Plan) { insert(p, 1, "--new-session") }},
		{"new-session in the agent argv", ProbForbidden, false, func(t *testing.T, p *Plan) { p.Inner = append(p.Inner, "--new-session") }},
		{"unknown option", ProbUnknown, false, func(t *testing.T, p *Plan) { beforeEnd(p, "--unshare-pid") }},
		{"ro-bind-try", ProbUnknown, false, func(t *testing.T, p *Plan) { i := find(t, p, "--ro-bind", "/usr"); p.Bwrap[i] = "--ro-bind-try" }},
		{"missing operand", ProbMalformed, false, func(t *testing.T, p *Plan) { p.Bwrap = append(p.Bwrap[:len(p.Bwrap)-3], "--tmpfs", "--") }},
		{"terminator dropped", ProbMalformed, false, func(t *testing.T, p *Plan) { p.Bwrap = p.Bwrap[:len(p.Bwrap)-1] }},
		{"terminator early", ProbMalformed, false, func(t *testing.T, p *Plan) { insert(p, 1, "--") }},
		{"operand read as an option", ProbOptionLike, false, func(t *testing.T, p *Plan) { i := find(t, p, "--setenv", "TERM"); p.Bwrap[i+2] = "--x" }},

		// The flag set.
		{"unshare-all dropped", ProbFlags, false, func(t *testing.T, p *Plan) { cut(p, find(t, p, "--unshare-all", "")) }},
		{"share-net dropped", ProbFlags, false, func(t *testing.T, p *Plan) { cut(p, find(t, p, "--share-net", "")) }},
		{"cap-drop dropped", ProbFlags, false, func(t *testing.T, p *Plan) { cut(p, find(t, p, "--cap-drop", "")) }},
		{"clearenv dropped", ProbFlags, false, func(t *testing.T, p *Plan) { cut(p, find(t, p, "--clearenv", "")) }},
		{"die-with-parent dropped", ProbFlags, false, func(t *testing.T, p *Plan) { cut(p, find(t, p, "--die-with-parent", "")) }},
		{"uid changed", ProbFlags, false, func(t *testing.T, p *Plan) { p.Bwrap[find(t, p, "--uid", "")+1] = "1000" }},
		{"cap-drop value changed", ProbFlags, false, func(t *testing.T, p *Plan) { p.Bwrap[find(t, p, "--cap-drop", "")+1] = "NONE" }},
		{"flag set reordered", ProbFlags, false, func(t *testing.T, p *Plan) { p.Bwrap[1], p.Bwrap[2] = p.Bwrap[2], p.Bwrap[1] }},

		// The environment.
		{"variable removed", ProbEnv, false, func(t *testing.T, p *Plan) { cut(p, find(t, p, "--setenv", "TERM")) }},
		{"value changed", ProbEnv, false, func(t *testing.T, p *Plan) { p.Bwrap[find(t, p, "--setenv", "HOME")+2] = "/root" }},
		{"variable added", ProbEnv, false, func(t *testing.T, p *Plan) {
			insert(p, find(t, p, "--setenv", "XDG_STATE_HOME")+3, "--setenv", "SSH_AUTH_SOCK", "/run/agent")
		}},
		{"variable set twice", ProbEnv, false, func(t *testing.T, p *Plan) {
			i := find(t, p, "--setenv", "HOME")
			insert(p, i, "--setenv", "HOME", "/home/lead")
		}},
		{"variables out of order", ProbEnv, false, func(t *testing.T, p *Plan) {
			i, j := find(t, p, "--setenv", "HOME"), find(t, p, "--setenv", "LANG")
			for k := 0; k < 3; k++ {
				p.Bwrap[i+k], p.Bwrap[j+k] = p.Bwrap[j+k], p.Bwrap[i+k]
			}
		}},
		{"Env of the plan changed", ProbEnv, false, func(t *testing.T, p *Plan) { p.Env[0].Value = "/elsewhere" }},
		{"Env of the plan has a secret", ProbEnv, false, func(t *testing.T, p *Plan) { p.Env = append(p.Env, profile.EnvVar{Name: "X_TOKEN", Value: "s"}) }},
		{"environment after the mounts", ProbOrder, false, func(t *testing.T, p *Plan) {
			i := find(t, p, "--setenv", "HOME")
			v := append([]string(nil), p.Bwrap[i:i+3]...)
			cut(p, i)
			beforeEnd(p, v...)
			// --chdir and --remount-ro still follow the mounts, so the
			// moved option also breaks the tail order.
		}},

		// The mounts.
		{"required row removed", ProbMountMissing, false, func(t *testing.T, p *Plan) { cut(p, find(t, p, "--ro-bind", "/usr")) }},
		{"probe row removed", ProbMountMissing, false, func(t *testing.T, p *Plan) { cut(p, find(t, p, "--ro-bind", "/opt/ataniq/bin/probe")) }},
		{"managed settings row removed", ProbMountMissing, false, func(t *testing.T, p *Plan) {
			cut(p, find(t, p, "--ro-bind", "/opt/ataniq/managed/arc-lead/settings.json"))
		}},
		{"tmpfs row removed", ProbMountMissing, false, func(t *testing.T, p *Plan) { cut(p, find(t, p, "--tmpfs", "/")) }},
		{"read-only bind made writable", ProbMountDiffers, false, func(t *testing.T, p *Plan) {
			p.Bwrap[find(t, p, "--ro-bind", "/usr")] = "--bind"
		}},
		{"git config made writable", ProbMountDiffers, false, func(t *testing.T, p *Plan) {
			p.Bwrap[find(t, p, "--ro-bind", "/state/leads/arc-lead/tree/.git/config")] = "--bind"
		}},
		{"source changed", ProbMountDiffers, false, func(t *testing.T, p *Plan) {
			p.Bwrap[find(t, p, "--bind", "/state/leads/arc-lead/tree")+1] = "/state/leads/other/tree"
		}},
		{"link target changed", ProbMountDiffers, false, func(t *testing.T, p *Plan) {
			p.Bwrap[find(t, p, "--symlink", "usr/bin")+1] = "usr/sbin"
		}},
		{"tmpfs made a bind", ProbMountDiffers, false, func(t *testing.T, p *Plan) {
			i := find(t, p, "--tmpfs", "/tmp")
			p.Bwrap[i] = "--ro-bind"
			insert(p, i+1, "/tmp")
		}},
		{"unexpected mount", ProbMountExtra, false, func(t *testing.T, p *Plan) { insert(p, find(t, p, "--chdir", ""), "--ro-bind", "/usr/share", "/share") }},
		{"mount twice", ProbMountTwice, false, func(t *testing.T, p *Plan) {
			insert(p, find(t, p, "--chdir", ""), "--ro-bind", "/usr", "/usr")
		}},
		{"mounts out of order", ProbOrder, false, func(t *testing.T, p *Plan) {
			i, j := find(t, p, "--ro-bind", "/etc/hosts"), find(t, p, "--ro-bind", "/etc/passwd")
			for k := 0; k < 3; k++ {
				p.Bwrap[i+k], p.Bwrap[j+k] = p.Bwrap[j+k], p.Bwrap[i+k]
			}
		}},
		{"owner's ssh folder mounted", ProbNever, false, func(t *testing.T, p *Plan) {
			insert(p, find(t, p, "--chdir", ""), "--ro-bind", "/home/owner/.ssh", "/home/lead/.ssh")
		}},
		{"owner's checkout mounted", ProbNever, false, func(t *testing.T, p *Plan) {
			p.Bwrap[find(t, p, "--bind", "/state/leads/arc-lead/tree")+1] = "/home/owner/work/demo"
		}},
		{"folder below a never-mounted path", ProbNever, false, func(t *testing.T, p *Plan) {
			insert(p, find(t, p, "--chdir", ""), "--ro-bind", "/home/owner/.claude/projects", "/x")
		}},
		{"folder above a never-mounted path", ProbNever, false, func(t *testing.T, p *Plan) {
			p.Bwrap[find(t, p, "--ro-bind", "/opt/ataniq/qargi")+1] = "/home/owner"
		}},
		{"other runtime's config mounted", ProbOtherRuntime, false, func(t *testing.T, p *Plan) {
			insert(p, find(t, p, "--chdir", ""), "--bind", "/state/leads/arc-lead/config/codex", "/home/lead/.codex")
		}},
		{"other runtime's login in the home", ProbOtherRuntime, false, func(t *testing.T, p *Plan) {
			insert(p, find(t, p, "--chdir", ""), "--ro-bind", "/opt/ataniq/qargi", "/home/lead/.codex/auth.json")
		}},
		{"other runtime's folder mounted elsewhere", ProbOtherRuntime, false, func(t *testing.T, p *Plan) {
			insert(p, find(t, p, "--chdir", ""), "--ro-bind", "/state/leads/arc-lead/config/codex/auth.json", "/x/auth.json")
		}},
		{"state of the other lead", ProbOtherRuntime, false, func(t *testing.T, p *Plan) {
			// The whole config root holds both runtimes' folders.
			p.Bwrap[find(t, p, "--bind", "/state/leads/arc-lead/config/claude")+1] = "/state/leads/arc-lead/config"
		}},

		// The tail.
		{"chdir removed", ProbWorkDir, false, func(t *testing.T, p *Plan) { cut(p, find(t, p, "--chdir", "")) }},
		{"chdir outside the tree and home", ProbWorkDir, false, func(t *testing.T, p *Plan) { p.Bwrap[find(t, p, "--chdir", "")+1] = "/etc" }},
		{"chdir not the plan's", ProbWorkDir, false, func(t *testing.T, p *Plan) { p.Bwrap[find(t, p, "--chdir", "")+1] = "/work/demo/sub" }},
		{"chdir relative", ProbWorkDir, false, func(t *testing.T, p *Plan) { p.Bwrap[find(t, p, "--chdir", "")+1] = "work/demo" }},
		{"chdir twice", ProbWorkDir, false, func(t *testing.T, p *Plan) { insert(p, find(t, p, "--chdir", ""), "--chdir", "/work/demo") }},
		{"remount removed", ProbRemount, false, func(t *testing.T, p *Plan) { cut(p, find(t, p, "--remount-ro", "")) }},
		{"remount of another path", ProbRemount, false, func(t *testing.T, p *Plan) { p.Bwrap[find(t, p, "--remount-ro", "")+1] = "/usr" }},
		{"remount twice", ProbRemount, false, func(t *testing.T, p *Plan) { beforeEnd(p, "--remount-ro", "/") }},
		{"chdir after the remount", ProbOrder, false, func(t *testing.T, p *Plan) {
			i := find(t, p, "--chdir", "")
			v := append([]string(nil), p.Bwrap[i:i+2]...)
			cut(p, i)
			beforeEnd(p, v...)
		}},

		// The seccomp hook.
		{"seccomp passed without a file named", ProbSeccomp, false, func(t *testing.T, p *Plan) {
			insert(p, find(t, p, "--tmpfs", "/"), "--seccomp", "7")
		}},
		{"descriptor listed without a file named", ProbSeccomp, false, func(t *testing.T, p *Plan) {
			p.Files = append([]File{{FD: 7, Path: "/opt/ataniq/seccomp/lead.bpf", Mode: FileRead, Purpose: PurposeSeccomp}}, p.Files...)
		}},
		{"seccomp dropped", ProbSeccomp, true, func(t *testing.T, p *Plan) { cut(p, find(t, p, "--seccomp", "")) }},
		{"seccomp twice", ProbSeccomp, true, func(t *testing.T, p *Plan) { insert(p, find(t, p, "--seccomp", ""), "--seccomp", "8") }},
		{"seccomp descriptor 1", ProbSeccomp, true, func(t *testing.T, p *Plan) { p.Bwrap[find(t, p, "--seccomp", "")+1] = "1" }},
		{"seccomp descriptor not a number", ProbSeccomp, true, func(t *testing.T, p *Plan) { p.Bwrap[find(t, p, "--seccomp", "")+1] = "07" }},
		{"seccomp descriptor differs from Files", ProbSeccomp, true, func(t *testing.T, p *Plan) { p.Bwrap[find(t, p, "--seccomp", "")+1] = "9" }},
		{"Files dropped", ProbSeccomp, true, func(t *testing.T, p *Plan) { p.Files = nil }},
		{"Files path changed", ProbSeccomp, true, func(t *testing.T, p *Plan) { p.Files[0].Path = "/opt/ataniq/seccomp/other.bpf" }},
		{"Files digest changed", ProbSeccomp, true, func(t *testing.T, p *Plan) { p.Files[0].SHA256 = strings.Repeat("0", 64) }},
		{"seccomp after the mounts", ProbOrder, true, func(t *testing.T, p *Plan) {
			i := find(t, p, "--seccomp", "")
			v := append([]string(nil), p.Bwrap[i:i+2]...)
			cut(p, i)
			insert(p, find(t, p, "--chdir", ""), v...)
		}},

		// The probe command line.
		{"probe path changed", ProbInner, false, func(t *testing.T, p *Plan) { p.Inner[0] = "/usr/bin/sh" }},
		{"spec path changed", ProbInner, false, func(t *testing.T, p *Plan) { p.Inner[2] = "/tmp/spec.json" }},
		{"probe separator dropped", ProbInner, false, func(t *testing.T, p *Plan) { p.Inner = append(p.Inner[:5], p.Inner[6:]...) }},
		{"relative agent", ProbAgent, false, func(t *testing.T, p *Plan) { p.Inner[6] = "claude" }},
		{"agent without an executable", ProbAgent, false, func(t *testing.T, p *Plan) { p.Inner = p.Inner[:6] }},
		{"NUL in an agent argument", ProbAgent, false, func(t *testing.T, p *Plan) { p.Inner[7] = "a\x00b" }},
		{"report option renamed", ProbReport, false, func(t *testing.T, p *Plan) { p.Inner[3] = "--report" }},

		// The pass-through step and the report channel.
		{"pass-through step dropped", ProbShape, false, func(t *testing.T, p *Plan) { p.PassFDs = nil }},
		{"pass-through runs another program", ProbPassFDs, false, func(t *testing.T, p *Plan) { p.PassFDs[0] = "/usr/bin/env" }},
		{"pass-through in another mode", ProbPassFDs, false, func(t *testing.T, p *Plan) { p.PassFDs[1] = "run" }},
		{"pass-through terminator dropped", ProbPassFDs, false, func(t *testing.T, p *Plan) { p.PassFDs = p.PassFDs[:len(p.PassFDs)-1] }},
		{"pass-through option unknown", ProbPassFDs, false, func(t *testing.T, p *Plan) { p.PassFDs[2] = "--append" }},
		{"pass-through operand not FD=PATH", ProbPassFDs, false, func(t *testing.T, p *Plan) { p.PassFDs[3] = "report.fifo" }},
		{"pass-through operand with a signed number", ProbPassFDs, false, func(t *testing.T, p *Plan) { p.PassFDs[3] = "+3=/opt/ataniq/run/arc-lead/report.fifo" }},
		{"pass-through option without operand", ProbPassFDs, false, func(t *testing.T, p *Plan) { p.PassFDs = append(p.PassFDs[:3], "--") }},
		{"pass-through opens another path", ProbPassFDs, false, func(t *testing.T, p *Plan) { p.PassFDs[3] = "3=/etc/shadow" }},
		{"pass-through opens another descriptor", ProbPassFDs, false, func(t *testing.T, p *Plan) { p.PassFDs[3] = "5=/opt/ataniq/run/arc-lead/report.fifo" }},
		{"pass-through opens the report read-only", ProbPassFDs, false, func(t *testing.T, p *Plan) { p.PassFDs[2] = "--read" }},
		{"pass-through opens more than Files lists", ProbPassFDs, false, func(t *testing.T, p *Plan) {
			p.PassFDs = append(p.PassFDs[:len(p.PassFDs)-1], "--write", "9=/etc/passwd", "--")
		}},
		{"pass-through reads after a write", ProbPassFDs, true, func(t *testing.T, p *Plan) {
			p.PassFDs = []string{p.PassFDs[0], p.PassFDs[1], p.PassFDs[4], p.PassFDs[5], p.PassFDs[2], p.PassFDs[3], "--"}
			p.Files[0], p.Files[1] = p.Files[1], p.Files[0]
		}},
		{"descriptor number used twice", ProbPassFDs, true, func(t *testing.T, p *Plan) {
			p.Files[1].FD = 7
			p.PassFDs[5] = "7=" + p.Files[1].Path
			p.Inner[4] = "7"
		}},
		{"descriptor 2 for the report", ProbPassFDs, false, func(t *testing.T, p *Plan) {
			p.Files[0].FD = 2
			p.PassFDs[3] = "2=" + p.Files[0].Path
			p.Inner[4] = "2"
		}},
		{"report descriptor differs in the probe", ProbReport, false, func(t *testing.T, p *Plan) { p.Inner[4] = "4" }},
		{"report descriptor not a number", ProbReport, false, func(t *testing.T, p *Plan) { p.Inner[4] = "x" }},
		{"no report channel listed", ProbReport, false, func(t *testing.T, p *Plan) {
			p.Files = nil
			p.PassFDs = p.PassFDs[:2]
			p.PassFDs = append(p.PassFDs, "--")
		}},
		{"PWD note dropped", ProbNotes, false, func(t *testing.T, p *Plan) {
			var n []string
			for _, x := range p.Notes {
				if !strings.Contains(x, "PWD") {
					n = append(n, x)
				}
			}
			p.Notes = n
		}},
		{"PWD note for another folder", ProbNotes, false, func(t *testing.T, p *Plan) {
			for i, x := range p.Notes {
				if x == NotePWD(p.WorkDir) {
					p.Notes[i] = NotePWD("/work/demo/other")
				}
			}
		}},
		{"report note dropped", ProbNotes, false, func(t *testing.T, p *Plan) {
			var n []string
			for _, x := range p.Notes {
				if !strings.HasPrefix(x, "report channel") {
					n = append(n, x)
				}
			}
			p.Notes = n
		}},

		// The rest.
		{"digest changed", ProbDigest, false, func(t *testing.T, p *Plan) { p.Digest = strings.Repeat("0", 64) }},
		{"digest empty", ProbDigest, false, func(t *testing.T, p *Plan) { p.Digest = "" }},
		{"controlling terminal set", ProbTerminal, false, func(t *testing.T, p *Plan) { p.SetControllingTerminal = true }},
		{"r17 note dropped", ProbNotes, false, func(t *testing.T, p *Plan) { p.Notes = p.Notes[:1] }},
		{"seccomp note dropped", ProbNotes, false, func(t *testing.T, p *Plan) {
			var n []string
			for _, x := range p.Notes {
				if x != NoteNoSeccomp {
					n = append(n, x)
				}
			}
			p.Notes = n
		}},
		{"named seccomp not noted", ProbNotes, true, func(t *testing.T, p *Plan) {
			var n []string
			for _, x := range p.Notes {
				if !strings.HasPrefix(x, "seccomp file named") {
					n = append(n, x)
				}
			}
			p.Notes = append(n, NoteNoSeccomp)
		}},
		{"command line over the cap", ProbSize, false, func(t *testing.T, p *Plan) {
			p.Inner = append(p.Inner, strings.Repeat("a", MaxArgvBytes))
		}},
	}

	plain, plainIn, plainOpt := basePlan(t)
	secIn := withSeccomp(fixture(profile.Claude))
	secOpt := options(secIn, allPresent(t, secIn))
	sec := mustBuild(t, secIn, secOpt)
	for name, base := range map[string]struct {
		p   *Plan
		in  profile.Inputs
		opt Options
	}{"plain": {plain, plainIn, plainOpt}, "seccomp": {sec, secIn, secOpt}} {
		if probs := Verify(base.p, base.in, base.opt); len(probs) > 0 {
			t.Fatalf("the %s base plan has problems: %v", name, probs)
		}
	}

	seen := map[ProblemKind]bool{}
	for _, m := range muts {
		t.Run(m.name, func(t *testing.T) {
			base, in, opt := plain, plainIn, plainOpt
			if m.seccomp {
				base, in, opt = sec, secIn, secOpt
			}
			p := clonePlan(base)
			m.do(t, p)
			// A mutation of a part keeps Argv in step, unless it is the
			// Argv itself that the case changes.
			if m.want != ProbShape {
				resync(p)
			}
			probs := Verify(p, in, opt)
			if !hasKind(probs, m.want) {
				t.Fatalf("want a %q problem, got %v", m.want, probs)
			}
			seen[m.want] = true
		})
	}
	// Every kind of problem has a mutation that raises it, except the one
	// that needs invalid inputs (TestVerifyReportsInvalidInputs).
	for _, k := range []ProblemKind{ProbShape, ProbExecutable, ProbPasta, ProbForbidden, ProbUnknown, ProbMalformed,
		ProbOptionLike, ProbOrder, ProbFlags, ProbEnv, ProbSeccomp, ProbMountMissing, ProbMountExtra, ProbMountDiffers,
		ProbMountTwice, ProbNever, ProbOtherRuntime, ProbWorkDir, ProbRemount, ProbInner, ProbDigest, ProbNotes,
		ProbTerminal, ProbSize, ProbPassFDs, ProbReport, ProbAgent} {
		if !seen[k] {
			t.Errorf("no mutation raises %q", k)
		}
	}
}

func TestVerifyAcceptsAnOptionalRowLeftOut(t *testing.T) {
	in := fixture(profile.Claude)
	o := options(in, nil)
	p := mustBuild(t, in, o)
	if probs := Verify(p, in, o); len(probs) > 0 {
		t.Errorf("a plan without its optional rows: %v", probs)
	}
}

// The optional rows are exactly the ones the options say the host has: a row
// the plan mounts against the options, or leaves out against them, is a
// problem, and so is a note that does not match.
func TestVerifyChecksWhichOptionalRowsArePresent(t *testing.T) {
	in := fixture(profile.Claude)
	present := allPresent(t, in)
	full, none := options(in, present), options(in, nil)
	pFull, pNone := mustBuild(t, in, full), mustBuild(t, in, none)

	if probs := Verify(pNone, in, full); !hasKind(probs, ProbMountMissing) || !hasKind(probs, ProbNotes) {
		t.Errorf("a plan without the optional rows against options that say the host has them: %v", probs)
	}
	if probs := Verify(pFull, in, none); !hasKind(probs, ProbMountExtra) || !hasKind(probs, ProbNotes) {
		t.Errorf("a plan with the optional rows against options that say the host lacks them: %v", probs)
	}
	// Every optional row, one at a time, in both directions.
	for _, row := range []string{"/lib64", "/sbin", "/etc/ssl", "/etc/ca-certificates", "/etc/nsswitch.conf", "/etc/localtime"} {
		if !present[row] {
			t.Fatalf("%s is not an optional row of the fixture", row)
		}
		o := options(in, without(present, row))
		if probs := Verify(pFull, in, o); !hasKind(probs, ProbMountExtra) {
			t.Errorf("%s mounted against the options: %v", row, probs)
		}
		q := mustBuild(t, in, o)
		if probs := Verify(q, in, full); !hasKind(probs, ProbMountMissing) {
			t.Errorf("%s left out against the options: %v", row, probs)
		}
		// The plan that agrees with its options has no problem.
		if probs := Verify(q, in, o); len(probs) > 0 {
			t.Errorf("%s left out, options agree: %v", row, probs)
		}
	}
}

func TestVerifyRefusesARequiredRowLeftOut(t *testing.T) {
	// The optional flag belongs to the row, not to the plan: a plan from a
	// caller that dropped /etc/passwd is not valid.
	in := fixture(profile.Claude)
	o := options(in, nil)
	p := mustBuild(t, in, o)
	cut(p, find(t, p, "--ro-bind", "/etc/passwd"))
	resync(p)
	probs := Verify(p, in, o)
	if !hasKind(probs, ProbMountMissing) || !strings.Contains(probs[0].Detail, "/etc/passwd") {
		t.Errorf("problems = %v", probs)
	}
}

func TestVerifyAgainstOtherInputs(t *testing.T) {
	p, in, o := basePlan(t)
	// A plan checked against inputs of another runtime, mode or project.
	other := fixture(profile.Codex)
	if probs := Verify(p, other, o); len(probs) == 0 {
		t.Error("a claude plan verifies against codex inputs")
	}
	owner := ownerMode(in)
	if probs := Verify(p, owner, o); !hasKind(probs, ProbFlags) {
		t.Errorf("a root plan verifies against owner inputs: %v", probs)
	}
	sec := withSeccomp(in)
	so := o
	so.SeccompFD = 7
	if probs := Verify(p, sec, so); !hasKind(probs, ProbSeccomp) {
		t.Errorf("a plan without seccomp verifies against inputs that name one: %v", probs)
	}
	moved := in
	moved.Project = "other"
	if probs := Verify(p, moved, o); len(probs) == 0 {
		t.Error("a plan verifies against another project")
	}
}

// Everything the holder knows independently is checked: a plan whose parts
// are consistent with each other but differ from the options is refused.
func TestVerifyAgainstOtherOptions(t *testing.T) {
	_, in, o := basePlan(t)
	secIn := withSeccomp(fixture(profile.Claude))
	so := options(secIn, allPresent(t, secIn))
	cases := []struct {
		name string
		want ProblemKind
		sec  bool
		mut  func(o *Options)
	}{
		{"another pasta", ProbExecutable, false, func(o *Options) { o.PastaPath = "/usr/bin/env" }},
		{"another bubblewrap", ProbExecutable, false, func(o *Options) { o.BwrapPath = "/bin/sh" }},
		{"another agent", ProbAgent, false, func(o *Options) { o.AgentArgv = []string{"/bin/sh", "--model", "example"} }},
		{"agent argument added", ProbAgent, false, func(o *Options) { o.AgentArgv = append([]string{}, append(o.AgentArgv, "--dangerous")...) }},
		{"agent argument dropped", ProbAgent, false, func(o *Options) { o.AgentArgv = o.AgentArgv[:2] }},
		{"another working folder", ProbWorkDir, false, func(o *Options) { o.WorkDir = "/work/demo/sub" }},
		{"another report path", ProbReport, false, func(o *Options) { o.ReportPath = "/opt/ataniq/run/arc-lead/other.fifo" }},
		{"another report descriptor", ProbReport, false, func(o *Options) { o.ReportFD = 9 }},
		{"another seccomp descriptor", ProbSeccomp, true, func(o *Options) { o.SeccompFD = 9 }},
		{"refused options", ProbOptions, false, func(o *Options) { o.ReportFD = 0 }},
		{"report path in a writable folder", ProbOptions, false, func(o *Options) { o.ReportPath = "/state/leads/arc-lead/home/report" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			base, bin := o, in
			if c.sec {
				base, bin = so, secIn
			}
			p := mustBuild(t, bin, base)
			other := base
			other.AgentArgv = append([]string(nil), base.AgentArgv...)
			c.mut(&other)
			probs := Verify(p, bin, other)
			if !hasKind(probs, c.want) {
				t.Errorf("want a %q problem, got %v", c.want, probs)
			}
		})
	}
}

// A plan from tampered parts that agree with each other, which the earlier
// shape-only check let through: each one is refused now.
func TestVerifyTamperedPartsThatAgree(t *testing.T) {
	p, in, o := basePlan(t)
	for name, mut := range map[string]func(p *Plan){
		"bubblewrap replaced":  func(p *Plan) { p.Bwrap[0] = "/bin/sh" },
		"pasta replaced":       func(p *Plan) { p.Pasta[0] = "/usr/bin/env" },
		"agent replaced":       func(p *Plan) { p.Inner[6] = "/bin/sh" },
		"agent option added":   func(p *Plan) { p.Inner = append(p.Inner, "--dangerously-skip-permissions") },
		"agent option dropped": func(p *Plan) { p.Inner = p.Inner[:len(p.Inner)-1] },
		"note added":           func(p *Plan) { p.Notes = append(p.Notes, "all is well") },
		"note duplicated":      func(p *Plan) { p.Notes = append(p.Notes, p.Notes[0]) },
		"optional row dropped": func(p *Plan) { cut(p, find(t, p, "--ro-bind", "/etc/ssl")) },
		"working folder": func(p *Plan) {
			p.WorkDir = "/work/demo/sub"
			p.Bwrap[find(t, p, "--chdir", "")+1] = "/work/demo/sub"
		},
	} {
		t.Run(name, func(t *testing.T) {
			q := clonePlan(p)
			mut(q)
			resync(q)
			if probs := Verify(q, in, o); len(probs) == 0 {
				t.Error("Verify found no problem")
			}
		})
	}
	// The pass-through step and the seccomp descriptor, changed together.
	secIn := withSeccomp(fixture(profile.Claude))
	so := options(secIn, allPresent(t, secIn))
	sp := mustBuild(t, secIn, so)
	for _, fd := range []string{"9", "1023"} {
		q := clonePlan(sp)
		q.Bwrap[find(t, q, "--seccomp", "")+1] = fd
		q.PassFDs[3] = fd + "=" + q.Files[0].Path
		q.Files[0].FD, _ = strconv.Atoi(fd)
		resync(q)
		if probs := Verify(q, secIn, so); !hasKind(probs, ProbSeccomp) {
			t.Errorf("seccomp descriptor %s changed everywhere: %v", fd, probs)
		}
	}
}

// sample returns the indexes of a list to try: all of them, except that of the
// bubblewrap arguments only the first occurrence of each option and operand
// slot is kept (the same code reads the others).
func sample(name string, list []string) []int {
	if name != "bwrap" {
		idx := make([]int, len(list))
		for i := range idx {
			idx[i] = i
		}
		return idx
	}
	var idx []int
	seen := map[string]bool{}
	for i := 1; i < len(list); {
		w := width(list[i])
		for j := 0; j < w && i+j < len(list); j++ {
			if key := fmt.Sprintf("%s/%d", list[i], j); !seen[key] {
				seen[key] = true
				idx = append(idx, i+j)
			}
		}
		i += w
	}
	return append(idx, 0, len(list)-1)
}

// canary is text that no valid plan holds and no problem may repeat.
const canary = "hunter2-canary"

// A problem never repeats text from the plan's own arguments: each token of
// every list of the plan is replaced by the canary, and the canary is also
// inserted before each token, so that the parse sees an unknown option, an
// operand out of place, a name, a value, a path, a link target and an agent
// argument that is not the options'.
func TestVerifyProblemsDoNotCarryPlanText(t *testing.T) {
	for _, withSeccompFile := range []bool{false, true} {
		in := fixture(profile.Claude)
		if withSeccompFile {
			in = withSeccomp(in)
		}
		o := options(in, allPresent(t, in))
		base := mustBuild(t, in, o)
		parts := map[string]func(p *Plan) *[]string{
			"pasta":   func(p *Plan) *[]string { return &p.Pasta },
			"passfds": func(p *Plan) *[]string { return &p.PassFDs },
			"bwrap":   func(p *Plan) *[]string { return &p.Bwrap },
			"inner":   func(p *Plan) *[]string { return &p.Inner },
			"notes":   func(p *Plan) *[]string { return &p.Notes },
		}
		check := func(what string, p *Plan) {
			t.Helper()
			resync(p)
			for _, pr := range Verify(p, in, o) {
				if strings.Contains(pr.String(), "hunter2") || strings.Contains(pr.String(), canary) {
					t.Errorf("%s: a problem repeats text of the plan: %s", what, pr)
				}
			}
		}
		for name, part := range parts {
			if withSeccompFile && (name == "pasta" || name == "bwrap") {
				continue // the same tokens as the plain plan, but for --seccomp
			}
			for _, i := range sample(name, *part(base)) {
				p := clonePlan(base)
				(*part(p))[i] = canary
				check(fmt.Sprintf("%s[%d] replaced", name, i), p)

				p = clonePlan(base)
				l := part(p)
				*l = append((*l)[:i], append([]string{canary}, (*l)[i:]...)...)
				check(fmt.Sprintf("%s[%d] preceded", name, i), p)
			}
		}
		// The environment and the descriptors of the plan.
		for i := range base.Env {
			p := clonePlan(base)
			p.Env[i].Name, p.Env[i].Value = canary, canary
			check(fmt.Sprintf("Env[%d]", i), p)
		}
		for i := range base.Files {
			p := clonePlan(base)
			p.Files[i].Path, p.Files[i].SHA256, p.Files[i].Purpose = canary, canary, canary
			check(fmt.Sprintf("Files[%d]", i), p)
		}
		p := clonePlan(base)
		p.Digest, p.WorkDir = canary, canary
		check("digest and working folder", p)
	}
}

func TestVerifyDoesNotPanicOnHostilePlans(t *testing.T) {
	p, in, o := basePlan(t)
	for n := 0; n <= len(p.Bwrap); n++ {
		q := clonePlan(p)
		q.Bwrap = q.Bwrap[:n]
		resync(q)
		Verify(q, in, o) // a truncated list is a set of problems, never a panic
	}
	for n := 0; n <= len(p.Inner); n++ {
		q := clonePlan(p)
		q.Inner = q.Inner[:n]
		resync(q)
		Verify(q, in, o)
	}
	for n := 0; n <= len(p.PassFDs); n++ {
		q := clonePlan(p)
		q.PassFDs = q.PassFDs[:n]
		resync(q)
		Verify(q, in, o)
	}
	for _, q := range []*Plan{{}, {Pasta: []string{"/x"}}, {Bwrap: []string{"/y", "--uid"}}, {Inner: []string{"a"}}, {PassFDs: []string{"a"}}} {
		if len(Verify(q, in, o)) == 0 {
			t.Errorf("an empty plan %+v verifies", q)
		}
	}
}
