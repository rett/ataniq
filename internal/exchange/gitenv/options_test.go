package gitenv

import (
	"strings"
	"testing"
)

// TestOptionsBeforeTheEndAreStrict covers the rule for options before the
// first "--" or "--end-of-options": each one is written "--name=value" or is
// a flag of [valuelessFlags]. Nothing else may stand there, so no option can
// take the end-of-options marker, or the argument after it, as its value.
func TestOptionsBeforeTheEndAreStrict(t *testing.T) {
	g := Git{Path: "/usr/bin/git"}
	ok := "/srv/r.git"
	for _, tc := range []struct {
		sub  string
		args []string
	}{
		// Accepted: "=" holds the value; the listed flags take none.
		{"fetch", []string{"--depth=1", "--no-tags", "--prune", "--end-of-options", "/srv/src"}},
		{"fetch", []string{"-q", "--quiet", "--no-recurse-submodules", "--no-write-fetch-head", "--", "/srv/src"}},
		{"log", []string{"--max-count=1", "--format=%H", "--first-parent", "--no-walk", "--end-of-options", "HEAD"}},
		{"config", []string{"--file", "/dev/stdin", "--list", "-z"}},
		{"config", []string{"--file=/dev/stdin", "--list", "-z"}},
		{"for-each-ref", []string{"--count=5", "--format=%(refname)", "refs/heads/-x"}},
		{"rev-parse", []string{"--verify", "-", "x"}},
		// After the end of the options git reads no options.
		{"fetch", []string{"--end-of-options", "-o", "--depth", "1", "--upload-pack=x"}},
		{"log", []string{"--", "-o", "--exec", "-"}},
	} {
		if _, err := g.Argv(Cmd{GitDir: ok, Args: append([]string{tc.sub}, tc.args...)}); err != nil {
			t.Errorf("%s %q: %v", tc.sub, tc.args, err)
		}
	}

	for _, tc := range []struct {
		name string
		sub  string
		args []string
		msg  string // part of the error text
	}{
		// The probes of the review: an option takes the marker as its value,
		// and git then reads the next argument as an option.
		{"-o takes --", "fetch", []string{"-o", "--", "--upload-pack=/abs/evil.sh", "/abs/src", "+refs/heads/*:refs/heads/*"}, `"-o"`},
		{"--server-option takes the marker", "fetch", []string{"--server-option", "--end-of-options", "--upl=/abs/evil.sh", "/abs/src", "+refs/heads/*:refs/heads/*"}, `"--server-option"`},
		{"--negotiation-tip takes the marker", "fetch", []string{"--negotiation-tip", "--end-of-options", "--upload-pack=/abs/evil.sh", "/abs/src"}, `"--negotiation-tip"`},
		{"--shallow-exclude takes the marker", "fetch", []string{"--shallow-exclude", "--", "--upload-pack=/abs/evil.sh", "/abs/src"}, `"--shallow-exclude"`},
		{"--refmap takes the marker", "fetch", []string{"--refmap", "--end-of-options", "--upload-pack=/abs/evil.sh", "/abs/src"}, `"--refmap"`},
		// The same without a marker, and other separate values.
		{"--depth 1", "fetch", []string{"--depth", "1", "--end-of-options", "/abs/src"}, `"--depth"`},
		{"-j 4", "fetch", []string{"-j", "4", "/abs/src"}, `"-j"`},
		{"-n 1", "log", []string{"-n", "1"}, `"-n"`},
		{"-1", "log", []string{"-1"}, `"-1"`},
		{"-n1", "log", []string{"-n1"}, `"-n1"`},
		{"joined short values", "fetch", []string{"-ofoo"}, `"-ofoo"`},
		{"unknown long flag", "fetch", []string{"--force"}, `"--force"`},
		{"a look-alike of a listed flag", "fetch", []string{"--prune-x"}, `"--prune-x"`},
		{"a look-alike of the marker", "fetch", []string{"--end-of-options-x", "--end-of-options", "/abs/src"}, `"--end-of-options-x"`},
		// A separate-value flag may not take an option or a marker.
		{"--file takes the marker", "config", []string{"--file", "--", "--list"}, `"--file"`},
		{"--file takes an option", "config", []string{"--file", "--list"}, `"--file"`},
		{"--file at the end", "config", []string{"--file"}, `"--file"`},
		{"--file takes the next --file", "config", []string{"--file", "--file", "x"}, `"--file"`},
		// The program-naming options are refused in every form.
		{"--upload-pack=", "fetch", []string{"--upload-pack=x", "--end-of-options", "/abs/src"}, "is refused"},
		{"--upload-pack abbreviated", "fetch", []string{"--upl=x", "--end-of-options", "/abs/src"}, "is refused"},
		{"--upload-pack with a value", "fetch", []string{"--upload-pack", "x", "--end-of-options", "/abs/src"}, "is refused"},
		{"--upload-pack alone", "fetch", []string{"--upload-pack"}, "is refused"},
		{"--receive-pack", "fetch", []string{"--receive-pack=x"}, "is refused"},
		{"--exec", "fetch", []string{"--exec=x"}, "is refused"},
		{"--ext-diff", "diff", []string{"--ext-diff", "a", "b"}, "is refused"},
		{"--ext abbreviated", "diff", []string{"--ext", "a", "b"}, "is refused"},
		{"--upload-pack after a flag with a value", "fetch", []string{"--depth=1", "--upload-pack=x"}, "is refused"},
	} {
		argv, err := g.Argv(Cmd{GitDir: ok, Args: append([]string{tc.sub}, tc.args...)})
		if err == nil {
			t.Errorf("%s: Argv accepted %q", tc.name, argv)
			continue
		}
		if argv != nil {
			t.Errorf("%s: Argv returned %q with its error", tc.name, argv)
		}
		if !strings.Contains(err.Error(), tc.msg) {
			t.Errorf("%s: error %q does not hold %s", tc.name, err, tc.msg)
		}
		if tc.msg != "is refused" && tc.sub != "config" && !strings.Contains(err.Error(), `"="`) {
			t.Errorf("%s: error %q does not say to write the value with =", tc.name, err)
		}
		if tc.msg != "is refused" && tc.sub != "config" && !strings.Contains(err.Error(), "ask for the flag") {
			t.Errorf("%s: error %q does not say how to get a flag added", tc.name, err)
		}
	}

	// The same inputs with UploadPack set are refused too.
	if _, err := g.Argv(Cmd{GitDir: ok, Protocols: []string{"file"}, UploadPack: []string{"/usr/bin/wrap"},
		Args: []string{"fetch", "-o", "--", "--upload-pack=/abs/evil.sh", "/abs/src"}}); err == nil {
		t.Error("the probe was accepted with UploadPack set")
	}
}

// TestValuelessFlagsTakeNoValue keeps the list honest: the entries are
// options, none is a form that holds a value, and none repeats.
func TestValuelessFlagsTakeNoValue(t *testing.T) {
	seen := map[string]bool{}
	for _, f := range valuelessFlags {
		if !strings.HasPrefix(f, "-") || strings.Contains(f, "=") || f == "--" || f == "-" {
			t.Errorf("valuelessFlags holds %q", f)
		}
		if seen[f] {
			t.Errorf("valuelessFlags holds %q twice", f)
		}
		seen[f] = true
		for _, r := range refusedOptions {
			if strings.HasPrefix(r, f) && len(f) >= 5 {
				t.Errorf("valuelessFlags holds %q, a refused option", f)
			}
		}
	}
	// What the review asked for by name.
	for _, f := range []string{"--no-tags", "--prune", "--no-recurse-submodules", "--no-write-fetch-head", "--no-auto-maintenance", "--no-auto-gc", "--bare", "--quiet", "-q", "-z", "-r", "-m", "--raw", "--root", "--no-renames", "--full-index", "--no-color", "--no-ext-diff", "--no-textconv", "--verify", "--is-ancestor", "--no-deref", "--stdin", "--list", "--batch-check", "--batch", "--objects", "--no-walk", "--first-parent", "--no-mailmap", "--no-notes", "--no-decorate", "--no-show-signature", "-v", "--count", "--all", "--local"} {
		if !seen[f] {
			t.Errorf("valuelessFlags lacks %q", f)
		}
	}
}

// TestRefusedProbesStartNothing runs the probes of the review. The control
// shows that plain git starts the program the probe names, because an option
// takes the marker as its value. Through this package each probe is refused
// and nothing is started.
//
// Proves: for these inputs the program is not started. Cannot prove: that no
// other input gets past the rule; the table test above covers the rule.
func TestRefusedProbesStartNothing(t *testing.T) {
	g := needGit(t)
	src := source(t, g)
	script, mark := canary(t)
	env := fixtureEnv(t, "GIT_ALLOW_PROTOCOL=file")
	refspec := "+refs/heads/*:refs/heads/*"
	probes := []struct {
		name string
		args []string
	}{
		{"-o", []string{"-o", "--", "--upload-pack=" + script, src, refspec}},
		{"--server-option", []string{"--server-option", "--end-of-options", "--upl=" + script, src, refspec}},
		{"--negotiation-tip", []string{"--negotiation-tip", "--end-of-options", "--upload-pack=" + script, src, refspec}},
		{"--shallow-exclude", []string{"--shallow-exclude", "--", "--upload-pack=" + script, src, refspec}},
		{"--refmap", []string{"--refmap", "--end-of-options", "--upload-pack=" + script, src, refspec}},
	}
	for _, p := range probes {
		t.Run(p.name, func(t *testing.T) {
			repo := bare(t, g)
			// Control: plain git runs the program.
			run(append([]string{g.Path, "--git-dir=" + repo, "fetch"}, p.args...), env, "", "")
			mustFire(t, mark, "plain git with the probe "+p.name)

			c := Cmd{GitDir: repo, Protocols: []string{"file"}, Args: append([]string{"fetch"}, p.args...)}
			if out, err := owned(g, c); err == nil {
				t.Fatalf("the probe %s was accepted and ran: %s", p.name, out)
			}
			mustNotFire(t, mark, "the probe "+p.name+" through this package")
		})
	}
}
