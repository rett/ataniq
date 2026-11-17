package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/exchange/sandboxplan/plantest"
)

// The tests call run with arguments and files of their own. The template they
// write starts the host's git-upload-pack directly: there is no sandbox and no
// namespace, and nothing here qualifies anything.

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// fixture writes a config and a template, and returns the paths.
type paths struct{ dir, config, template, root, out string }

func newPaths(t *testing.T, ack string) paths {
	t.Helper()
	p := paths{dir: t.TempDir()}
	p.config = filepath.Join(p.dir, "config.json")
	p.template = filepath.Join(p.dir, "template.json")
	p.root = filepath.Join(p.dir, "scratch")
	p.out = filepath.Join(p.dir, "report.json")
	gup, err := plantest.GitUploadPack()
	if err != nil {
		t.Fatal(err)
	}
	// The template has no verify section. The upload-pack argv runs the
	// host's git-upload-pack on {tree}; git's own repository argument, the
	// inside path, is ignored.
	write(t, p.template, fmt.Sprintf(`{"version": 1, "serve": {
  "upload_pack": ["/bin/sh", "-c", "exec %s \"$1\"", "sh", "{tree}"],
  "repository": "/work/{project}",
  "measure": ["/bin/sh", "-c", "echo 1000"],
  "measure_env": ["PATH=/usr/bin:/bin"]}}`, gup[0]))
	write(t, p.config, fmt.Sprintf(`{"state_root": %q, "template": %q, "role": "coder", "project": "proj"%s}`, p.root, p.template, ack))
	return p
}

func call(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(context.Background(), args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestRunWritesFacts(t *testing.T) {
	p := newPaths(t, `, "i_run_this_on_purpose": true`)
	code, stdout, stderr := call(t, "--config", p.config, "--out", p.out)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "facts only") {
		t.Errorf("stdout: %q", stdout)
	}
	fi, err := os.Stat(p.out)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("report file: %v, %v", fi, err)
	}
	data, err := os.ReadFile(p.out)
	if err != nil {
		t.Fatal(err)
	}
	var rep struct {
		Format   int `json:"format"`
		Template struct {
			SHA256 string `json:"sha256"`
		} `json:"template"`
		Tools struct{ Git, Go string } `json:"tools"`
		Cases []struct {
			ID    string `json:"id"`
			Facts []struct {
				ID string `json:"id"`
			} `json:"facts"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Format != 1 || len(rep.Template.SHA256) != 64 || rep.Tools.Git == "" || rep.Tools.Go == "" || len(rep.Cases) != 5 {
		t.Errorf("report: %+v", rep)
	}
	// The template has no verify section: one fact says so.
	last := rep.Cases[len(rep.Cases)-1]
	if last.ID != "verification" || len(last.Facts) != 1 || last.Facts[0].ID != "verification.no-verify-section" {
		t.Errorf("verification case: %+v", last)
	}
	if strings.Contains(string(data), p.dir) {
		t.Errorf("the report names the scratch path")
	}
}

// TestInterruptedRunWritesTheReportSoFar: a run that the context ends writes
// the partial report, then exits 1.
func TestInterruptedRunWritesTheReportSoFar(t *testing.T) {
	p := newPaths(t, `, "i_run_this_on_purpose": true`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, errb bytes.Buffer
	code := run(ctx, []string{"--config", p.config, "--out", p.out}, &out, &errb)
	if code != exitFailed {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "did not finish") || strings.Contains(out.String(), "report written") {
		t.Errorf("stdout %q, stderr %q", out.String(), errb.String())
	}
	data, err := os.ReadFile(p.out)
	if err != nil {
		t.Fatalf("no report after an interrupted run: %v", err)
	}
	var rep struct {
		Format int `json:"format"`
		Role   string
	}
	if err := json.Unmarshal(data, &rep); err != nil || rep.Format != 1 {
		t.Errorf("report: %v, %+v", err, rep)
	}
}

func TestRunRefusals(t *testing.T) {
	ack := `, "i_run_this_on_purpose": true`
	t.Run("no arguments", func(t *testing.T) {
		if code, _, stderr := call(t); code != exitRefused || !strings.Contains(stderr, "usage") {
			t.Errorf("exit %d: %s", code, stderr)
		}
	})
	t.Run("no config", func(t *testing.T) {
		p := newPaths(t, ack)
		if code, _, _ := call(t, "--out", p.out); code != exitRefused {
			t.Errorf("exit %d", code)
		}
	})
	t.Run("no out", func(t *testing.T) {
		p := newPaths(t, ack)
		if code, _, _ := call(t, "--config", p.config); code != exitRefused {
			t.Errorf("exit %d", code)
		}
	})
	t.Run("extra argument", func(t *testing.T) {
		p := newPaths(t, ack)
		if code, _, _ := call(t, "--config", p.config, "--out", p.out, "more"); code != exitRefused {
			t.Errorf("exit %d", code)
		}
	})
	t.Run("missing config file", func(t *testing.T) {
		p := newPaths(t, ack)
		if code, _, _ := call(t, "--config", filepath.Join(p.dir, "none.json"), "--out", p.out); code != exitRefused {
			t.Errorf("exit %d", code)
		}
	})
	t.Run("no acknowledgement", func(t *testing.T) {
		p := newPaths(t, "")
		code, _, stderr := call(t, "--config", p.config, "--out", p.out)
		if code != exitRefused || !strings.Contains(stderr, "i_run_this_on_purpose") {
			t.Errorf("exit %d: %s", code, stderr)
		}
		if _, err := os.Lstat(p.root); err == nil {
			t.Error("the scratch root was made")
		}
	})
	t.Run("missing template", func(t *testing.T) {
		p := newPaths(t, ack)
		if err := os.Remove(p.template); err != nil {
			t.Fatal(err)
		}
		code, _, stderr := call(t, "--config", p.config, "--out", p.out)
		if code != exitRefused || !strings.Contains(stderr, "template") {
			t.Errorf("exit %d: %s", code, stderr)
		}
		if _, err := os.Lstat(p.root); err == nil {
			t.Error("the scratch root was made")
		}
	})
	t.Run("a template that does not parse", func(t *testing.T) {
		p := newPaths(t, ack)
		write(t, p.template, `{"version": 2}`)
		if code, _, _ := call(t, "--config", p.config, "--out", p.out); code != exitRefused {
			t.Errorf("exit %d", code)
		}
	})
	t.Run("scratch root holds something", func(t *testing.T) {
		p := newPaths(t, ack)
		if err := os.Mkdir(p.root, 0o700); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(p.root, "keep"), "x")
		if code, _, _ := call(t, "--config", p.config, "--out", p.out); code != exitRefused {
			t.Errorf("exit %d", code)
		}
		if _, err := os.Lstat(p.out); err == nil {
			t.Error("a report was written")
		}
	})
	t.Run("output exists", func(t *testing.T) {
		p := newPaths(t, ack)
		write(t, p.out, "keep")
		if code, _, _ := call(t, "--config", p.config, "--out", p.out); code != exitRefused {
			t.Errorf("exit %d", code)
		}
		if b, _ := os.ReadFile(p.out); string(b) != "keep" {
			t.Errorf("the output file was changed: %q", b)
		}
		if _, err := os.Lstat(p.root); err == nil {
			t.Error("the scratch root was made")
		}
	})
	t.Run("output inside the scratch root", func(t *testing.T) {
		p := newPaths(t, ack)
		if code, _, _ := call(t, "--config", p.config, "--out", filepath.Join(p.root, "report.json")); code != exitRefused {
			t.Errorf("exit %d", code)
		}
	})
	t.Run("output folder is a link into the scratch root", func(t *testing.T) {
		p := newPaths(t, ack)
		if err := os.Mkdir(p.root, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(p.dir, "link")
		if err := os.Symlink(p.root, link); err != nil {
			t.Fatal(err)
		}
		if code, _, stderr := call(t, "--config", p.config, "--out", filepath.Join(link, "report.json")); code != exitRefused || !strings.Contains(stderr, "scratch state root") {
			t.Errorf("exit %d: %s", code, stderr)
		}
		if entries, _ := os.ReadDir(p.root); len(entries) != 0 {
			t.Errorf("the scratch root holds %d entries", len(entries))
		}
	})
	t.Run("scratch root reached through a link", func(t *testing.T) {
		p := newPaths(t, ack)
		real := filepath.Join(p.dir, "real")
		if err := os.Mkdir(real, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(p.dir, "link")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		// The config names the root below the link; the output folder is the
		// real folder of the same place.
		write(t, p.config, fmt.Sprintf(`{"state_root": %q, "template": %q, "role": "coder", "project": "proj", "i_run_this_on_purpose": true}`, filepath.Join(link, "scratch"), p.template))
		if err := os.Mkdir(filepath.Join(real, "scratch"), 0o700); err != nil {
			t.Fatal(err)
		}
		if code, _, _ := call(t, "--config", p.config, "--out", filepath.Join(real, "scratch", "report.json")); code != exitRefused {
			t.Errorf("exit %d", code)
		}
	})
	t.Run("output folder missing", func(t *testing.T) {
		p := newPaths(t, ack)
		if code, _, _ := call(t, "--config", p.config, "--out", filepath.Join(p.dir, "no", "report.json")); code != exitRefused {
			t.Errorf("exit %d", code)
		}
	})
}
