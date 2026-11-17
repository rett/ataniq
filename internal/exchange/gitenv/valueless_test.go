package gitenv

import (
	"context"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// optionRead matches the messages git prints when it reads "--zzbogus" as an
// option (parse-options, the revision parser and the diff options each have
// their own wording). When "--zzbogus" is only a value or a revision, git
// says something else ("bad revision", "not a valid ref") or nothing.
var optionRead = regexp.MustCompile(`(?i)(unknown (option|switch)|unrecognized (argument|option)|invalid option)[^\n]*zzbogus`)

// askGit runs the command line of c with the environment of this package, in
// dir, for at most a short time, and returns what git printed.
func askGit(t *testing.T, g Git, c Cmd, dir string) string {
	t.Helper()
	argv, err := g.Argv(c)
	if err != nil {
		t.Fatalf("Argv %q: %v", c.Args, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = g.Env(c)
	cmd.Dir = dir
	out, _ := cmd.CombinedOutput() // the exit status does not matter, the text does
	return string(out)
}

// TestValuelessFlagsTakeNoValueInGit asks the installed git, which is the
// only honest answer. For every allowed subcommand and every flag of
// [valuelessFlags] that Argv accepts there, the command
//
//	git <sub> <flag> <marker> --zzbogus
//
// must not read "--zzbogus" as an option: the flag must not have taken the
// marker as its value. The table is built here at run time from
// [subcommands] and [valuelessFlags], so a flag added later is covered.
//
// "--zzbogus" is not enough for the commands that do not report an unknown
// option. "rev-parse" prints it back, so for it the probe is "--git-dir"
// (the output must not hold the repository path), and the other probes of
// [markerProbes] that belong to a subcommand are put after the marker for it
// ("config --list", "version --build-options", "count-objects -v",
// "check-ref-format --normalize"). A probe the flag itself stands for is
// skipped. "check-ref-format" fails with its usage text for either marker, so
// it cannot act on a probe in any case; it is asked all the same, so a git
// that changes this is seen.
//
// Proves: for these subcommands and flags, in this git, no accepted flag
// swallows the marker. Cannot prove: anything for flags that are not on the
// list (the strict rule refuses those), or other git versions.
func TestValuelessFlagsTakeNoValueInGit(t *testing.T) {
	g := needGit(t)

	for _, sub := range subcommands {
		t.Run(sub, func(t *testing.T) {
			t.Parallel()
			repo := bare(t, g)
			dir := t.TempDir()
			c := Cmd{GitDir: repo, Dir: dir}
			if sub == "init" || sub == "version" {
				c.GitDir = ""
			}
			probes := []markerProbe{markerProbes[0]} // "--zzbogus"
			for _, p := range markerProbes[1:] {
				if p.control == sub {
					probes = append(probes, p)
				}
			}
			tried := 0
			for _, flag := range valuelessFlags {
				for _, marker := range []string{"--", "--end-of-options"} {
					c.Args = []string{sub, flag, marker, "--zzbogus"}
					if _, err := g.Argv(c); err != nil {
						continue // refused here: that is the other way to be safe
					}
					tried++
					for _, p := range probes {
						if slices.Contains(p.args, flag) {
							continue // the flag itself has this effect
						}
						c.Args = append([]string{sub, flag, marker}, p.args...)
						if out := askGit(t, g, c, dir); p.seen(out, repo) {
							t.Errorf("git %s: %s took %q as its value and read %q as options, yet Argv accepts it:\n%s", sub, flag, marker, p.args, out)
						}
					}
				}
			}
			if tried == 0 {
				t.Errorf("Argv accepted no listed flag for %s, so nothing was asked", sub)
			}
		})
	}
}

// TestDetectorSeesAFlagThatTakesTheMarker is the control of the test above:
// plain git, with "-m" in update-ref and symbolic-ref, does read the argument
// after the marker as an option, and the detector sees it. Without this the
// test above could pass because the detector never fires.
func TestDetectorSeesAFlagThatTakesTheMarker(t *testing.T) {
	g := needGit(t)
	env := fixtureEnv(t)
	for _, sub := range []string{"update-ref", "symbolic-ref"} {
		for _, marker := range []string{"--", "--end-of-options"} {
			repo := bare(t, g)
			out, _ := run([]string{g.Path, "--git-dir=" + repo, sub, "-m", marker, "--zzbogus"}, env, "", "")
			if !optionRead.MatchString(out) {
				t.Errorf("control: plain git %s -m %s --zzbogus did not read --zzbogus as an option:\n%s", sub, marker, strings.TrimSpace(out))
			}
		}
	}
}
