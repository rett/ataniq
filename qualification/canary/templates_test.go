package canary

import (
	"os"
	"os/user"
	"regexp"
	"strings"
	"testing"

	"ataniq/internal/exchange/sandboxplan"
)

// The template is checked for form only. These tests feed it to the intake's
// own parser, look at the argument lists it renders, and scan it and its
// note for names that belong to one machine. None of them starts a process,
// a namespace or bubblewrap: whether the template confines is for the owner
// to vouch for, from the note beside it.

const (
	templateFile = "templates/canary-intake.json"
	noteFile     = "templates/README.md"
	launcher     = "/usr/bin/bwrap"
)

func read(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func parse(t *testing.T) *sandboxplan.Template {
	t.Helper()
	tpl, err := sandboxplan.ParseTemplate(read(t, templateFile))
	if err != nil {
		t.Fatalf("the intake's template parser refuses %s: %v", templateFile, err)
	}
	return tpl
}

// Sample values for the placeholders. They are neutral and never reach a
// process.
var (
	sampleServe  = sandboxplan.ServeRequest{Role: "sample-role", Project: "sample-project", Tree: "/sample/tree"}
	sampleVerify = sandboxplan.VerifyRequest{
		Project: "sample-project", Mirror: "/sample/mirror", Scratch: "/sample/scratch", Home: "/sample/owner-home",
	}
)

func TestTemplateAccepted(t *testing.T) {
	tpl := parse(t)
	if _, err := tpl.Serve(sampleServe); err != nil {
		t.Errorf("serve section: %v", err)
	}
	if _, err := tpl.Verify(sampleVerify); err != nil {
		t.Errorf("verify section: %v", err)
	}
}

// flag is one bubblewrap option with the words it takes.
type flag struct {
	name string
	args []string
}

// arity is how many words each option the template may use takes. An option
// that is not here fails the test, so that adding one is a reviewed change
// of this table.
var arity = map[string]int{
	"--die-with-parent": 0, "--new-session": 0, "--clearenv": 0,
	"--unshare-user": 0, "--unshare-ipc": 0, "--unshare-pid": 0,
	"--unshare-net": 0, "--unshare-uts": 0, "--unshare-cgroup": 0,
	"--disable-userns": 0, "--assert-userns-disabled": 0,
	"--cap-drop": 1, "--hostname": 1, "--chdir": 1,
	"--proc": 1, "--dev": 1, "--tmpfs": 1,
	"--ro-bind": 2, "--bind": 2, "--symlink": 2, "--setenv": 2,
}

// split reads the options of a bubblewrap argument list and returns them and
// the program that follows them, with its arguments.
func split(t *testing.T, argv []string) (flags []flag, program []string) {
	t.Helper()
	if len(argv) == 0 || argv[0] != launcher {
		t.Fatalf("the launcher is %q, want %s", argv, launcher)
	}
	i := 1
	for i < len(argv) && strings.HasPrefix(argv[i], "--") {
		n, ok := arity[argv[i]]
		if !ok {
			t.Fatalf("option %s is not in this test's table: review it, then add it", argv[i])
		}
		if i+n >= len(argv) {
			t.Fatalf("option %s is cut short", argv[i])
		}
		flags = append(flags, flag{argv[i], argv[i+1 : i+1+n]})
		i += n + 1
	}
	return flags, argv[i:]
}

// hasFlag reports whether the option appears, with the given first word when
// one is given.
func hasFlag(flags []flag, name string, first ...string) bool {
	for _, f := range flags {
		if f.name == name && (len(first) == 0 || (len(f.args) > 0 && f.args[0] == first[0])) {
			return true
		}
	}
	return false
}

func sources(flags []flag, name string) []string {
	var out []string
	for _, f := range flags {
		if f.name == name {
			out = append(out, f.args[0])
		}
	}
	return out
}

func sameList(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// checkCommon holds for every sandbox the template starts: all the
// namespaces that bubblewrap can unshare, a cleared environment, and no
// option that shares or opens anything with the host.
func checkCommon(t *testing.T, what string, flags []flag) {
	t.Helper()
	for _, name := range []string{
		"--die-with-parent", "--new-session", "--clearenv",
		"--unshare-user", "--unshare-ipc", "--unshare-pid", "--unshare-net",
		"--unshare-uts", "--unshare-cgroup",
		"--disable-userns", "--assert-userns-disabled",
	} {
		if !hasFlag(flags, name) {
			t.Errorf("%s: %s is missing", what, name)
		}
	}
	if !hasFlag(flags, "--cap-drop", "ALL") {
		t.Errorf("%s: --cap-drop ALL is missing", what)
	}
	if !hasFlag(flags, "--chdir") {
		t.Errorf("%s: --chdir is missing: bubblewrap would name the caller's folder in its warning", what)
	}
	if !hasFlag(flags, "--hostname") {
		t.Errorf("%s: --hostname is missing: the host's name would be visible inside", what)
	}
	if got := sources(flags, "--ro-bind"); len(got) == 0 || got[0] != "/usr" {
		t.Errorf("%s: the first read-only bind is %q, want /usr", what, got)
	}
	for _, f := range flags {
		if f.name == "--setenv" && f.args[0] != "PATH" && f.args[0] != "HOME" && f.args[0] != "TMPDIR" && f.args[0] != "GIT_CONFIG_NOSYSTEM" {
			t.Errorf("%s: unexpected variable %s", what, f.args[0])
		}
	}
}

func TestServeForm(t *testing.T) {
	plan, err := parse(t).Serve(sampleServe)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Repository == "" || strings.Contains(plan.Repository, sampleServe.Tree) {
		t.Errorf("repository %q must be a name inside the sandbox", plan.Repository)
	}
	for _, c := range []struct {
		what string
		argv []string
		prog string
	}{
		{"upload_pack", plan.UploadPack, "/usr/bin/git-upload-pack"},
		{"measure", plan.Measure.Argv, "/usr/bin/du"},
	} {
		flags, program := split(t, c.argv)
		checkCommon(t, c.what, flags)
		if len(program) == 0 || program[0] != c.prog {
			t.Errorf("%s runs %q, want %s", c.what, program, c.prog)
		}
		// The clone is read-only and nothing is writable.
		if got := sources(flags, "--ro-bind"); !sameList(got, []string{"/usr", sampleServe.Tree}) {
			t.Errorf("%s: read-only binds %q, want /usr and the tree", c.what, got)
		}
		for _, name := range []string{"--bind", "--tmpfs", "--proc"} {
			if hasFlag(flags, name) {
				t.Errorf("%s: %s is not part of the serving sandbox", c.what, name)
			}
		}
	}
	if got := plan.Measure.Env; !sameList(got, []string{"PATH=/usr/bin:/bin"}) {
		t.Errorf("measure environment %q", got)
	}
}

func TestVerifyForm(t *testing.T) {
	plan, err := parse(t).Verify(sampleVerify)
	if err != nil {
		t.Fatal(err)
	}
	flags, program := split(t, plan.Prefix)
	if len(program) != 0 {
		t.Errorf("the prefix ends with %q: the command appends the inner command", program)
	}
	checkCommon(t, "verify prefix", flags)
	if got := sources(flags, "--ro-bind"); !sameList(got, []string{"/usr", sampleVerify.Mirror}) {
		t.Errorf("read-only binds %q, want /usr and the mirror", got)
	}
	if got := sources(flags, "--bind"); !sameList(got, []string{sampleVerify.Scratch, sampleVerify.Home}) {
		t.Errorf("writable binds %q, want the scratch and home folders only", got)
	}
	if !sameList(plan.Env, []string{"PATH=/usr/bin:/bin"}) {
		t.Errorf("environment %q", plan.Env)
	}
	// The inside paths are names inside the sandbox, not the host's folders.
	for _, p := range []string{plan.Mirror, plan.Scratch, plan.Home} {
		for _, host := range []string{sampleVerify.Mirror, sampleVerify.Scratch, sampleVerify.Home} {
			if p == host {
				t.Errorf("inside path %q is a host path", p)
			}
		}
	}
	cmd, err := plan.Command([]string{"/bin/sh", "-c", "true"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Argv[0] != launcher {
		t.Errorf("command starts with %q", cmd.Argv[0])
	}
}

// Names that belong to one machine. A home path is the plain mark of one;
// the other two are the names of this host and of the person who runs the
// test, found when the test runs, and a few shapes that a host name or an
// address takes.
var (
	homePath = regexp.MustCompile(`/home/|/Users/|/root/|/var/home/`)
	fqdn     = regexp.MustCompile(`(?i)\b[a-z0-9][a-z0-9-]*(\.[a-z0-9-]+)*\.(com|org|net|io|dev|local|lan|internal|localdomain|corp|home|test|example)\b`)
	ipv4     = regexp.MustCompile(`\b[0-9]{1,3}(\.[0-9]{1,3}){3}\b`)
)

// leaks returns what in text names a machine, a user or a home path.
// Names found at run time count only as a path component or beside an @,
// which is how a leak looks, so that a machine named like a word of the file
// does not fail the test.
func leaks(text string, names []string, homes []string) []string {
	var out []string
	if m := homePath.FindString(text); m != "" {
		out = append(out, "home path "+m)
	}
	for _, h := range homes {
		if h != "" && h != "/" && strings.Contains(text, h) {
			out = append(out, "the home folder of this host")
		}
	}
	if m := fqdn.FindString(text); m != "" {
		out = append(out, "host name "+m)
	}
	if m := ipv4.FindString(text); m != "" {
		out = append(out, "address "+m)
	}
	for _, n := range names {
		q := regexp.QuoteMeta(n)
		re := regexp.MustCompile(`(?i)(/|@)` + q + `($|[^A-Za-z0-9_-])|(^|[^A-Za-z0-9_./-])` + q + `@`)
		if re.MatchString(text) {
			out = append(out, "name "+n)
		}
	}
	return out
}

// hostNames returns the names of this host and of this user, as the
// scanner looks for them. A name of two letters or fewer, and the names of
// the system accounts, are not looked for.
func hostNames() (names, homes []string) {
	add := func(n string) {
		n = strings.TrimSpace(n)
		if len(n) <= 2 || n == "root" || n == "nobody" {
			return
		}
		names = append(names, n)
	}
	if h, err := os.Hostname(); err == nil {
		add(h)
		short, _, _ := strings.Cut(h, ".")
		add(short)
	}
	if u, err := user.Current(); err == nil {
		add(u.Username)
		homes = append(homes, u.HomeDir)
	}
	add(os.Getenv("USER"))
	add(os.Getenv("LOGNAME"))
	if h, err := os.UserHomeDir(); err == nil {
		homes = append(homes, h)
	}
	return names, homes
}

func TestNoMachineNames(t *testing.T) {
	names, homes := hostNames()
	for _, path := range []string{templateFile, noteFile} {
		if l := leaks(string(read(t, path)), names, homes); len(l) > 0 {
			t.Errorf("%s names something that belongs to one machine: %s", path, strings.Join(l, "; "))
		}
	}
}

// The scanner must see what it is for.
func TestScannerFindsLeaks(t *testing.T) {
	names := []string{"examplehost", "exampleuser"}
	homes := []string{"/srv/example-owner"}
	for _, c := range []struct{ text, want string }{
		{`"--ro-bind", "/home/someone/x"`, "home path"},
		{"see /Users/someone", "home path"},
		{"/root/.config", "home path"},
		{"path /srv/example-owner/x", "home folder"},
		{"fetch from builder.example.com", "host name"},
		{"fetch from nas.lan", "host name"},
		{"fetch from 192.0.2.7", "address"},
		{"/srv/exampleuser/x", "name exampleuser"},
		{"exampleuser@elsewhere", "name exampleuser"},
		{"ssh://exampleuser@elsewhere", "name exampleuser"},
		{"/data/ExampleHost", "name examplehost"},
	} {
		l := strings.Join(leaks(c.text, names, homes), "; ")
		if !strings.Contains(l, c.want) {
			t.Errorf("scan of %q gave %q, want %q", c.text, l, c.want)
		}
	}
	for _, clean := range []string{
		"/ataniq/mirror/{project}.git", "--unshare-user", "/usr/bin/git-upload-pack",
		"canary-token.txt", "docs/lead-intake.md", "/ataniq/home",
	} {
		if l := leaks(clean, names, homes); len(l) > 0 {
			t.Errorf("scan of %q found %v", clean, l)
		}
	}
}

// The note must be there and say what the owner vouches for. The words are
// the labels of its list: one per line.
func TestNoteLabels(t *testing.T) {
	note := string(read(t, noteFile))
	for _, label := range []string{
		"Read-only binds:", "Writable binds:", "Shared with the host:", "Programs:",
		"Not confined:", "The test:",
	} {
		if !strings.Contains(note, "- **"+label+"**") {
			t.Errorf("the note has no list item %q", label)
		}
	}
	if !strings.Contains(note, "form only") {
		t.Error("the note must say that the test checks form only")
	}
}
