package gitenv

import (
	"context"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

// markerProbe is an argument list that has a visible effect in the output of
// git when git reads it as options. "--zzbogus" shows in the message of a
// command that reports unknown options. The others serve the commands that do
// not report them: "rev-parse" prints an option it does not know, so it needs
// "--git-dir"; "config --list" prints the configuration; "version
// --build-options" and "count-objects -v" print more than they do without the
// option; "check-ref-format --normalize" prints the cleaned name.
type markerProbe struct {
	args    []string
	control string // a subcommand that reads the probe and shows it
	seen    func(out, repo string) bool
}

var markerProbes = []markerProbe{
	{[]string{"--zzbogus"}, "config", func(out, _ string) bool { return optionRead.MatchString(out) }},
	{[]string{"--git-dir"}, "rev-parse", func(out, repo string) bool { return strings.Contains(out, repo) }},
	{[]string{"--list"}, "config", func(out, _ string) bool { return strings.Contains(out, "core.bare") }},
	{[]string{"--build-options"}, "version", func(out, _ string) bool { return strings.Contains(out, "sizeof-long") }},
	{[]string{"-v"}, "count-objects", func(out, _ string) bool { return strings.Contains(out, "size-pack") }},
	{[]string{"--normalize", "refs//heads//zz"}, "check-ref-format", func(out, _ string) bool { return strings.Contains(out, "refs/heads/zz") }},
}

// rawGit runs "git <sub> <rest...>" with the settings and the environment of
// this package, but without the check of the options: Argv would refuse the
// pair that is being asked about. It returns what git printed and how it
// ended.
func rawGit(t *testing.T, g Git, c Cmd, sub string, rest ...string) (string, error) {
	t.Helper()
	argv, err := g.Argv(Cmd{GitDir: c.GitDir, Dir: c.Dir, Args: []string{sub}})
	if err != nil {
		t.Fatal(err)
	}
	argv = append(argv, rest...)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = g.Env(c)
	cmd.Dir = c.Dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// markerCase is a fresh repository and working folder for one subcommand.
func markerCase(t *testing.T, g Git, sub string) (Cmd, string) {
	t.Helper()
	repo := bare(t, g)
	c := Cmd{GitDir: repo, Dir: t.TempDir()}
	if slices.Contains(noGitDir, sub) {
		c.GitDir = ""
	}
	return c, repo
}

// TestMarkerFirstEndsTheOptionsInGit asks the installed git, for every
// allowed subcommand and both markers, with the marker right after the
// subcommand, whether git still reads the next argument as an option and acts
// on it. Each probe of [markerProbes] is put after the marker.
//
// The pairs where it does must be exactly the pairs of [markerNotEnd], and
// Argv must refuse them. For every other pair Argv must accept the command, so
// the rule refuses nothing it does not need to. That the probes can see an
// option read is shown by the pairs of the table (git acts there) and by the
// controls of TestMarkerProbesSeeAnOption.
//
// Proves: in this git, for these subcommands, the markers and the probes, a
// leading marker ends the options except for the pairs of the table. Cannot
// prove: anything for other git versions, other probes, or a marker that does
// not stand first.
func TestMarkerFirstEndsTheOptionsInGit(t *testing.T) {
	g := needGit(t)
	for _, sub := range subcommands {
		t.Run(sub, func(t *testing.T) {
			t.Parallel()
			c, repo := markerCase(t, g, sub)
			for _, marker := range []string{"--", "--end-of-options"} {
				var acted []string
				for _, p := range markerProbes {
					out, _ := rawGit(t, g, c, sub, append([]string{marker}, p.args...)...)
					if p.seen(out, repo) {
						acted = append(acted, strings.Join(p.args, " "))
					}
				}
				listed := slices.Contains(markerNotEnd[sub], marker)
				if (len(acted) > 0) != listed {
					t.Errorf("git %s %s: git read %q as options = %v, but the pair is in markerNotEnd = %v", sub, marker, acted, len(acted) > 0, listed)
				}
				c.Args = append([]string{sub, marker}, markerProbes[0].args...)
				_, err := g.Argv(c)
				if listed && err == nil {
					t.Errorf("Argv accepted %q, where git reads the next argument as an option", c.Args)
				}
				if !listed && err != nil {
					t.Errorf("Argv refused %q, where git ends the options: %v", c.Args, err)
				}
			}
		})
	}
}

// TestMarkerProbesSeeAnOption is the control of the test above: plain git,
// given each probe as options of the subcommand that knows it, shows the
// effect. Without it a probe that never fires would let the test pass.
func TestMarkerProbesSeeAnOption(t *testing.T) {
	g := needGit(t)
	for _, p := range markerProbes {
		c, repo := markerCase(t, g, p.control)
		out, _ := rawGit(t, g, c, p.control, p.args...)
		if !p.seen(out, repo) {
			t.Errorf("control: git %s %s did not show the probe:\n%s", p.control, strings.Join(p.args, " "), strings.TrimSpace(out))
		}
	}
}

// TestMarkerReadThenDie pins the pairs that stay allowed although git reads
// the argument after the marker as an option: it reads it and dies, so
// nothing acts. They are listed in the checkOptions comment. If a git release
// changes one, the test fails and the pair needs a decision.
func TestMarkerReadThenDie(t *testing.T) {
	g := needGit(t)
	const died = "must come before non-option arguments"
	for _, sub := range []string{"rev-parse", "rev-list", "log", "diff", "diff-tree"} {
		c, repo := markerCase(t, g, sub)
		for _, p := range markerProbes {
			out, err := rawGit(t, g, c, sub, append([]string{"--end-of-options"}, p.args...)...)
			if err == nil || !strings.Contains(out, died) {
				t.Errorf("git %s --end-of-options %s: want a failure that says %q, got err=%v:\n%s", sub, strings.Join(p.args, " "), died, err, out)
			}
			if p.seen(out, repo) {
				t.Errorf("git %s --end-of-options %s acted on the option:\n%s", sub, strings.Join(p.args, " "), out)
			}
		}
	}
}

// TestCheckRefFormatTakesNoMarker pins what the checkOptions comment says of
// check-ref-format: a marker, in either form, makes it print its usage text
// and fail, even before a valid name, so it never acts on what follows.
func TestCheckRefFormatTakesNoMarker(t *testing.T) {
	g := needGit(t)
	c, _ := markerCase(t, g, "check-ref-format")
	for _, marker := range []string{"--", "--end-of-options"} {
		for _, tail := range [][]string{{"refs/heads/x"}, {"--normalize", "refs//heads//zz"}} {
			out, err := rawGit(t, g, c, "check-ref-format", append([]string{marker}, tail...)...)
			if err == nil || !strings.Contains(out, "usage: git check-ref-format") || strings.Contains(out, "refs/heads/zz") {
				t.Errorf("git check-ref-format %s %q: want the usage text and a failure, got err=%v:\n%s", marker, tail, err, out)
			}
		}
	}
}
