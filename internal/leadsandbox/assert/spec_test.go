package assert

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"ataniq/internal/leadsandbox/profile"
)

func TestSpecRoundTrip(t *testing.T) {
	for _, rt := range profile.Runtimes() {
		s := testSpec(rt)
		s.Optional = []int{4}
		s.ConnectTimeoutMS, s.ResolveTimeoutMS = 500, 2000
		data, err := EncodeSpec(s)
		if err != nil {
			t.Fatalf("%s: EncodeSpec: %v", rt, err)
		}
		back, err := DecodeSpec(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("%s: DecodeSpec: %v", rt, err)
		}
		again, _ := EncodeSpec(back)
		if !bytes.Equal(data, again) {
			t.Errorf("%s: round trip changed the spec", rt)
		}
		if back.ConnectTimeout().Milliseconds() != 500 || back.ResolveTimeout().Milliseconds() != 2000 {
			t.Errorf("timeouts %v %v", back.ConnectTimeout(), back.ResolveTimeout())
		}
		if !back.OptionalSet(4) || back.OptionalSet(5) {
			t.Errorf("OptionalSet wrong")
		}
	}
}

func TestSpecDefaultTimeouts(t *testing.T) {
	s := testSpec(profile.Claude)
	if s.ConnectTimeout().Milliseconds() != DefaultConnectMS || s.ResolveTimeout().Milliseconds() != DefaultResolveMS {
		t.Errorf("defaults %v %v", s.ConnectTimeout(), s.ResolveTimeout())
	}
}

func TestDecodeSpecStrict(t *testing.T) {
	good, err := EncodeSpec(testSpec(profile.Claude))
	if err != nil {
		t.Fatal(err)
	}
	withField := func(k, v string) []byte {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(good, &m); err != nil {
			t.Fatal(err)
		}
		m[k] = json.RawMessage(v)
		out, _ := json.Marshal(m)
		return out
	}
	for _, tc := range []struct {
		name  string
		input []byte
		field string
	}{
		{"unknown field", withField("sandbox_passed", "true"), "spec"},
		{"wrong version", withField("version", "2"), "version"},
		{"version as text", withField("version", `"1"`), "spec"},
		{"trailing data", append(append([]byte(nil), good...), []byte(`{}`)...), "spec"},
		{"not json", []byte("version: 1"), "spec"},
		{"empty", nil, "spec"},
		{"null", []byte("null"), "version"},
		{"array", []byte("[]"), "spec"},
		{"oversized", append(bytes.Repeat([]byte(" "), MaxSpecBytes+1), good...), "spec"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeSpecBytes(tc.input)
			if err == nil {
				t.Fatal("accepted")
			}
			if r := refusalOf(t, err); r.Field != tc.field {
				t.Errorf("field %q (%v), want %q", r.Field, err, tc.field)
			}
			// DecodeSpec reads at most one byte over the cap, and says so.
			if _, err := DecodeSpec(bytes.NewReader(tc.input)); err == nil {
				t.Error("DecodeSpec accepted")
			}
		})
	}
	if _, err := DecodeSpecBytes(good); err != nil {
		t.Errorf("good spec refused: %v", err)
	}
	// Exactly at the cap is read; one over is refused (padding with spaces).
	atCap := append(append([]byte(nil), good...), bytes.Repeat([]byte(" "), MaxSpecBytes-len(good))...)
	if _, err := DecodeSpec(bytes.NewReader(atCap)); err != nil {
		t.Errorf("a spec of exactly %d bytes was refused: %v", MaxSpecBytes, err)
	}
	if _, err := DecodeSpec(bytes.NewReader(append(atCap, ' '))); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("a spec one byte over the cap: %v", err)
	}
}

func TestEncodeSpecRefusesInvalid(t *testing.T) {
	s := testSpec(profile.Claude)
	s.Digest = "short"
	if _, err := EncodeSpec(s); err == nil {
		t.Error("an invalid spec was encoded")
	}
}

func TestSpecValidate(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	manyPaths := func(n int) []string {
		var out []string
		for i := 0; i < n; i++ {
			out = append(out, fmt.Sprintf("/x/%d", i))
		}
		return out
	}
	cases := []struct {
		name   string
		mutate func(s *Spec)
		field  string
	}{
		{"digest upper case", func(s *Spec) { s.Digest = strings.Repeat("AB", 32) }, "digest"},
		{"digest short", func(s *Spec) { s.Digest = "abc" }, "digest"},
		{"runtime", func(s *Spec) { s.Runtime = "gemini" }, "runtime"},
		{"no env", func(s *Spec) { s.Env = nil }, "env"},
		{"env too many", func(s *Spec) {
			s.Env = nil
			for i := 0; i < MaxEnvEntries+1; i++ {
				s.Env = append(s.Env, EnvEntry{fmt.Sprintf("V%d", i), "x"})
			}
		}, "env"},
		{"env name bad", func(s *Spec) { s.Env[0].Name = "1BAD" }, "env[0]"},
		{"env name empty", func(s *Spec) { s.Env[0].Name = "" }, "env[0]"},
		{"env name has equals", func(s *Spec) { s.Env[0].Name = "A=B" }, "env[0]"},
		{"env name long", func(s *Spec) { s.Env[0].Name = long(MaxEnvNameBytes + 1) }, "env[0]"},
		{"env value long", func(s *Spec) { s.Env[0].Value = long(MaxEnvValueBytes + 1) }, "env[0]"},
		{"env value newline", func(s *Spec) { s.Env[0].Value = "a\nb" }, "env[0]"},
		{"env duplicate", func(s *Spec) { s.Env[1].Name = s.Env[0].Name }, "env[1]"},
		{"holder paths empty", func(s *Spec) { s.HolderPaths = nil }, "holder_paths"},
		{"holder paths many", func(s *Spec) { s.HolderPaths = manyPaths(MaxPathEntries + 1) }, "holder_paths"},
		{"holder path relative", func(s *Spec) { s.HolderPaths[0] = "run/x" }, "holder_paths[0]"},
		{"holder path unclean", func(s *Spec) { s.HolderPaths[0] = "/run/../x" }, "holder_paths[0]"},
		{"holder path root", func(s *Spec) { s.HolderPaths[0] = "/" }, "holder_paths[0]"},
		{"holder path control", func(s *Spec) { s.HolderPaths[0] = "/run/\x01" }, "holder_paths[0]"},
		{"holder path long", func(s *Spec) { s.HolderPaths[0] = "/" + long(MaxPathBytes) }, "holder_paths[0]"},
		{"owner paths empty", func(s *Spec) { s.OwnerPaths = []string{} }, "owner_paths"},
		{"owner paths many", func(s *Spec) { s.OwnerPaths = manyPaths(MaxPathEntries + 1) }, "owner_paths"},
		{"owner path relative", func(s *Spec) { s.OwnerPaths[2] = "~/.ssh" }, "owner_paths[2]"},
		{"substrings empty", func(s *Spec) { s.MountSubstrings = nil }, "mount_substrings"},
		{"substrings many", func(s *Spec) { s.MountSubstrings = manyPaths(MaxMountSubstrings + 1) }, "mount_substrings"},
		{"substring empty", func(s *Spec) { s.MountSubstrings[1] = "" }, "mount_substrings[1]"},
		{"substring long", func(s *Spec) { s.MountSubstrings[1] = long(MaxSubstringBytes + 1) }, "mount_substrings[1]"},
		{"substring control", func(s *Spec) { s.MountSubstrings[0] = "a\x00b" }, "mount_substrings[0]"},
		{"substring bad utf8", func(s *Spec) { s.MountSubstrings[0] = "a\xffb" }, "mount_substrings[0]"},
		{"other config dir is the same runtime", func(s *Spec) { s.OtherConfigDir = "/home/lead/.claude" }, "other_config_dir"},
		{"other config dir elsewhere", func(s *Spec) { s.OtherConfigDir = "/tmp/.codex" }, "other_config_dir"},
		{"tcp not loopback", func(s *Spec) { s.LoopbackTCP = "192.0.2.1:80" }, "loopback_tcp"},
		{"tcp no port", func(s *Spec) { s.LoopbackTCP = "127.0.0.1" }, "loopback_tcp"},
		{"tcp port zero", func(s *Spec) { s.LoopbackTCP = "127.0.0.1:0" }, "loopback_tcp"},
		{"tcp a name", func(s *Spec) { s.LoopbackTCP = "localhost:80" }, "loopback_tcp"},
		{"abstract with @", func(s *Spec) { s.AbstractSocket = "@x" }, "abstract_socket"},
		{"abstract empty", func(s *Spec) { s.AbstractSocket = "" }, "abstract_socket"},
		{"abstract long", func(s *Spec) { s.AbstractSocket = long(MaxAbstractBytes + 1) }, "abstract_socket"},
		{"abstract nul", func(s *Spec) { s.AbstractSocket = "a\x00b" }, "abstract_socket"},
		{"host empty", func(s *Spec) { s.ProviderHost = "" }, "provider_host"},
		{"host is an address", func(s *Spec) { s.ProviderHost = "192.0.2.1" }, "provider_host"},
		{"host is an ipv6 address", func(s *Spec) { s.ProviderHost = "2001:db8::1" }, "provider_host"},
		{"host has a space", func(s *Spec) { s.ProviderHost = "a b.example" }, "provider_host"},
		{"host empty label", func(s *Spec) { s.ProviderHost = "a..example" }, "provider_host"},
		{"host trailing dot", func(s *Spec) { s.ProviderHost = "a.example." }, "provider_host"},
		{"host leading dot", func(s *Spec) { s.ProviderHost = ".example" }, "provider_host"},
		{"host long label", func(s *Spec) { s.ProviderHost = long(64) + ".example" }, "provider_host"},
		{"host long", func(s *Spec) { s.ProviderHost = strings.Repeat(long(60)+".", 5) + "x" }, "provider_host"},
		{"tree relative", func(s *Spec) { s.Tree = "work/x" }, "tree"},
		{"tree root", func(s *Spec) { s.Tree = "/" }, "tree"},
		{"git paths missing", func(s *Spec) { s.GitReadOnly = s.GitReadOnly[:2] }, "git_read_only"},
		{"git paths extra", func(s *Spec) { s.GitReadOnly = append(s.GitReadOnly, "/work/ataniq/.git/x") }, "git_read_only"},
		{"git paths elsewhere", func(s *Spec) { s.GitReadOnly[1] = "/work/other/.git/hooks" }, "git_read_only[1]"},
		{"git paths out of order", func(s *Spec) { s.GitReadOnly[0], s.GitReadOnly[1] = s.GitReadOnly[1], s.GitReadOnly[0] }, "git_read_only[0]"},
		{"managed relative", func(s *Spec) { s.ManagedSettings = "etc/x" }, "managed_settings"},
		{"claude managed elsewhere", func(s *Spec) { s.ManagedSettings = "/etc/other.json" }, "managed_settings"},
		{"claude without marker", func(s *Spec) { s.ManagedMarker = "" }, "managed_marker"},
		{"marker long", func(s *Spec) { s.ManagedMarker = long(MaxMarkerBytes + 1) }, "managed_marker"},
		{"marker control", func(s *Spec) { s.ManagedMarker = "a\tb" }, "managed_marker"},
		{"optional outside", func(s *Spec) { s.Optional = []int{9} }, "optional"},
		{"optional zero", func(s *Spec) { s.Optional = []int{0} }, "optional"},
		{"optional twice", func(s *Spec) { s.Optional = []int{4, 4} }, "optional"},
		{"optional many", func(s *Spec) { s.Optional = []int{1, 2, 3, 4, 5, 6, 7, 8, 1, 2, 3} }, "optional"},
		{"connect timeout low", func(s *Spec) { s.ConnectTimeoutMS = 10 }, "connect_timeout_ms"},
		{"connect timeout high", func(s *Spec) { s.ConnectTimeoutMS = MaxTimeoutMS + 1 }, "connect_timeout_ms"},
		{"connect timeout negative", func(s *Spec) { s.ConnectTimeoutMS = -1 }, "connect_timeout_ms"},
		{"resolve timeout high", func(s *Spec) { s.ResolveTimeoutMS = 60000 }, "resolve_timeout_ms"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := testSpec(profile.Claude)
			tc.mutate(&s)
			err := s.Validate()
			if err == nil {
				t.Fatal("accepted")
			}
			r := refusalOf(t, err)
			if r.Field != tc.field {
				t.Errorf("field %q (%s), want %q", r.Field, r.Reason, tc.field)
			}
			if r.Reason == "" {
				t.Error("no reason")
			}
		})
	}
}

func TestSpecValidateBoundsAreInclusive(t *testing.T) {
	s := testSpec(profile.Claude)
	s.Env = nil
	for i := 0; i < MaxEnvEntries; i++ {
		s.Env = append(s.Env, EnvEntry{fmt.Sprintf("V%d", i), strings.Repeat("v", MaxEnvValueBytes)})
	}
	s.HolderPaths = nil
	for i := 0; i < MaxPathEntries; i++ {
		s.HolderPaths = append(s.HolderPaths, fmt.Sprintf("/x/%d", i))
	}
	s.MountSubstrings = nil
	for i := 0; i < MaxMountSubstrings; i++ {
		s.MountSubstrings = append(s.MountSubstrings, strings.Repeat("m", MaxSubstringBytes))
	}
	s.AbstractSocket = strings.Repeat("a", MaxAbstractBytes)
	s.ManagedMarker = strings.Repeat("k", MaxMarkerBytes)
	s.ConnectTimeoutMS, s.ResolveTimeoutMS = MinTimeoutMS, MaxTimeoutMS
	if err := s.Validate(); err != nil {
		t.Fatalf("the largest allowed spec was refused: %v", err)
	}
	// 128 values of 4096 bytes do not fit the 256 KiB cap, and say so.
	if _, err := EncodeSpec(s); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("EncodeSpec of an oversized spec: %v", err)
	}
}

func TestSpecHostFilesDoNotLeakIntoRefusals(t *testing.T) {
	s := testSpec(profile.Claude)
	s.Env[0].Value = "secret-value\n"
	err := s.Validate()
	if err == nil || strings.Contains(err.Error(), "secret-value") {
		t.Errorf("a refusal quotes an environment value: %v", err)
	}
}

// profileInputs are valid inputs of the profile package, every path invented.
func profileInputs(rt profile.Runtime) profile.Inputs {
	in := profile.Inputs{
		StateRoot:             "/srv/state",
		Role:                  "arc-lead",
		Project:               "ataniq",
		Runtime:               rt,
		RuntimeVersion:        "2.1.0",
		Identity:              profile.IdentityRoot,
		AgentInstall:          "/srv/owned/agent/" + string(rt) + "/2.1.0",
		ProbeBinary:           "/srv/owned/bin/probe",
		SpecDir:               "/srv/owned/spec/arc-lead",
		ManagedSettings:       "/srv/owned/managed/arc-lead/settings.json",
		ManagedSettingsSHA256: strings.Repeat("c1", 32),
		LeadSkills:            "/srv/owned/skills/arc-lead",
		InstructionFile:       "/srv/owned/instructions/arc-lead/" + rt.InstructionFileName(),
		GitIdentityFile:       "/srv/owned/git/arc-lead/config",
		QargiInstall:          "/srv/owned/qargi",
		AssignmentDir:         "/srv/owned/assignment/ataniq",
		ProjectionDir:         "/srv/owned/projection/ataniq",
		ResolvConf:            "/srv/owned/resolv/arc-lead/resolv.conf",
		NeverMounted: []string{
			"/home/owner/work/ataniq", "/home/owner/.claude", "/home/owner/.claude.json",
			"/home/owner/.codex", "/home/owner/.ssh",
		},
		Term: "xterm-256color", ColorTerm: "truecolor", Lang: "C.UTF-8", TZ: "UTC",
	}
	if rt == profile.Codex {
		in.ManagedSettingsInside = "/etc/codex/managed-config.toml"
	}
	return in
}

func TestNewSpec(t *testing.T) {
	for _, rt := range profile.Runtimes() {
		t.Run(string(rt), func(t *testing.T) {
			in := profileInputs(rt)
			h := HostFacts{
				HolderPaths:     []string{"/run/example/holder.sock", "/var/lib/example/holder"},
				OwnerPaths:      []string{"/home/owner/.claude", "/home/owner/extra"},
				MountSubstrings: []string{"/run/example"},
				LoopbackTCP:     "127.0.0.1:40123",
				AbstractSocket:  "example-canary-1",
				ProviderHost:    "api.example.org",
				ManagedMarker:   "example-guard-hook",
				Optional:        []int{4},
			}
			s, err := NewSpec(in, h)
			if err != nil {
				t.Fatalf("NewSpec: %v", err)
			}
			digest, _ := in.Digest()
			if s.Digest != digest || s.Runtime != rt {
				t.Errorf("digest or runtime: %s %s", s.Digest, s.Runtime)
			}
			env, _ := in.Environment()
			if len(s.Env) != len(env) {
				t.Fatalf("env has %d entries, want %d", len(s.Env), len(env))
			}
			for i, e := range env {
				if s.Env[i].Name != e.Name || s.Env[i].Value != e.Value {
					t.Errorf("env[%d] = %v, want %v", i, s.Env[i], e)
				}
			}
			if s.OtherConfigDir != profile.ConfigInside(rt.Other()) || s.Tree != "/work/ataniq" {
				t.Errorf("other config dir %q or tree %q", s.OtherConfigDir, s.Tree)
			}
			if s.ManagedSettings != in.ManagedInside() || s.SeccompExpected {
				t.Errorf("managed %q seccomp %v", s.ManagedSettings, s.SeccompExpected)
			}
			// Never-mounted paths are owner paths and mount substrings, once.
			if len(s.OwnerPaths) != 6 {
				t.Errorf("owner paths %v: want the five never-mounted and one extra", s.OwnerPaths)
			}
			subs := strings.Join(s.MountSubstrings, " ")
			for _, want := range []string{"/run/example", "/home/owner/.ssh", "/var/lib/example/holder"} {
				if !strings.Contains(subs, want) {
					t.Errorf("mount substrings %v lack %s", s.MountSubstrings, want)
				}
			}
			// The result is a spec the probe accepts.
			data, err := EncodeSpec(s)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeSpecBytes(data); err != nil {
				t.Errorf("DecodeSpecBytes: %v", err)
			}
		})
	}
}

func TestNewSpecSeccompAndRefusals(t *testing.T) {
	in := profileInputs(profile.Claude)
	in.SeccompFile = "/srv/owned/seccomp/filter.bpf"
	in.SeccompSHA256 = strings.Repeat("d2", 32)
	h := HostFacts{
		HolderPaths: []string{"/run/example/holder.sock"}, LoopbackTCP: "127.0.0.1:40123",
		AbstractSocket: "example-canary-1", ProviderHost: "api.example.org", ManagedMarker: "m",
	}
	s, err := NewSpec(in, h)
	if err != nil {
		t.Fatal(err)
	}
	if !s.SeccompExpected {
		t.Error("a seccomp file in the inputs did not make the spec expect a filter")
	}
	// Different profiles give different digests, so the spec follows.
	other, _ := NewSpec(profileInputs(profile.Claude), h)
	if other.Digest == s.Digest {
		t.Error("the digest does not follow the profile")
	}

	bad := in
	bad.Term = ""
	if _, err := NewSpec(bad, h); err == nil {
		t.Error("invalid inputs gave a spec")
	}
	h.ManagedMarker = ""
	if _, err := NewSpec(in, h); err == nil {
		t.Error("a claude spec without a marker")
	}
}

// A spec the encoder makes is read back equal, whatever the characters of its
// strings: nothing may change in the JSON, or the probe checks other paths
// than the holder wrote.
func TestSpecRoundTripIsExact(t *testing.T) {
	for _, rt := range profile.Runtimes() {
		s := testSpec(rt)
		s.OwnerPaths = append(s.OwnerPaths, "/home/owner/café", "/home/owner/日本", `/home/owner/a"b\c<d>&e`)
		s.HolderPaths = append(s.HolderPaths, "/run/example/☃")
		s.Env[0].Value = `/home/lead "quoted" <&> \ and café`
		s.Optional = []int{4}
		s.ConnectTimeoutMS, s.ResolveTimeoutMS = 250, 9000
		data, err := EncodeSpec(s)
		if err != nil {
			t.Fatalf("%s: %v", rt, err)
		}
		back, err := DecodeSpecBytes(data)
		if err != nil {
			t.Fatalf("%s: %v", rt, err)
		}
		if !reflect.DeepEqual(s, back) {
			t.Errorf("%s: the spec changed in a round trip:\n%+v\n%+v", rt, s, back)
		}
	}
}

// A string that is not valid UTF-8 changes in the JSON (encoding/json writes
// U+FFFD), so Validate refuses it in every field that holds text.
func TestSpecRefusesInvalidUTF8(t *testing.T) {
	bad := "/home/owner/caf\xe9"
	for _, tc := range []struct {
		name   string
		mutate func(s *Spec)
		field  string
	}{
		{"holder path", func(s *Spec) { s.HolderPaths[1] = bad }, "holder_paths[1]"},
		{"owner path", func(s *Spec) { s.OwnerPaths[2] = bad }, "owner_paths[2]"},
		{"mount substring", func(s *Spec) { s.MountSubstrings[0] = bad }, "mount_substrings[0]"},
		{"env value", func(s *Spec) { s.Env[1].Value = "/usr/caf\xe9" }, "env[1]"},
		{"env name", func(s *Spec) { s.Env[1].Name = "P\xe9TH" }, "env[1]"},
		{"tree", func(s *Spec) { s.Tree = bad }, "tree"},
		{"managed settings of codex", func(s *Spec) {
			s.Runtime = profile.Codex
			s.OtherConfigDir = profile.ConfigInside(profile.Claude)
			s.ManagedSettings = bad
		}, "managed_settings"},
		{"managed marker", func(s *Spec) { s.ManagedMarker = "hook\xe9" }, "managed_marker"},
		{"provider host", func(s *Spec) { s.ProviderHost = "api.\xe9.org" }, "provider_host"},
		{"abstract socket", func(s *Spec) { s.AbstractSocket = "a\xe9" }, "abstract_socket"},
		{"digest", func(s *Spec) { s.Digest = strings.Repeat("a\xe9", 32) }, "digest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testSpec(profile.Claude)
			tc.mutate(&s)
			err := s.Validate()
			if err == nil {
				t.Fatal("accepted")
			}
			if r := refusalOf(t, err); r.Field != tc.field {
				t.Errorf("field %q (%v), want %q", r.Field, err, tc.field)
			}
			if _, err := EncodeSpec(s); err == nil {
				t.Error("EncodeSpec accepted")
			}
		})
	}
	// Raw bytes in a spec that was written by hand are refused whole.
	good, _ := EncodeSpec(testSpec(profile.Claude))
	raw := bytes.Replace(good, []byte("/home/owner/.ssh"), []byte("/home/owner/.ss\xe9"), 1)
	if bytes.Equal(raw, good) {
		t.Fatal("test input did not change")
	}
	if _, err := DecodeSpecBytes(raw); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Errorf("a spec with a stray byte: %v", err)
	}
}

// Keys are exact and case-sensitive and appear once: the decoder of
// encoding/json would take another case and the last of two.
func TestDecodeSpecKeysAreExact(t *testing.T) {
	good, err := EncodeSpec(testSpec(profile.Claude))
	if err != nil {
		t.Fatal(err)
	}
	g := string(good)
	for _, tc := range []struct {
		name  string
		input string
	}{
		{"another case", strings.Replace(g, `"seccomp_expected"`, `"SECCOMP_EXPECTED"`, 1)},
		{"a key twice", strings.Replace(g, `"seccomp_expected": false,`, `"seccomp_expected": true, "seccomp_expected": false,`, 1)},
		{"the same key in two cases", strings.Replace(g, `"seccomp_expected": false,`, `"seccomp_expected": true, "Seccomp_Expected": false,`, 1)},
		{"another case in a list of objects", strings.Replace(g, `"name": "HOME"`, `"Name": "HOME"`, 1)},
		{"a key twice in a list of objects", strings.Replace(g, `"name": "HOME",`, `"name": "HOME", "name": "OTHER",`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.input == g {
				t.Fatal("test input did not change")
			}
			_, err := DecodeSpecBytes([]byte(tc.input))
			if err == nil {
				t.Fatal("accepted")
			}
			refusalOf(t, err)
		})
	}
	// Deep nesting is refused, not recursed into.
	deep := `{"version":` + strings.Repeat("[", 100) + strings.Repeat("]", 100) + `}`
	if _, err := DecodeSpecBytes([]byte(deep)); err == nil {
		t.Error("a deeply nested spec was accepted")
	}
}
