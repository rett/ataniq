package protocol_test

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	core "ataniq/internal/agentsession/protocol"
	"ataniq/packs/codex/protocol"
)

func active(t *testing.T) (protocol.Codec, []byte) {
	t.Helper()
	c, f := fresh(t), frames(t, "events")
	observe(t, c, f[0])
	observe(t, c, f[1], core.Event{Kind: "state", State: "busy"})
	return c, f[3]
}

func TestGrantReplay(t *testing.T) {
	c, req := active(t)
	replies := frames(t, "grants")
	g, err := c.DecodeGrant(req, replies[0])
	review := true
	want := protocol.Grant{Scope: "turn", StrictAutoReview: &review, Permissions: &protocol.Permissions{FileSystem: &protocol.FileSystem{Read: []string{"/work/b"}}}}
	if err != nil || !reflect.DeepEqual(g, want) {
		t.Fatal(g, err)
	}
	b, err := c.EncodeGrant(req, g)
	if err != nil {
		t.Fatal(err)
	}
	equalJSON(t, b, string(replies[0]))
	g, err = c.DecodeGrant(req, replies[1])
	if err != nil || g.Scope != "turn" || !reflect.DeepEqual(g.Permissions, &protocol.Permissions{}) {
		t.Fatal(g, err)
	}
	b, err = c.EncodeGrant(req, g)
	if err != nil {
		t.Fatal(err)
	}
	equalJSON(t, b, `{"id":7,"result":{"permissions":{},"scope":"turn"}}`)
	g.Permissions.FileSystem = &protocol.FileSystem{Write: []string{"/work/b"}}
	if b, err := c.EncodeGrant(req, g); b != nil || err == nil {
		t.Fatal("encoded widened decision")
	}
	observe(t, c, frames(t, "events")[4], core.Event{Kind: "state", State: "idle"})
	if b, err := c.EncodeGrant(req, want); b != nil || err == nil {
		t.Fatal("answered completed turn")
	}
}

func TestRefuseGrant(t *testing.T) {
	c, req := active(t)
	reply := frames(t, "grants")[0]
	refuse := func(req, reply []byte) {
		t.Helper()
		if g, err := c.DecodeGrant(req, reply); err == nil || !reflect.DeepEqual(g, protocol.Grant{}) {
			t.Fatal(string(req), string(reply), g, err)
		}
	}
	for _, tc := range [][2]string{{`"id":7`, `"id":"7"`}, {`"id":7`, `"id":8`}, {`"scope":"turn"`, `"scope":"session"`},
		{`"scope":"turn"`, `"scope":null`}, {"permissions", "Permissions"}, {"fileSystem", "filesystem"}, {`"read"`, `"write"`},
		{`"/work/b"`, `"/work"`}, {`"/work/b"`, `null`}, {`"/work/b"`, `"/work/b","/work/b"`},
		{`"read":[`, `"all":true,"read":[`}, {`"permissions":{`, `"permissions":{},"permissions":{`}} {
		refuse(req, bytes.Replace(reply, []byte(tc[0]), []byte(tc[1]), 1))
	}
	for _, tc := range [][2]string{{permissionName, "other"}, {"turn-a", "turn-b"}, {"thread-a", "thread-b"}, {"cwd", "Cwd"},
		{"permissions", "extra"}, {`"startedAtMs":0`, `"startedAtMs":null`}} {
		refuse(bytes.Replace(req, []byte(tc[0]), []byte(tc[1]), 1), reply)
	}
	refuse(req, []byte(`{"id":7,"error":{"code":1,"message":"no"}}`))
	refuse(req, []byte(`{"id":7,"result":{}}`))
	refuse(bytes.Replace(req, []byte(`"enabled":true`), []byte(`"enabled":false`), 1), []byte(`{"id":7,"result":{"permissions":{"network":{"enabled":true}}}}`))
	refuse([]byte("{"), reply)
	refuse(req, []byte("{"))
}

const permissionName = "item/permissions/requestApproval"

func permissionFrame(profile string) []byte {
	return []byte(`{"method":"item/permissions/requestApproval","id":7,"params":{"threadId":"thread-a","turnId":"turn-a","itemId":"item","startedAtMs":0,"cwd":"/work","permissions":` + profile + `}}`)
}

func TestNetworkOnlyRefusesFilesystem(t *testing.T) {
	for _, profile := range []string{`{"network":{"enabled":true}}`, `{"network":{"enabled":true},"fileSystem":null}`} {
		c, _ := active(t)
		req := permissionFrame(profile)
		observe(t, c, req, core.Event{Kind: "question", Text: "Permission requested"})
		for _, fs := range []string{`{"write":["/"]}`, `{"read":["/"]}`,
			`{"entries":[{"access":"write","path":{"type":"path","path":"/"}}]}`} {
			reply := []byte(`{"id":7,"result":{"permissions":{"fileSystem":` + fs + `}}}`)
			if g, err := c.DecodeGrant(req, reply); err == nil || !reflect.DeepEqual(g, protocol.Grant{}) {
				t.Errorf("accepted filesystem %s for %s: %+v, %v", fs, profile, g, err)
			}
		}
	}
}

func TestInvalidPermissionRequest(t *testing.T) {
	c, _ := active(t)
	for _, tc := range []struct{ name, filesystem string }{
		{"mixed", `{"read":[],"entries":[]}`},
		{"empty read", `{"read":[""]}`},
		{"empty write", `{"write":[""]}`},
		{"zero depth", `{"globScanMaxDepth":0}`},
		{"access", `{"entries":[{"access":"other","path":{"type":"path","path":"/"}}]}`},
		{"path member", `{"entries":[{"access":"read","path":{"type":"path","path":"/","extra":1}}]}`},
		{"special member", `{"entries":[{"access":"read","path":{"type":"special","value":{"kind":"root","extra":1}}}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if d, err := c.Decode(permissionFrame(`{"fileSystem":` + tc.filesystem + `}`)); err == nil || !reflect.DeepEqual(d, core.Decoded{}) {
				t.Fatalf("accepted malformed request: %+v, %v", d, err)
			}
		})
	}
}

func TestUnknownRequestMember(t *testing.T) {
	c, _ := active(t)
	req := bytes.Replace(permissionFrame(`{}`), []byte(`"permissions"`), []byte(`"extra":1,"permissions"`), 1)
	if d, err := c.Decode(req); err == nil || !reflect.DeepEqual(d, core.Decoded{}) {
		t.Fatal(d, err)
	}
}

func TestEntrySubset(t *testing.T) {
	c, _ := active(t)
	const request = `{"method":"item/permissions/requestApproval","id":"card","params":{"threadId":"thread-a","turnId":"turn-a","itemId":"item","startedAtMs":0,"cwd":"/work","permissions":PROFILE}}`
	const reply = `{"id":"c\u0061rd","result":{"permissions":PROFILE,"scope":"turn"}}`
	const selected = `{"access":"read","path":{"type":"path","path":"/work/a"}}`
	const glob = `{"access":"read","path":{"type":"glob_pattern","pattern":"*.md"}}`
	const deny = `{"access":"deny","path":{"type":"special","value":{"kind":"project_roots","subpath":"private"}}}`
	profile := func(entries ...string) string {
		return `{"fileSystem":{"globScanMaxDepth":1,"entries":[` + strings.Join(entries, ",") + `]}}`
	}
	req := []byte(strings.Replace(request, "PROFILE", profile(selected, glob, deny), 1))
	accepted := strings.Replace(reply, "PROFILE", profile(glob, deny), 1)
	g, err := c.DecodeGrant(req, []byte(accepted))
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.EncodeGrant(req, g)
	if err != nil {
		t.Fatal(err)
	}
	equalJSON(t, b, accepted)
	write := strings.Replace(selected, `"read"`, `"write"`, 1)
	mixed := []byte(strings.Replace(request, "PROFILE", profile(write, glob, deny), 1))
	if _, err := c.DecodeGrant(mixed, []byte(strings.Replace(reply, "PROFILE", profile(write, deny), 1))); err == nil {
		t.Fatal("dropped a read restriction while retaining write")
	}
	for _, bad := range []string{profile(glob), profile(deny, glob), profile(selected, selected, deny),
		strings.Replace(accepted, "glob_pattern", "other", 1), strings.Replace(accepted, `"globScanMaxDepth":1,`, "", 1),
		strings.Replace(accepted, "project_roots", "root", 1), strings.Replace(accepted, `"entries":`, `"read":["/work/a"],"entries":`, 1),
		strings.Replace(accepted, `"globScanMaxDepth":1`, `"globScanMaxDepth":2`, 1)} {
		if !strings.Contains(bad, `"result"`) {
			bad = strings.Replace(reply, "PROFILE", bad, 1)
		}
		if _, err := c.DecodeGrant(req, []byte(bad)); err == nil {
			t.Fatal(bad)
		}
	}
}

func FuzzDecode(f *testing.F) {
	f.Add([]byte(`{"method":"thread/started","params":{"thread":{"id":"thread-a"}}}`))
	f.Add([]byte(`{"id":"r","result":{}}`))
	f.Add([]byte(`{"id":7,"result":{"permissions":{}}}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		c, req := active(t)
		if d, err := c.Decode(b); err != nil && !reflect.DeepEqual(d, core.Decoded{}) {
			t.Fatal(d)
		}
		g, err := c.DecodeGrant(req, b)
		if err != nil {
			return
		}
		wire, err := c.EncodeGrant(req, g)
		if err != nil {
			t.Fatal(err)
		}
		again, err := c.DecodeGrant(req, wire)
		if err != nil || !reflect.DeepEqual(g, again) {
			t.Fatal(again, err)
		}
	})
}
