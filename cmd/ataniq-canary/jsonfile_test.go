//go:build linux && ataniq_canary

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// file runs a subcommand that takes --file and returns its status and outputs.
func file(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(context.Background(), args, &out, &errOut, func(string) string { return "" })
	return code, out.String(), errOut.String()
}

func writeJSON(t *testing.T, text string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "record.json")
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFieldsPrintsEveryValueAsOneSortedLine(t *testing.T) {
	p := writeJSON(t, `{"role":"throwaway-one/lead","epoch":18446744073709551615,
		"result":{"outcome":"uncertain","reason":"not-submitted","written":"partial","detail":"two\nlines \"quoted\""},
		"argv":["/opt/sample/agent","--flag"],"scratch":false,"gone":null,"title":"caf\u00e9","lead":"\"x"}`+"\n")
	code, out, errOut := file(t, "fields", "--file", p)
	if code != 0 || errOut != "" {
		t.Fatalf("status %d: %s", code, errOut)
	}
	want := strings.Join([]string{
		"argv.0=/opt/sample/agent",
		"argv.1=--flag",
		"epoch=18446744073709551615",
		"gone=null",
		`lead="\"x"`,
		`result.detail="two\nlines \"quoted\""`,
		"result.outcome=uncertain",
		"result.reason=not-submitted",
		"result.written=partial",
		"role=throwaway-one/lead",
		"scratch=false",
		`title="caf\u00e9"`,
	}, "\n") + "\n"
	if out != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
}

func TestFieldsRefusesWhatIsNotOnePlainObject(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(writeJSON(t, `{"a":1}`), link); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]string{
		"a list":               writeJSON(t, `[1]`),
		"null":                 writeJSON(t, `null`),
		"two objects":          writeJSON(t, `{"a":1}{"b":2}`),
		"text after":           writeJSON(t, `{"a":1} x`),
		"not json":             writeJSON(t, `a=1`),
		"a key with a dot":     writeJSON(t, `{"a.b":1}`),
		"a key with an equals": writeJSON(t, `{"a=b":1}`),
		"a key with a newline": writeJSON(t, `{"a\nb":1}`),
		"an empty key":         writeJSON(t, `{"":1}`),
		"a symlink":            link,
		"a folder":             dir,
		"an absent file":       filepath.Join(dir, "absent.json"),
	} {
		t.Run(name, func(t *testing.T) {
			code, out, errOut := file(t, "fields", "--file", p)
			if code != exitFailed || out != "" || errOut == "" {
				t.Fatalf("status %d, out %q, err %q", code, out, errOut)
			}
		})
	}
}

func TestScrubScreenReplacesTheTextByItsHashAndKeepsTheRest(t *testing.T) {
	const text = "login at sample.invalid/device code SAMPLE-CODE\nsecond row"
	quoted, err := json.Marshal(text)
	if err != nil {
		t.Fatal(err)
	}
	// The position is a number that does not survive a float.
	p := writeJSON(t, `{"version":1,"role":"throwaway-one/lead","epoch":"3","screen":{"cols":120,"rows":40,`+
		`"position":18446744073709551615,"text":`+string(quoted)+`,"truncated":true},"counters":{"output_bytes":77}}`)
	sum := sha256.Sum256([]byte(text))
	hash := "sha256:" + hex.EncodeToString(sum[:])

	code, out, errOut := file(t, "scrub-screen", "--file", p)
	if code != 0 || errOut != "" || out != "scrubbed: the screen text is replaced by "+hash+"\n" {
		t.Fatalf("status %d, out %q, err %q", code, out, errOut)
	}
	after, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(after, []byte("SAMPLE-CODE")) || bytes.Contains(after, []byte("second row")) {
		t.Fatalf("the screen text is still in the file:\n%s", after)
	}
	_, got, _ := file(t, "fields", "--file", p)
	want := strings.Join([]string{
		"counters.output_bytes=77", "epoch=3", "role=throwaway-one/lead",
		"screen.cols=120", "screen.position=18446744073709551615", "screen.rows=40",
		"screen.text=" + hash, "screen.truncated=true", "version=1",
	}, "\n") + "\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if st, err := os.Stat(p); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, %v", st.Mode(), err)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(p), ".scrub-*")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}

	// A second run changes nothing and says so.
	code, out, _ = file(t, "scrub-screen", "--file", p)
	again, _ := os.ReadFile(p)
	if code != 0 || out != "kept: the screen text is a hash already\n" || !bytes.Equal(after, again) {
		t.Fatalf("second run: status %d, out %q", code, out)
	}
}

func TestScrubScreenLeavesARecordWithoutScreenText(t *testing.T) {
	for name, text := range map[string]string{
		"no screen":       `{"version":1}`,
		"no text":         `{"screen":{"cols":1}}`,
		"text not a text": `{"screen":{"text":7}}`,
	} {
		t.Run(name, func(t *testing.T) {
			p := writeJSON(t, text)
			code, out, errOut := file(t, "scrub-screen", "--file", p)
			after, _ := os.ReadFile(p)
			if code != 0 || errOut != "" || out != "kept: the record holds no screen text\n" || string(after) != text {
				t.Fatalf("status %d, out %q, err %q, file %q", code, out, errOut, after)
			}
		})
	}
}

func TestScrubScreenRefusesStandardInputAndABadFile(t *testing.T) {
	for name, p := range map[string]string{
		"standard input": "-",
		"not json":       writeJSON(t, "x"),
		"an absent file": filepath.Join(t.TempDir(), "absent.json"),
	} {
		t.Run(name, func(t *testing.T) {
			if code, out, errOut := file(t, "scrub-screen", "--file", p); code != exitFailed || out != "" || errOut == "" {
				t.Fatalf("status %d, out %q, err %q", code, out, errOut)
			}
		})
	}
}

func TestUsageErrorsOfFieldsAndScrubScreen(t *testing.T) {
	for name, args := range map[string][]string{
		"fields without a file":         {"fields"},
		"fields with an empty path":     {"fields", "--file", ""},
		"fields with a config":          {"fields", "--config", "/opt/sample/c.json", "--file", "/opt/sample/f.json"},
		"fields with a further word":    {"fields", "--file", "/opt/sample/f.json", "extra"},
		"scrub-screen without a file":   {"scrub-screen"},
		"scrub-screen with an argument": {"scrub-screen", "/opt/sample/f.json"},
	} {
		t.Run(name, func(t *testing.T) {
			code, out, errOut := file(t, args...)
			if code != exitUsage || out != "" || !strings.Contains(errOut, "usage:") {
				t.Fatalf("status %d, out %q, err %q", code, out, errOut)
			}
		})
	}
}
