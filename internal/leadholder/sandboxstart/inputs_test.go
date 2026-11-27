//go:build linux && ataniq_canary

package sandboxstart

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

// writeInputs writes bytes to a new file with an exact mode and returns its
// path.
func writeInputs(t *testing.T, data []byte, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(realpath(t, t.TempDir()), "inputs.json")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func sampleInputsJSON(t *testing.T) (Inputs, []byte) {
	t.Helper()
	in := sampleInputs(realpath(t, t.TempDir()))
	in.AuthorityTag = "0123456789abcdef"
	data, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return in, data
}

func wantInputsError(t *testing.T, err error, substr string) {
	t.Helper()
	var ie *InputsError
	if !errors.As(err, &ie) {
		t.Fatalf("error %v is no *InputsError", err)
	}
	if !strings.Contains(err.Error(), substr) {
		t.Fatalf("error %q does not mention %q", err, substr)
	}
}

// TestReadInputsRoundTrip reads what a writer marshals, and shows that the
// fields the holder fills are not in the file.
func TestReadInputsRoundTrip(t *testing.T) {
	in, data := sampleInputsJSON(t)
	for _, key := range []string{"authority_tag", "AuthorityTag", "agent_argv", "agent_env", "argv", "env", "expected_digest", "holder_paths", "AgentArgv", "ExpectedDigest", "HolderPaths", "AgentEnv", "no_lead_has_run", "previous_instance_stopped", "NoLeadHasRun", "PreviousInstanceStopped"} {
		if strings.Contains(string(data), `"`+key+`"`) {
			t.Errorf("the file holds the holder's field %s", key)
		}
	}
	got, err := ReadInputs(writeInputs(t, data, 0o600))
	if err != nil {
		t.Fatal(err)
	}
	// The file carries everything but the fields the holder fills.
	want := in
	want.AuthorityTag = ""
	want.AgentArgv, want.AgentEnv, want.ExpectedDigest, want.HolderPaths = nil, nil, "", nil
	want.NoLeadHasRun, want.PreviousInstanceStopped = false, false
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("read %+v\nwant %+v", got, want)
	}
}

// TestReadInputsFileRules checks what is asked of the file itself.
func TestReadInputsFileRules(t *testing.T) {
	_, data := sampleInputsJSON(t)
	t.Run("mode 0644", func(t *testing.T) {
		_, err := ReadInputs(writeInputs(t, data, 0o644))
		wantInputsError(t, err, "mode 0644")
	})
	t.Run("mode 0400", func(t *testing.T) {
		_, err := ReadInputs(writeInputs(t, data, 0o400))
		wantInputsError(t, err, "mode 0400")
	})
	t.Run("a symbolic link", func(t *testing.T) {
		p := writeInputs(t, data, 0o600)
		l := p + ".link"
		if err := os.Symlink(p, l); err != nil {
			t.Fatal(err)
		}
		_, err := ReadInputs(l)
		wantInputsError(t, err, "cannot open")
		if !errors.Is(err, syscall.ELOOP) && !strings.Contains(err.Error(), "symbolic links") {
			t.Errorf("the refusal is not about the link: %v", err)
		}
	})
	t.Run("a folder", func(t *testing.T) {
		d := realpath(t, t.TempDir())
		if err := os.Chmod(d, 0o600); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(d, 0o700)
		_, err := ReadInputs(d)
		wantInputsError(t, err, "not a regular file")
	})
	t.Run("a FIFO does not block", func(t *testing.T) {
		p := filepath.Join(realpath(t, t.TempDir()), "fifo")
		if err := syscall.Mkfifo(p, 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := ReadInputs(p)
		wantInputsError(t, err, "not a regular file")
	})
	t.Run("missing", func(t *testing.T) {
		_, err := ReadInputs(filepath.Join(t.TempDir(), "nothing"))
		wantInputsError(t, err, "cannot open")
	})
	t.Run("too large", func(t *testing.T) {
		big := append([]byte(`{"version":1,"x":"`), make([]byte, MaxInputsBytes)...)
		_, err := ReadInputs(writeInputs(t, big, 0o600))
		wantInputsError(t, err, "larger than")
	})
	t.Run("not UTF-8", func(t *testing.T) {
		_, err := parseInputs(append([]byte(`{"version":1,"project":"`), 0xff, 0xfe, '"', '}'))
		wantInputsError(t, err, "UTF-8")
	})
}

// TestReadInputsIsStrict checks the decoding: an unknown key, a key in another
// case, a key twice, data after the document, the wrong types and the values the
// file alone decides.
func TestReadInputsIsStrict(t *testing.T) {
	_, data := sampleInputsJSON(t)
	text := string(data)
	mutate := func(from, to string) []byte {
		t.Helper()
		if !strings.Contains(text, from) {
			t.Fatalf("the sample has no %q", from)
		}
		return []byte(strings.Replace(text, from, to, 1))
	}
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"unknown key", mutate(`"canary": true,`, `"canary": true, "extra": 1,`), "unknown key"},
		{"unknown nested key", mutate(`"term": "xterm-256color"`, `"term": "xterm-256color", "rows": 24`), "unknown key"},
		{"a field of the holder", mutate(`"canary": true,`, `"canary": true, "agent_argv": ["/x"],`), "unknown key"},
		{"authority tag from file", mutate(`"canary": true,`, `"canary": true, "authority_tag": "0123456789abcdef",`), "unknown key"},
		{"another case", mutate(`"project":`, `"Project":`), "unknown key"},
		{"a key twice", mutate(`"canary": true,`, `"canary": true, "canary": false,`), "twice"},
		{"trailing data", append(append([]byte(nil), data...), []byte(` {}`)...), "after the inputs"},
		{"not JSON", []byte(`nope`), "not JSON"},
		{"a list", []byte(`[]`), "a list"},
		{"wrong type", mutate(`"canary": true`, `"canary": "yes"`), "not an inputs file"},
		{"wrong version", mutate(`"version": 1`, `"version": 2`), "version"},
		{"missing project", mutate(`"project": "throwaway-one"`, `"project": ""`), "project"},
		{"a relative path", mutate(`"state_root": "`, `"state_root": "rel/`), "state_root"},
		{"an unclean path", mutate(`"runtime_root": "`, `"runtime_root": "/./`), "runtime_root"},
		{"nested roots", nestedRoots(t, text), "runtime_root"},
		{"two executables the same", sameExecutables(t, text), "executables"},
		{"an unknown runtime", mutate(`"runtime": "`, `"runtime": "x`), "runtime"},
		{"an unknown identity", mutate(`"canary": true,`, `"canary": true, "identity": "wheel",`), "identity"},
		{"an owner for the root identity", mutate(`"canary": true,`, `"canary": true, "owner_user": "someone",`), "owner_user"},
		{"no time limit", mutate(`"time_limit_seconds": 5`, `"time_limit_seconds": 0`), "time_limit_seconds"},
		{"a time limit too long", mutate(`"time_limit_seconds": 5`, `"time_limit_seconds": 3601`), "time_limit_seconds"},
		{"no provider host", mutate(`"provider_host": "provider.example.test"`, `"provider_host": ""`), "provider_host"},
		{"an unreviewed filter", mutate(`"managed_marker"`, `"seccomp": {"file": "/f", "sha256": "`+strings.Repeat("0", 64)+`"}, "managed_marker"`), "reviewed_bpf"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ReadInputs(writeInputs(t, c.data, 0o600))
			if err == nil {
				t.Fatal("accepted")
			}
			wantInputsError(t, err, c.want)
		})
	}
}

func nestedRoots(t *testing.T, text string) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		t.Fatal(err)
	}
	m["runtime_root"] = m["state_root"].(string) + "/run"
	out, _ := json.Marshal(m)
	return out
}

func sameExecutables(t *testing.T, text string) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		t.Fatal(err)
	}
	e := m["executables"].(map[string]any)
	e["bwrap"] = e["pasta"]
	out, _ := json.Marshal(m)
	return out
}

// TestInputsDocumentEveryKey checks the documentation names every key of the
// file: other tasks write the file from it.
func TestInputsDocumentEveryKey(t *testing.T) {
	src, err := os.ReadFile("inputs.go")
	if err != nil {
		t.Fatal(err)
	}
	var comments []string
	for _, line := range strings.Split(string(src), "\n") {
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "//") {
			comments = append(comments, t)
		}
	}
	text := strings.Join(comments, "\n")
	var keys func(rt reflect.Type, prefix string) []string
	keys = func(rt reflect.Type, prefix string) []string {
		var out []string
		for _, f := range reflect.VisibleFields(rt) {
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "" || name == "-" {
				continue
			}
			out = append(out, name)
			ft := f.Type
			for ft.Kind() == reflect.Pointer || ft.Kind() == reflect.Slice {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				out = append(out, keys(ft, name+".")...)
			}
		}
		return out
	}
	for _, k := range keys(reflect.TypeFor[Inputs](), "") {
		leaf := k[strings.LastIndex(k, ".")+1:]
		if !strings.Contains(text, leaf) {
			t.Errorf("key %s is not documented in inputs.go", k)
		}
	}
}
