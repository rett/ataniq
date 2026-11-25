//go:build linux && ataniq_canary

package leaddaemon

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadwire"
)

// appendPackDefine appends to the registry journal of the state folder, as a
// later build would write it, a define record of role whose requested profile
// is the recorded one with a pack named. The line is chained to the one
// before it, so the journal opens; this build replays it and does not launch
// it (leadregistry.ErrLaunchProfile).
func appendPackDefine(t *testing.T, stateDir string, role leadrole.Role) {
	t.Helper()
	dir := filepath.Join(stateDir, RegistryDirName)
	reg, err := leadregistry.Open(dir, leadregistry.Options{Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	e, err := reg.Get(role)
	reg.Close()
	if err != nil {
		t.Fatal(err)
	}
	req := e.Requested
	req.Pack = leadregistry.PackRef{Name: "sample-pack", ManifestDigest: strings.Repeat("ab", 32)}

	path := filepath.Join(dir, leadregistry.FileName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n"))
	var last struct {
		Seq  uint64 `json:"seq"`
		Hash string `json:"hash"`
	}
	if err := json.Unmarshal(lines[len(lines)-1], &last); err != nil {
		t.Fatal(err)
	}
	prev, err := hex.DecodeString(last.Hash)
	if err != nil || len(prev) != sha256.Size {
		t.Fatalf("the last line's hash %q: %v", last.Hash, err)
	}
	compact := func(v any) []byte {
		t.Helper()
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(v); err != nil {
			t.Fatal(err)
		}
		return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	}
	payload := compact(struct {
		Manager   leadregistry.Manager   `json:"manager"`
		Requested leadregistry.Requested `json:"requested"`
	}{leadregistry.ManagerAtaniq, req})
	content := compact(struct {
		Seq     uint64          `json:"seq"`
		Time    string          `json:"time"`
		Kind    string          `json:"kind"`
		Role    leadrole.Role   `json:"role"`
		Payload json.RawMessage `json:"payload"`
	}{last.Seq + 1, time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z"), "define", role, payload})
	h := sha256.New()
	h.Write(prev)
	h.Write(content)
	line := append(append([]byte(nil), content[:len(content)-1]...), `,"hash":"`+hex.EncodeToString(h.Sum(nil))+`"}`+"\n"...)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(line); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestLeadStartOfAProfileThisBuildDoesNotLaunch: a lead whose define record
// names a pack, as a later build may write it, replays, and its start is
// refused with the code profile-not-launchable, not the generic
// launch-refused, with the way out in the message; nothing changes. Defining
// the lead again lifts it, and the lead then starts.
func TestLeadStartOfAProfileThisBuildDoesNotLaunch(t *testing.T) {
	l := newLeadEnv(t)
	l.start()
	want(t, l.define("alpha"), leadwire.StatusDone, "")
	if err := l.running.stop(); err != nil {
		t.Fatal(err)
	}
	appendPackDefine(t, l.state, l.cfgOf("alpha").Role)
	l.start()

	r := l.startLead("alpha")
	want(t, r, leadwire.StatusRefused, codeProfileNotLaunchable)
	if r.Message != msgProfileNotLaunchable || !strings.Contains(r.Message, "define the role again") {
		t.Fatalf("message %q", r.Message)
	}
	if row := l.row("alpha"); row.Epoch != 0 || row.State != "stopped" {
		t.Fatalf("row %+v", row)
	}

	want(t, l.define("alpha"), leadwire.StatusDone, "")
	l.holder("alpha", submitted)
	want(t, l.startLead("alpha"), leadwire.StatusDone, "")
	if row := l.row("alpha"); row.Epoch != 1 {
		t.Fatalf("row %+v", row)
	}
}
