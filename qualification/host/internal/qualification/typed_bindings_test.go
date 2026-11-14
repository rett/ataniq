package qualification

import (
	"encoding/json"
	"strings"
	"testing"
)

func typedRequest(c Case) Request {
	r := request(c)
	r.Payload = &Payload{TypedFD: &TypedFDPayload{Name: FD1BindingName, FD: typedFDSlot(c), Profile: FD1TypedProfile, AttemptID: "aabbccdd", Nonce: "01234567"}}
	if c == CaseFakeDirFD {
		r.Payload.TypedFD.Leaf = &FD1Leaf{Name: "leaf", Device: 1, Inode: 2, Size: pointer(int64(4)), SHA256: syntheticDigest([]byte("leaf"))}
	}
	return r
}

func typedBindings(r Request) FD1Bindings {
	p := r.Payload.TypedFD
	object := func(fd int, kind, access string, nb bool) FD1Object {
		return FD1Object{FD: fd, Device: 1, Inode: uint64(fd), Type: kind, Access: access, Nonblock: pointer(nb)}
	}
	channel := func(c Case, kind, access string) *FD1Channel {
		frame := fd1Frame(c, p.Nonce)
		return &FD1Channel{FD1Object: object(typedFDSlot(c), kind, access, true), Length: len(frame), SHA256: syntheticDigest(frame), PeerClosed: pointer(true)}
	}
	leaf := typedRequest(CaseFakeDirFD).Payload.TypedFD.Leaf
	if p.Leaf != nil {
		leaf = p.Leaf
	}
	return FD1Bindings{Version: 1, RunID: r.RunID, RequestID: r.RequestID, AttemptID: p.AttemptID, Profile: p.Profile, PlanSHA256: strings.Repeat("a", 64), Nonce: p.Nonce,
		Directory: &FD1Directory{FD1Object: object(FD1DirectoryFD, "directory", "read-only", false), Leaf: leaf},
		Pipe:      channel(CaseFakePipeFD, "pipe", "read-only"), Socket: channel(CaseFakeSocketFD, "unix-stream", "read-write"),
		Regular: &FD1Regular{FD1Object: object(FD1RegularFD, "regular", "read-only", false), Size: pointer(int64(4)), SHA256: syntheticDigest([]byte("fake"))}}
}

func TestTypedRequestCodecAndCompatibility(t *testing.T) {
	for _, c := range []Case{CaseFakeDirFD, CaseFakePipeFD, CaseFakeSocketFD} {
		r := typedRequest(c)
		if _, err := DecodeRequest(strings.NewReader(wire(t, r))); err != nil {
			t.Fatal(err)
		}
		for _, mutate := range []func(*Request){
			func(r *Request) { r.Payload.File = &NamePayload{Name: "fake"} },
			func(r *Request) { r.Payload.TypedFD.FD++ },
			func(r *Request) { r.Payload.TypedFD.Profile = "unknown" },
			func(r *Request) { r.Payload.TypedFD.Name = "other.json" },
			func(r *Request) { r.Payload.TypedFD.Nonce = "" },
			func(r *Request) { r.Payload.TypedFD.AttemptID = "" },
			func(r *Request) { r.MaxBytes = 1 },
			func(r *Request) {
				if c == CaseFakeDirFD {
					r.Payload.TypedFD.Leaf = nil
				} else {
					r.Payload.TypedFD.Leaf = typedRequest(CaseFakeDirFD).Payload.TypedFD.Leaf
				}
			},
		} {
			bad := typedRequest(c)
			mutate(&bad)
			if _, err := DecodeRequest(strings.NewReader(wire(t, bad))); err == nil {
				t.Fatal("invalid typed request accepted", wire(t, bad))
			}
		}
	}
	// Exact historical wire shape, including field order; no optional extension
	// field appears in old requests or nested payloads used by plan hashing.
	r := request(CaseInventory)
	want := `{"version":1,"run_id":"0123456789abcdef","request_id":"fedcba9876543210","case":"inventory","max_duration_ms":100,"max_bytes":4096}`
	if wire(t, r) != want {
		t.Fatal(wire(t, r))
	}
	r = request(CaseFakeFile)
	r.Payload = &Payload{File: &NamePayload{Name: "fake-file"}}
	if strings.Contains(wire(t, r), "typed_fd") {
		t.Fatal("extension serialized for old request")
	}
}

func TestFD1BindingStrictSchema(t *testing.T) {
	r := typedRequest(CaseFakePipeFD)
	good := wire(t, typedBindings(r))
	if b, err := DecodeFD1Bindings(strings.NewReader(good)); err != nil || !b.matches(r) {
		t.Fatal(b, err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"missing directory": func(m map[string]any) { delete(m, "directory") },
		"missing pipe":      func(m map[string]any) { delete(m, "pipe") },
		"missing socket":    func(m map[string]any) { delete(m, "socket") },
		"missing regular":   func(m map[string]any) { delete(m, "regular") },
		"extra":             func(m map[string]any) { m["bpf"] = 513 },
		"null":              func(m map[string]any) { m["pipe"] = nil },
		"bad version":       func(m map[string]any) { m["version"] = 2 },
		"unknown profile":   func(m map[string]any) { m["profile"] = "other" },
		"missing plan":      func(m map[string]any) { delete(m, "plan_sha256") },
		"bad plan":          func(m map[string]any) { m["plan_sha256"] = "" },
		"zero device":       func(m map[string]any) { m["pipe"].(map[string]any)["device"] = 0 },
		"zero inode":        func(m map[string]any) { m["pipe"].(map[string]any)["inode"] = 0 },
		"missing identity":  func(m map[string]any) { delete(m["pipe"].(map[string]any), "inode") },
		"wrong slot":        func(m map[string]any) { m["pipe"].(map[string]any)["fd"] = 257 },
		"wrong type":        func(m map[string]any) { m["socket"].(map[string]any)["type"] = "pipe" },
		"wrong access":      func(m map[string]any) { m["pipe"].(map[string]any)["access"] = "read-write" },
		"missing nonblock":  func(m map[string]any) { delete(m["directory"].(map[string]any), "nonblock") },
		"wrong nonblock":    func(m map[string]any) { m["pipe"].(map[string]any)["nonblock"] = false },
		"live peer":         func(m map[string]any) { m["socket"].(map[string]any)["peer_closed"] = false },
		"length":            func(m map[string]any) { m["pipe"].(map[string]any)["length"] = 128 },
		"negative length":   func(m map[string]any) { m["pipe"].(map[string]any)["length"] = -1 },
		"wrong hash":        func(m map[string]any) { m["pipe"].(map[string]any)["sha256"] = strings.Repeat("b", 64) },
		"path leaf":         func(m map[string]any) { m["directory"].(map[string]any)["leaf"].(map[string]any)["name"] = "../leaf" },
		"huge leaf":         func(m map[string]any) { m["directory"].(map[string]any)["leaf"].(map[string]any)["size"] = 4097 },
		"missing size":      func(m map[string]any) { delete(m["regular"].(map[string]any), "size") },
		"negative size":     func(m map[string]any) { m["regular"].(map[string]any)["size"] = -1 },
	} {
		t.Run(name, func(t *testing.T) {
			var m map[string]any
			if err := json.Unmarshal([]byte(good), &m); err != nil {
				t.Fatal(err)
			}
			mutate(m)
			if _, err := DecodeFD1Bindings(strings.NewReader(wire(t, m))); err == nil {
				t.Fatal("invalid bindings accepted")
			}
		})
	}
	for _, bad := range []string{
		strings.Replace(good, `"version":1`, `"version":1,"version":1`, 1),
		strings.Replace(good, `"fd":128`, `"fd":128,"fd":128`, 1),
		strings.Replace(good, `"version"`, `"Version"`, 1),
		strings.Replace(good, `"socket"`, `"\u017focket"`, 1),
		good + `{}`, good + "junk", "[]", "{", good + strings.Repeat(" ", MaxFD1BindingBytes),
	} {
		if _, err := DecodeFD1Bindings(strings.NewReader(bad)); err == nil {
			t.Fatal("bad JSON accepted")
		}
	}
}
