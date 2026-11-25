package leadlifecycle

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
)

// appendPackDefine closes the rig's registry, appends to its journal a define
// record of role whose requested profile names a pack, as a later build may
// write it (chained to the line before, so the journal opens), and opens the
// registry again. This build replays the record and does not launch it.
func (r *rig) appendPackDefine(role leadrole.Role) {
	r.t.Helper()
	t := r.t
	req := profile()
	req.Pack = leadregistry.PackRef{Name: "sample-pack", ManifestDigest: strings.Repeat("ab", 32)}
	if err := r.reg.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(r.dir, leadregistry.FileName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n"))
	var last struct {
		Seq  uint64 `json:"seq"`
		Time string `json:"time"`
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
	}{last.Seq + 1, last.Time, "define", role, payload})
	h := sha256.New()
	h.Write(prev)
	h.Write(content)
	line := append(append([]byte(nil), content[:len(content)-1]...), `,"hash":"`+hex.EncodeToString(h.Sum(nil))+`"}`+"\n"...)
	if err := os.WriteFile(path, append(data, line...), 0o600); err != nil {
		t.Fatal(err)
	}
	r.open()
}

// TestStartOfAProfileThisBuildDoesNotLaunchWritesNothing: a stopped role
// whose stored profile names a pack is refused with ErrLaunchProfile before
// anything changes: the journal is byte for byte as it was (no desired state
// set and put back), no unit call is made and no holder command is built.
// The refusal is the lifecycle's own check; the registry's BeginLaunchWith
// still refuses the profile on its own.
func TestStartOfAProfileThisBuildDoesNotLaunchWritesNothing(t *testing.T) {
	r := newRig(t)
	role := r.define("alpha")
	r.mustStart(role)
	if _, err := r.ctl.Stop(t.Context(), role); err != nil {
		t.Fatal(err)
	}
	r.appendPackDefine(role)
	if e := r.get(role); e.Shown.State != leadregistry.StateStopped || e.Desired != leadregistry.DesiredStopped {
		t.Fatalf("before the start: %+v", e)
	}
	path := filepath.Join(r.dir, leadregistry.FileName)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	calls, built := len(r.fake.Calls()), r.holder.calls
	_, err = r.start(role)
	isErr(t, err, leadregistry.ErrLaunchProfile)
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("the refused start wrote to the journal:\n%s", after[len(before):])
	}
	if len(r.fake.Calls()) != calls || r.holder.calls != built {
		t.Fatal("the refused start reached the manager or built the holder command")
	}
	if _, err := r.reg.BeginLaunch(role); !errors.Is(err, leadregistry.ErrLaunchProfile) {
		t.Fatalf("BeginLaunch = %v, want ErrLaunchProfile", err)
	}
}
