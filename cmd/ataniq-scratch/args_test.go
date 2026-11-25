package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestValidName(t *testing.T) {
	good := []string{"a", "0", "a-b", "x9", strings.Repeat("a", 31), "9-"}
	bad := []string{"", "-a", "A", "a_b", "a.b", "a/b", "a b", strings.Repeat("a", 32), "é", "a\n", ".."}
	for _, s := range good {
		if !validName(s) {
			t.Errorf("validName(%q) = false", s)
		}
	}
	for _, s := range bad {
		if validName(s) {
			t.Errorf("validName(%q) = true", s)
		}
	}
}

func TestParseStart(t *testing.T) {
	getenv := func(string) string { return "" }
	cfg, err := parseStart([]string{"n1", "--cols", "120", "--rows=40", "--env", "A=1", "--env", "B=two words", "--holder", "/x/holder", "--direct", "--", "/bin/agent", "--flag", "--", "x"}, getenv)
	if err != nil {
		t.Fatal(err)
	}
	want := startConfig{name: "n1", cols: 120, rows: 40, env: []string{"A=1", "B=two words"}, holder: "/x/holder", direct: true, argv: []string{"/bin/agent", "--flag", "--", "x"}}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("got %+v, want %+v", cfg, want)
	}
	cfg, err = parseStart([]string{"n1", "--", "/bin/agent"}, getenv)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.cols != defaultCols || cfg.rows != defaultRows || cfg.direct || cfg.holder != "" || len(cfg.env) != 0 {
		t.Errorf("defaults: %+v", cfg)
	}
}

func TestParseStartRefusals(t *testing.T) {
	const secret = "SECRETTEXT"
	cases := map[string][]string{
		"no name":            {},
		"bad name":           {secret + "_X", "--", "/bin/a"},
		"no dash":            {"n"},
		"empty argv":         {"n", "--"},
		"stray positional":   {"n", secret, "--", "/bin/a"},
		"unknown flag":       {"n", "--" + secret, "--", "/bin/a"},
		"flag lacks value":   {"n", "--cols", "--", "/bin/a"},
		"cols not a number":  {"n", "--cols", secret, "--", "/bin/a"},
		"cols too small":     {"n", "--cols", "19", "--", "/bin/a"},
		"cols too large":     {"n", "--cols", "501", "--", "/bin/a"},
		"rows too small":     {"n", "--rows", "4", "--", "/bin/a"},
		"rows too large":     {"n", "--rows", "201", "--", "/bin/a"},
		"env without equals": {"n", "--env", secret, "--", "/bin/a"},
		"env bad key":        {"n", "--env", "1A=" + secret, "--", "/bin/a"},
		"env bad key 2":      {"n", "--env", "A-B=" + secret, "--", "/bin/a"},
		"env empty key":      {"n", "--env", "=" + secret, "--", "/bin/a"},
		"env control char":   {"n", "--env", "A=" + secret + "\n", "--", "/bin/a"},
		"env too long":       {"n", "--env", "A=" + strings.Repeat("x", maxEnvValue+1), "--", "/bin/a"},
		"env sets TERM":      {"n", "--env", "TERM=" + secret, "--", "/bin/a"},
		"env sets HOME":      {"n", "--env", "HOME=" + secret, "--", "/bin/a"},
		"env sets PATH":      {"n", "--env", "PATH=" + secret, "--", "/bin/a"},
		"env sets LANG":      {"n", "--env", "LANG=" + secret, "--", "/bin/a"},
		"env repeated":       {"n", "--env", "A=1", "--env", "A=" + secret, "--", "/bin/a"},
		"holder relative":    {"n", "--holder", secret, "--", "/bin/a"},
		"agent relative":     {"n", "--", secret},
		"agent dot-relative": {"n", "--", "./" + secret},
		"agent control char": {"n", "--", "/bin/" + secret + "\n"},
		"agent too long":     {"n", "--", "/" + strings.Repeat("x", maxAgentPath)},
		"bool with junk":     {"n", "--direct=" + secret, "--", "/bin/a"},
	}
	for name, args := range cases {
		_, err := parseStart(args, func(string) string { return "" })
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if _, ok := err.(usageError); !ok {
			t.Errorf("%s: error %T, want usageError", name, err)
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("%s: the error echoes the argument: %q", name, err)
		}
	}
	long := make([]string, 0, 2*maxEnv+4)
	long = append(long, "n")
	for i := 0; i <= maxEnv; i++ {
		long = append(long, "--env", "K"+strings.Repeat("a", i)+"=1")
	}
	long = append(long, "--", "/bin/a")
	if _, err := parseStart(long, func(string) string { return "" }); err == nil {
		t.Error("more than maxEnv --env flags accepted")
	}
}

func TestRunUsageErrorsNeverEchoArguments(t *testing.T) {
	const secret = "SECRETTEXT"
	e := newTestEnv(t)
	for _, args := range [][]string{
		nil,
		{secret},
		{"start", secret + "_"},
		{"start", "n", "--" + secret, "--", "/bin/a"},
		{"start", "n", "--", secret, "x"},
		{"start", "n", "--direct", "--", "./" + secret},
		{"ticket", secret + "_"},
		{"ticket", "n", "--" + secret},
		{"ticket", "n", secret},
		{"status"},
		{"status", "n", secret},
		{"stop", "n", "--" + secret},
		{"release-writer", "n", "-x"},
	} {
		l := &fakeLauncher{}
		a, out, errout := e.newApp(l)
		code := a.run(args)
		if code != exitUsage {
			t.Errorf("%q: exit %d, want 2", args, code)
		}
		if s := out.String() + errout.String(); strings.Contains(s, secret) {
			t.Errorf("%q: output echoes the argument", args)
		}
		if !strings.HasPrefix(errout.String(), "error: ") || !strings.Contains(errout.String(), "Usage:") {
			t.Errorf("%q: want a short diagnostic then the usage, got %q", args, errout.String())
		}
		if len(l.runList()) != 0 || len(l.starts) != 0 {
			t.Errorf("%q: started something", args)
		}
	}
}

func TestHelp(t *testing.T) {
	e := newTestEnv(t)
	for _, args := range [][]string{{"-h"}, {"--help"}, {"help"}, {"start", "-h"}, {"start", "n", "-h"}, {"ticket", "--help"}, {"stop", "n", "-h"}} {
		a, out, errout := e.newApp(&fakeLauncher{})
		if code := a.run(args); code != exitOK {
			t.Errorf("%q: exit %d", args, code)
		}
		if !strings.Contains(out.String(), "SCRATCH agent only") || !strings.Contains(out.String(), "not for a real lead") {
			t.Errorf("%q: the help does not say it is scratch-only", args)
		}
		if errout.String() != "" {
			t.Errorf("%q: stderr %q", args, errout.String())
		}
	}
}

// TestHelpCommandListIsWhole: the entries of the commands follow one another
// in the order of the usage lines, with no other paragraph between them, so
// each entry reads as its command's.
func TestHelpCommandListIsWhole(t *testing.T) {
	lines := strings.Split(usageText, "\n")
	first, last := -1, -1
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, "start    "):
			first = i
		case strings.HasPrefix(l, "stop     "):
			last = i
		}
	}
	if first < 0 || last < first {
		t.Fatalf("no command list from start to stop in the help")
	}
	var names []string
	for _, l := range lines[first : last+1] {
		if l == "" {
			t.Fatalf("a blank line splits the command list:\n%s", strings.Join(lines[first:last+1], "\n"))
		}
		if !strings.HasPrefix(l, " ") {
			names = append(names, strings.Fields(l)[0])
		}
	}
	if got, want := strings.Join(names, " "), "start ticket status release-writer stop"; got != want {
		t.Fatalf("the command list is %q, want %q", got, want)
	}
}

func TestLangFrom(t *testing.T) {
	for in, want := range map[string]string{
		"":                      "C.UTF-8",
		"de_DE.UTF-8":           "de_DE.UTF-8",
		"en_US@euro":            "en_US@euro",
		"bad value":             "C.UTF-8",
		"x\ny":                  "C.UTF-8",
		"--env":                 "--env",
		strings.Repeat("a", 65): "C.UTF-8",
	} {
		got := langFrom(func(string) string { return in })
		if got != want {
			t.Errorf("langFrom(%q) = %q, want %q", in, got, want)
		}
	}
}
