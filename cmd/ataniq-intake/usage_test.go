package main

import (
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/usage.golden")

// goldenUsage is the usage of the command and of every subcommand, in one
// text.
func goldenUsage() string {
	var names []string
	for n := range subUsage {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("=== ataniq-intake help\n")
	b.WriteString(usageText)
	for _, n := range names {
		b.WriteString("\n=== ataniq-intake help " + n + "\n")
		b.WriteString(subUsage[n])
	}
	return b.String()
}

func TestGoldenUsage(t *testing.T) {
	path := filepath.Join("testdata", "usage.golden")
	got := goldenUsage()
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != got {
		t.Fatalf("the usage differs from %s; run the test with -update to rewrite it and read the diff", path)
	}
}

func TestEverySubcommandHasUsage(t *testing.T) {
	for name := range commands {
		if subUsage[name] == "" {
			t.Errorf("no usage for %s", name)
		}
	}
	for name := range subUsage {
		if commands[name] == nil {
			t.Errorf("usage for %s, which is no subcommand", name)
		}
		if !strings.Contains(usageText, "  "+name) {
			t.Errorf("the usage does not list %s", name)
		}
	}
}

// TestUsageMentionsWhatTheOwnerMustKnow keeps the sentences that the task
// requires from being edited out.
func TestUsageMentionsWhatTheOwnerMustKnow(t *testing.T) {
	for _, want := range []string{
		"the same list must be given to review and to integrate",
		"working checkout is never touched",
		"Without a usable template it refuses",
	} {
		if !strings.Contains(strings.Join(strings.Fields(usageText), " "), want) {
			t.Errorf("the usage lacks %q", want)
		}
	}
	if !strings.Contains(strings.Join(strings.Fields(subUsage["verify"]), " "), "as /bin/sh -c COMMAND inside the verification sandbox, in the scratch clone") {
		t.Error("the verify usage does not say where COMMAND runs")
	}
	if !strings.Contains(strings.Join(strings.Fields(subUsage["review"]), " "), "the digest covers it") {
		t.Error("the review usage does not say that the digest covers the path rules")
	}
}

func TestHelpForms(t *testing.T) {
	w := newWorld(t)
	for _, args := range [][]string{nil, {"help"}, {"-h"}, {"--help"}} {
		code, out, errb := w.run(args...)
		if code != exitOK || out != usageText || errb != "" {
			t.Errorf("%v: exit %d, stderr %q", args, code, errb)
		}
	}
	for name, u := range subUsage {
		for _, args := range [][]string{{"help", name}, {name, "-h"}, {name, "--help"}} {
			code, out, errb := w.run(args...)
			if code != exitOK || out != u || errb != "" {
				t.Errorf("%v: exit %d, stderr %q", args, code, errb)
			}
		}
	}
	code, out, _ := w.run("help", "nosuch")
	if code != exitUsage || out != "" {
		t.Errorf("help nosuch: %d %q", code, out)
	}
	if len(w.loads) != 0 {
		t.Error("help loaded a template")
	}
}

func TestUsageErrors(t *testing.T) {
	w := newWorld(t)
	w.initMirror()
	w.lead(testRole)
	s, p := w.state, testProject
	cases := map[string][]string{
		"unknown subcommand":     {"frobnicate"},
		"subcommand with escape": {"\x1b[2Jfetch"},
		"unknown flag":           {"init", "--state", s, "--project", p, "--nope"},
		"positional argument":    {"init", "--state", s, "--project", p, "extra"},
		"no state":               {"init", "--project", p},
		"no project":             {"init", "--state", s},
		"relative state":         {"init", "--state", "rel/state", "--project", p},
		"unclean state":          {"init", "--state", s + "/../" + filepath.Base(s), "--project", p},
		"state is missing":       {"init", "--state", s + "/none", "--project", p},
		"state is a file":        {"init", "--state", filepath.Join(w.other, "sandbox.json"), "--project", p},
		"bad project":            {"init", "--state", s, "--project", "Bad_Name"},
		"flag without value":     {"init", "--state"},
		"fetch without role":     {"fetch", "--state", s, "--project", p, "--sandbox", "x"},
		"fetch bad role":         {"fetch", "--state", s, "--project", p, "--role", "../x", "--sandbox", "x"},
		"fetch bad timeout":      {"fetch", "--state", s, "--project", p, "--role", testRole, "--sandbox", "x", "--timeout", "soon"},
		"fetch zero timeout":     {"fetch", "--state", s, "--project", p, "--role", testRole, "--sandbox", "x", "--timeout", "0s"},
		"fetch negative size":    {"fetch", "--state", s, "--project", p, "--role", testRole, "--sandbox", "x", "--max-staged", "-1"},
		"fetch zero size":        {"fetch", "--state", s, "--project", p, "--role", testRole, "--sandbox", "x", "--max-mirror", "0"},
		"review without branch":  {"review", "--state", s, "--project", p, "--role", testRole, "--target", "main"},
		"review without target":  {"review", "--state", s, "--project", p, "--role", testRole, "--branch", "main"},
		"review bad escape":      {"review", "--state", s, "--project", p, "--role", testRole, "--branch", `a\q`, "--target", "main"},
		"review bad branch":      {"review", "--state", s, "--project", p, "--role", testRole, "--branch", "-x", "--target", "main"},
		"review bad rule":        {"review", "--state", s, "--project", p, "--role", testRole, "--branch", "main", "--target", "main", "--path-rule", "[x]"},
		"verify without gate":    {"verify", "--state", s, "--project", p, "--role", testRole, "--branch", "main", "--sandbox", "x", "--records", w.records, "--work", w.work},
		"verify gate no equals":  {"verify", "--state", s, "--project", p, "--role", testRole, "--branch", "main", "--gate", "true", "--sandbox", "x", "--records", w.records, "--work", w.work},
		"verify gate bad name":   {"verify", "--state", s, "--project", p, "--role", testRole, "--branch", "main", "--gate", "Bad=true", "--sandbox", "x", "--records", w.records, "--work", w.work},
		"verify gate empty":      {"verify", "--state", s, "--project", p, "--role", testRole, "--branch", "main", "--gate", "a=", "--sandbox", "x", "--records", w.records, "--work", w.work},
		"verify gate newline":    {"verify", "--state", s, "--project", p, "--role", testRole, "--branch", "main", "--gate", "a=x\ny", "--sandbox", "x", "--records", w.records, "--work", w.work},
		"verify without records": {"verify", "--state", s, "--project", p, "--role", testRole, "--branch", "main", "--gate", "a=true", "--sandbox", "x", "--work", w.work},
		"verify without work":    {"verify", "--state", s, "--project", p, "--role", testRole, "--branch", "main", "--gate", "a=true", "--sandbox", "x", "--records", w.records},
		"integrate without tip":  {"integrate", "--state", s, "--project", p, "--role", testRole, "--branch", "main", "--target", "main", "--digest", strings.Repeat("a", 64), "--accept-change", "--records", w.records},
		"integrate short tip":    {"integrate", "--state", s, "--project", p, "--role", testRole, "--branch", "main", "--target", "main", "--tip", "abc123", "--digest", strings.Repeat("a", 64), "--accept-change", "--records", w.records},
		"integrate no digest":    {"integrate", "--state", s, "--project", p, "--role", testRole, "--branch", "main", "--target", "main", "--tip", strings.Repeat("a", 40), "--accept-change", "--records", w.records},
		"integrate bad digest":   {"integrate", "--state", s, "--project", p, "--role", testRole, "--branch", "main", "--target", "main", "--tip", strings.Repeat("a", 40), "--digest", strings.Repeat("A", 64), "--accept-change", "--records", w.records},
		"integrate no records":   {"integrate", "--state", s, "--project", p, "--role", testRole, "--branch", "main", "--target", "main", "--tip", strings.Repeat("a", 40), "--digest", strings.Repeat("a", 64), "--accept-change"},
		"integrate bad path":     {"integrate", "--state", s, "--project", p, "--role", testRole, "--branch", "main", "--target", "main", "--tip", strings.Repeat("a", 40), "--digest", strings.Repeat("a", 64), "--accept-change", "--accept-path", `a\q`, "--records", w.records},
		"branches bad role":      {"branches", "--state", s, "--project", p, "--role", "Bad"},
	}
	names := make([]string, 0, len(cases))
	for n := range cases {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		code, out, errb := w.run(cases[name]...)
		if code != exitUsage || out != "" || errb == "" {
			t.Errorf("%s: exit %d\nstdout %q\nstderr %q", name, code, out, errb)
		}
		noControl(t, name, errb)
	}
	if len(w.loads) != 0 {
		t.Errorf("a usage error loaded a template: %v", w.loads)
	}
}

func TestStateRootChecks(t *testing.T) {
	w := newWorld(t)
	link := filepath.Join(w.other, "link")
	if err := os.Symlink(w.state, link); err != nil {
		t.Fatal(err)
	}
	if code, _, errb := w.run("init", "--state", link, "--project", testProject); code != exitUsage || !strings.Contains(errb, "symlink") {
		t.Errorf("a symlink as the state root: %d %s", code, errb)
	}
	open := filepath.Join(w.other, "open")
	if err := os.Mkdir(open, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o777); err != nil {
		t.Fatal(err)
	}
	if code, _, errb := w.run("init", "--state", open, "--project", testProject); code != exitUsage || !strings.Contains(errb, "writable") {
		t.Errorf("a state root that others write: %d %s", code, errb)
	}
	if _, err := os.Stat(filepath.Join(open, "mirrors")); err == nil {
		t.Error("init made a folder in a refused state root")
	}
}

func TestNoGitIsAFailure(t *testing.T) {
	w := newWorld(t)
	w.git.Path = ""
	code, _, errb := w.sub("init")
	if code != exitFailed || !strings.Contains(errb, "git") {
		t.Fatalf("exit %d\n%s", code, errb)
	}
}
