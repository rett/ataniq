package main

import (
	"sort"
	"strings"
	"testing"

	"ataniq/internal/exchange/boundedexec"
	"ataniq/internal/intake/execconfig"
)

// samplePath turns a rule into a path that it matches. A folder rule gets a
// file below the folder; a name with a star gets a plain letter for it.
func samplePath(pattern string) string {
	p := strings.NewReplacer("*", "x", "?", "x").Replace(strings.TrimPrefix(pattern, "/"))
	if strings.HasSuffix(p, "/") {
		return p + "file"
	}
	return p
}

// execConfigPaths returns one sample path for every rule of the default set.
// ".git" cannot be a path of a repository: git refuses it, and so it cannot
// be a lead's change.
func execConfigPaths(t *testing.T) map[string]string {
	t.Helper()
	paths := map[string]string{}
	set := execconfig.Default()
	for _, r := range set.Rules() {
		if r.Pattern == ".git" {
			continue
		}
		p := samplePath(r.Pattern)
		if m, ok := set.Match(p); !ok || m.Pattern == "" {
			t.Fatalf("sample %q does not match its rule %q", p, r.Pattern)
		}
		paths[p] = r.Pattern
	}
	return paths
}

// TestEveryExecConfigPathNeedsItsOwnSignOff is design case S19, end to end:
// one lead branch changes a path of every rule of the execution-config set.
// Review lists each of them under "Needs your sign-off", and integration
// without the sign-off of any one of them is refused.
func TestEveryExecConfigPathNeedsItsOwnSignOff(t *testing.T) {
	w := newWorld(t)
	w.runner = &fakeRunner{res: boundedexec.Result{Stdout: []byte("ok\n")}}
	w.initMirror()
	base := w.lead(testRole)
	w.branch(testRole, "config")
	paths := execConfigPaths(t)
	files := map[string]string{}
	for p := range paths {
		files[p] = "x\n"
	}
	// A plain file with a name that is no rule's: it is not listed.
	files["docs/plain.txt"] = "plain\n"
	w.write(testRole, files)
	tip := w.commit(testRole, "change every execution-config path")
	w.fetch(testRole)

	rev := w.review(testRole, "config", "main")
	if rev.tip != tip {
		t.Fatalf("tip %s, want %s", rev.tip, tip)
	}
	section := rev.text[strings.Index(rev.text, "Needs your sign-off"):]
	listed := acceptArgs(t, section)
	var want []string
	for p := range paths {
		want = append(want, p)
		if !strings.Contains(section, "  path: "+p+"\n") {
			t.Errorf("%q (rule %q) is not listed under \"Needs your sign-off\"", p, paths[p])
		}
	}
	sort.Strings(want)
	sort.Strings(listed)
	if strings.Join(want, "\n") != strings.Join(listed, "\n") {
		t.Fatalf("listed paths differ:\nwant %q\ngot  %q", want, listed)
	}
	if strings.Contains(section, "docs/plain.txt") {
		t.Fatalf("a plain file is listed:\n%s", section)
	}

	w.mustOK(w.sub("verify", w.verifyArgs(testRole, "config", "true")...))
	before := refLines(w.refs())

	// Each path alone is missing: refused, with that path named.
	for _, missing := range want {
		var signed []string
		for _, p := range want {
			if p != missing {
				signed = append(signed, p)
			}
		}
		code, out, errb := w.sub("integrate", w.integrateArgs(testRole, "config", "main", rev, signed...)...)
		if code != exitRefused || out != "" {
			t.Fatalf("without %q: exit %d\n%s\n%s", missing, code, out, errb)
		}
		if !strings.Contains(errb, "add "+acceptArg(missing)+"\n") {
			t.Fatalf("without %q the refusal does not say to add it:\n%s", missing, errb)
		}
		if n := strings.Count(errb, "add --accept-path"); n != 1 {
			t.Fatalf("without %q the refusal names %d paths:\n%s", missing, n, errb)
		}
	}
	if got := refLines(w.refs()); !equalMaps(got, before) {
		t.Fatalf("refused integrations changed refs")
	}

	// With every path signed it goes through.
	out := w.mustOK(w.sub("integrate", w.integrateArgs(testRole, "config", "main", rev, want...)...))
	if !strings.Contains(out, "Old: "+base) || !strings.Contains(out, "New: "+tip) {
		t.Fatalf("output:\n%s", out)
	}
	if got := refLines(w.refs())["refs/heads/main"]; got != tip {
		t.Fatalf("main is at %s, want %s", got, tip)
	}
}
