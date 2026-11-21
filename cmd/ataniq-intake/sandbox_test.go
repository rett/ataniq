package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/exchange/boundedexec"
	"ataniq/internal/exchange/sandboxplan"
	"ataniq/internal/exchange/sandboxplan/plantest"
)

// scripts writes the plain programs that a template names, and a marker
// file that the upload-pack program creates when it runs. It returns the
// paths.
type scripts struct{ upload, measure, launcher, marker string }

func writeScripts(t *testing.T, w *world) scripts {
	t.Helper()
	up, err := plantest.GitUploadPack()
	if err != nil {
		t.Fatal(err)
	}
	s := scripts{
		upload:   filepath.Join(w.other, "upload.sh"),
		measure:  filepath.Join(w.other, "measure.sh"),
		launcher: filepath.Join(w.other, "launch.sh"),
		marker:   filepath.Join(w.other, "upload-ran"),
	}
	write := func(path, body string) {
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// The template passes the lead's clone as the first argument; git adds
	// the repository as the last one, which this program does not need.
	write(s.upload, "echo ran > '"+s.marker+"'\ntree=$1\nexec '"+strings.Join(up, "' '")+"' \"$tree\"\n")
	write(s.measure, "echo '1000 /work'\n")
	write(s.launcher, "exit 0\n")
	return s
}

// template writes a template file with the sections given. A section that is
// false is left out.
func writeTemplate(t *testing.T, w *world, s scripts, serve, verifySection bool) string {
	t.Helper()
	doc := map[string]any{"version": 1}
	if serve {
		doc["serve"] = map[string]any{
			"upload_pack": []string{s.upload, "{tree}"},
			"repository":  "/work/{project}",
			"measure":     []string{s.measure},
			"measure_env": []string{"PATH=/usr/bin:/bin"},
		}
	}
	if verifySection {
		doc["verify"] = map[string]any{
			"prefix":  []string{s.launcher, "{mirror}", "{scratch}", "{home}"},
			"env":     []string{"PATH=/usr/bin:/bin"},
			"mirror":  "/ataniq/mirror/{project}.git",
			"scratch": "/work/verify",
			"home":    "/home/verify",
		}
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(w.other, "template.json")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// withRealLoader makes the world use the loader of the real command.
func (w *world) withRealLoader() { w.loader = loadTemplate }

func TestFetchWithoutSandboxFlagStartsNothing(t *testing.T) {
	w := newWorld(t)
	w.initMirror()
	w.lead(testRole)
	before := w.refs()
	code, out, errb := w.sub("fetch", "--role", testRole)
	if code != exitUsage || out != "" || !strings.Contains(errb, "--sandbox is required") {
		t.Fatalf("exit %d\n%s\n%s", code, out, errb)
	}
	if len(w.loads) != 0 || len(w.planner.ServeRequests()) != 0 {
		t.Fatalf("something was asked for: %v %v", w.loads, w.planner.ServeRequests())
	}
	if w.refs() != before {
		t.Fatal("refs changed")
	}
}

func TestVerifyWithoutSandboxFlagStartsNothing(t *testing.T) {
	w := newWorld(t)
	w.runner = &fakeRunner{}
	w.initMirror()
	w.lead(testRole)
	w.fetch(testRole)
	code, _, errb := w.sub("verify", "--role", testRole, "--branch", "main", "--gate", "a=true",
		"--records", w.records, "--work", w.work)
	if code != exitUsage || !strings.Contains(errb, "--sandbox is required") {
		t.Fatalf("exit %d\n%s", code, errb)
	}
	if len(w.planner.VerifyRequests()) != 0 || w.runner.(*fakeRunner).calls != 0 {
		t.Fatal("a verification was planned or run")
	}
}

func TestFetchRefusesWithoutAUsableTemplate(t *testing.T) {
	w := newWorld(t)
	w.withRealLoader()
	s := writeScripts(t, w)
	w.initMirror()
	w.lead(testRole)
	before := w.refs()

	missing := filepath.Join(w.other, "no-such-template.json")
	huge := filepath.Join(w.other, "huge.json")
	if err := os.WriteFile(huge, []byte(strings.Repeat(" ", sandboxplan.MaxTemplateBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	junk := filepath.Join(w.other, "junk.json")
	if err := os.WriteFile(junk, []byte(`{"version":1,"serve":{"upload_pack":["relative"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	atBracket := filepath.Join(w.other, "at-bracket.json")
	if err := os.WriteFile(atBracket, []byte(`{"version":1,"serve":{"upload_pack":["/sb"],"repository":"/work/a@[b]c","measure":["/du"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	real := writeTemplate(t, w, s, true, true)
	link := filepath.Join(w.other, "link.json")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	verifyOnly := writeTemplate(t, w, s, false, true)

	for name, path := range map[string]string{
		"missing":          missing,
		"too big":          huge,
		"invalid":          junk,
		"at-bracket path":  atBracket,
		"a symlink":        link,
		"without serve":    verifyOnly,
		"relative missing": "no-such-template.json",
	} {
		code, out, errb := w.sub("fetch", "--role", testRole, "--sandbox", path)
		if code != exitRefused || out != "" {
			t.Fatalf("%s: exit %d\n%s\n%s", name, code, out, errb)
		}
		noControl(t, name, errb)
		if path == atBracket && !strings.Contains(errb, `nothing was run: sandbox template: serve: repository: path holds "@["`) {
			t.Fatalf("%s: the refusal does not say why:\n%s", name, errb)
		}
	}
	if _, err := os.Stat(s.marker); err == nil {
		t.Fatal("the upload-pack program ran")
	}
	if w.refs() != before {
		t.Fatal("refs changed")
	}
}

func TestFetchThroughATemplate(t *testing.T) {
	w := newWorld(t)
	w.withRealLoader()
	s := writeScripts(t, w)
	w.initMirror()
	base := w.lead(testRole)
	path := writeTemplate(t, w, s, true, false)
	out := w.mustOK(w.sub("fetch", "--role", testRole, "--sandbox", path, "--timeout", "2m",
		"--max-source", "100000", "--max-staged", "10000000", "--max-mirror", "100000000"))
	if !strings.Contains(out, "created refs/leads/lead/heads/main "+base) || !strings.Contains(out, "1000 bytes") {
		t.Fatalf("output:\n%s", out)
	}
	if _, err := os.Stat(s.marker); err != nil {
		t.Fatalf("the template's upload-pack program did not run: %v", err)
	}
	// The template has no verify section: verify refuses and runs nothing.
	fr := &fakeRunner{}
	w.runner = fr
	args := w.verifyArgs(testRole, "main", "true")
	for i, a := range args {
		if a == "--sandbox" {
			args[i+1] = path
		}
	}
	code, _, errb := w.sub("verify", args...)
	if code != exitRefused || !strings.Contains(errb, "no sandbox configured") || fr.calls != 0 {
		t.Fatalf("exit %d, %d runs\n%s", code, fr.calls, errb)
	}
	if es, _ := os.ReadDir(w.work); len(es) != 0 {
		t.Fatalf("the work folder is not empty: %v", es)
	}
	if recs, err := w.store().Verifications(base); err != nil || len(recs) != 0 {
		t.Fatalf("records: %v %v", recs, err)
	}
}

func TestVerifyThroughATemplate(t *testing.T) {
	w := newWorld(t)
	w.withRealLoader()
	s := writeScripts(t, w)
	fr := &fakeRunner{res: boundedexec.Result{Stdout: []byte("ok\n")}}
	w.runner = fr
	w.initMirror()
	w.lead(testRole)
	path := writeTemplate(t, w, s, true, true)
	w.mustOK(w.sub("fetch", "--role", testRole, "--sandbox", path))
	args := w.verifyArgs(testRole, "main", "true")
	for i, a := range args {
		if a == "--sandbox" {
			args[i+1] = path
		}
	}
	out := w.mustOK(w.sub("verify", args...))
	if !strings.Contains(out, "Outcome: pass") || fr.calls != 1 {
		t.Fatalf("calls %d\n%s", fr.calls, out)
	}
}

func TestTemplateInsideTheStateRootIsRefused(t *testing.T) {
	w := newWorld(t)
	w.initMirror()
	w.lead(testRole)
	inside := filepath.Join(w.state, "template.json")
	if err := os.WriteFile(inside, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, errb := w.sub("fetch", "--role", testRole, "--sandbox", inside)
	if code != exitUsage || !strings.Contains(errb, "state root") {
		t.Fatalf("exit %d\n%s", code, errb)
	}
	if len(w.loads) != 0 {
		t.Fatal("the template was loaded")
	}
}

func TestFetchLimitIsARefusal(t *testing.T) {
	w := newWorld(t)
	w.initMirror()
	w.lead(testRole)
	before := w.refs()
	code, out, errb := w.sub("fetch", "--role", testRole, "--sandbox", w.template(), "--max-source", "10")
	if code != exitRefused || out != "" || !strings.Contains(errb, "limit") {
		t.Fatalf("exit %d\n%s\n%s", code, out, errb)
	}
	if w.refs() != before {
		t.Fatal("refs changed")
	}
}

func TestFetchOfAMissingCloneIsRefused(t *testing.T) {
	w := newWorld(t)
	w.initMirror()
	code, out, errb := w.sub("fetch", "--role", "nobody", "--sandbox", w.template())
	if code == exitOK || out != "" || errb == "" {
		t.Fatalf("exit %d\n%s\n%s", code, out, errb)
	}
}
