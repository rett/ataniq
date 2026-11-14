//go:build linux && amd64

package qualification

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestR1StrictSchemas(t *testing.T) {
	f := newR1Fixture(t)
	if err := runR1(f.d); err != nil {
		t.Fatal(err)
	}
	inputs := []struct {
		name   string
		data   []byte
		decode func([]byte) error
	}{
		{"request", f.request, func(b []byte) error { _, e := DecodeR1Request(b); return e }},
		{"context", f.store.files[R1ContextName].Data, func(b []byte) error { _, e := DecodeR1Context(b); return e }},
		{"release", f.release, func(b []byte) error { _, e := DecodeR1Release(b); return e }},
		{"finish", f.finish, func(b []byte) error { _, e := DecodeR1Finish(b); return e }},
		{"handshake", r1JSON(r1Handshake{1, "observed", &f.child.id}), func(b []byte) error { _, e := r1DecodeHandshake(b); return e }},
		{"startup", r1JSON(R1StartupFailure{1, "r1-startup-failure", "context", "control", "closed"}), func(b []byte) error { _, e := DecodeR1StartupFailure(b); return e }},
	}
	for _, e := range f.frames {
		inputs = append(inputs, struct {
			name   string
			data   []byte
			decode func([]byte) error
		}{e.Stage, r1JSON(e), func(b []byte) error { _, e := DecodeR1Evidence(b); return e }})
	}
	failed := newR1Fixture(t)
	failed.child.readyErr = io.ErrUnexpectedEOF
	if runR1(failed.d) == nil {
		t.Fatal("failed handshake accepted")
	}
	inputs = append(inputs, struct {
		name   string
		data   []byte
		decode func([]byte) error
	}{"failure", r1JSON(failed.frames[len(failed.frames)-1]), func(b []byte) error { _, e := DecodeR1Evidence(b); return e }})
	for _, tc := range inputs {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.decode(tc.data); err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(tc.data, &fields)
			for key, value := range fields {
				for _, mutation := range []string{"null", "alias", "duplicate"} {
					t.Run(key+"/"+mutation, func(t *testing.T) {
						m := map[string]json.RawMessage{}
						for k, v := range fields {
							m[k] = v
						}
						var b []byte
						switch mutation {
						case "null":
							m[key] = json.RawMessage("null")
							b = r1JSON(m)
						case "alias":
							delete(m, key)
							m[strings.ToUpper(key)] = value
							b = r1JSON(m)
						case "duplicate":
							b = bytes.TrimSpace(tc.data)
							b = append(append(append([]byte{}, b[:len(b)-1]...), ','), append(append(r1JSON(key), ':'), append(value, '}')...)...)
						}
						if tc.decode(b) == nil {
							t.Fatal("strict rejection absent", string(b))
						}
					})
				}
				// Every present top-level field here is required (payload presence is
				// selected by stage). No omission may silently default zero/false.
				m := map[string]json.RawMessage{}
				for k, v := range fields {
					if k != key {
						m[k] = v
					}
				}
				if tc.decode(r1JSON(m)) == nil {
					t.Fatal("missing accepted", key)
				}
			}
			for _, suffix := range []string{` ,"unknown":1}`, ` ,"Version":1}`, ` ,"\u0056ersion":1}`, ` ,"path":"elsewhere"}`, ` ,"max_bytes":1}`} {
				b := bytes.TrimSpace(tc.data)
				b = append(append([]byte{}, b[:len(b)-1]...), suffix...)
				if tc.decode(b) == nil {
					t.Fatal("unexpected field", string(b))
				}
			}
			for _, b := range [][]byte{append(append([]byte{}, tc.data...), []byte("{}")...), append(append([]byte{}, tc.data...), bytes.Repeat([]byte{' '}, R1MaxFrame)...), []byte("null"), []byte("[]"), []byte{0xff}} {
				if tc.decode(b) == nil {
					t.Fatal("bad frame accepted")
				}
			}
		})
	}
}

func TestR1NestedRequiredAndFixedValues(t *testing.T) {
	f := newR1Fixture(t)
	context := f.store.files[R1ContextName].Data
	for _, bad := range []string{`"control_directory":{}`, `"control_directory":{"device":1}`, `"control_directory":{"device":1,"inode":0}`, `"control_directory":{"device":1,"inode":2,"extra":0}`} {
		start := bytes.Index(context, []byte(`"control_directory"`))
		b := append(append([]byte{}, context[:start]...), bad...)
		b = append(b, '}')
		if _, err := DecodeR1Context(b); err == nil {
			t.Fatal(string(b))
		}
	}
	for _, key := range []string{"profile", "challenge", "run_id", "attempt_id"} {
		var m map[string]any
		_ = json.Unmarshal(f.request, &m)
		m[key] = "UPPER-invalid"
		if _, err := DecodeR1Request(r1JSON(m)); err == nil {
			t.Fatal(key)
		}
	}
	for _, key := range []string{"plan_sha256", "max_bytes", "max_duration_ms", "command", "control_root"} {
		var m map[string]any
		_ = json.Unmarshal(f.request, &m)
		m[key] = "forbidden"
		if _, err := DecodeR1Request(r1JSON(m)); err == nil {
			t.Fatal(key)
		}
	}
	if _, err := DecodeR1Finish(f.release); err == nil {
		t.Fatal("release accepted as finish")
	}
	if _, err := DecodeR1Release(f.finish); err == nil {
		t.Fatal("finish accepted as release")
	}
	if err := runR1(f.d); err != nil {
		t.Fatal(err)
	}
	e := f.frames[2]
	e.Touched.RequestedBytes--
	if e.valid() {
		t.Fatal("variable memory cap")
	}
	e.Touched.RequestedBytes++
	e.Touched.TouchedBytes--
	if e.valid() {
		t.Fatal("incomplete touch accepted as retained")
	}
	c := f.frames[3]
	c.Completion.Child.Reap = "unknown"
	if c.valid() {
		t.Fatal("future reap accepted")
	}
	var out bytes.Buffer
	if err := EncodeR1Evidence(&out, c); err == nil || out.Len() != 0 {
		t.Fatal("invalid evidence caused partial write")
	}
}

type r1ShortWriter struct{}

func (r1ShortWriter) Write(b []byte) (int, error) { return len(b) - 1, nil }
func TestR1EncoderShortWriteAndLegacyWire(t *testing.T) {
	f := newR1Fixture(t)
	if err := runR1(f.d); err != nil {
		t.Fatal(err)
	}
	if err := EncodeR1Evidence(r1ShortWriter{}, f.frames[0]); err != io.ErrShortWrite {
		t.Fatal(err)
	}
	legacy := `{"version":1,"run_id":"01234567","request_id":"89abcdef","case":"R1","max_duration_ms":100,"max_bytes":4096}`
	r, err := DecodeRequest(strings.NewReader(legacy))
	if err != nil {
		t.Fatal(err)
	}
	if string(r1JSON(r)) != legacy {
		t.Fatal("legacy request encoding changed")
	}
	if o := Probe(r, Config{}); o.Status != "unsupported" {
		t.Fatal(o)
	}
	h := HoldFrame{Version: 1, RunID: "01234567", RequestID: "89abcdef", Kind: "ready", PID: 10, Start: 100, Executable: FileIdentity{Device: 1, Inode: 3}, PIDNamespace: FileIdentity{Device: 1, Inode: 4}}
	want := `{"version":1,"run_id":"01234567","request_id":"89abcdef","kind":"ready","pid":10,"start":100,"executable":{"device":1,"inode":3,"uid":0},"pid_namespace":{"device":1,"inode":4,"uid":0}}` + "\n"
	var out bytes.Buffer
	if err := emitHold(&out, h); err != nil || out.String() != want {
		t.Fatal("hold wire changed", err, out.String())
	}
	config := HostConfig{Version: 1, Campaign: "/campaign", Driver: "/driver", Probe: "/probe", Bwrap: "/bwrap", BPF: "/bpf", Systemctl: "/systemctl", SystemdRun: "/systemd-run", Role: "direct", RoleID: "01234567", RunID: "01234567", RequestID: "89abcdef", ReviewedBPF: true}
	for _, name := range []string{"R1", R1Profile, "r1-memory"} {
		config.Case = name
		if err := config.validate(); err == nil || err.Error() != "unsupported host case" {
			t.Fatal("R1 host launch admitted", err)
		}
	}
}
