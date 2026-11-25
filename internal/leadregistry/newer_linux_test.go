package leadregistry

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
	"time"

	"ataniq/internal/leadrole"
)

// newerRequested is the requested profile of a build newer than this one:
// every member this build knows and one it does not, in that build's order.
type newerRequested struct {
	Provider       Provider     `json:"provider"`
	Version        string       `json:"version"`
	Model          string       `json:"model"`
	Effort         string       `json:"effort"`
	PermissionMode string       `json:"permission_mode"`
	AccessMode     string       `json:"access_mode"`
	Future         futureMember `json:"future_member"`
}

type futureMember struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

// newerEnvelope is a journal line of a newer build: this build's envelope and
// a member it does not know.
type newerEnvelope struct {
	Seq     uint64          `json:"seq"`
	Time    string          `json:"time"`
	Kind    string          `json:"kind"`
	Role    *leadrole.Role  `json:"role,omitempty"`
	Payload json.RawMessage `json:"payload"`
	Record  int             `json:"record_version"`
}

// newerJournal builds, on top of this build's header, the lines a newer build
// writes for alpha: a define whose requested profile has a member this build
// does not know, a launch-begin whose digest is that build's own digest of the
// profile (with a payload member of its own), the outcome in an envelope with
// a member of its own, and a proven stop. It returns the journal and the
// digest the newer build recorded.
func newerJournal(t *testing.T, alpha leadrole.Role) ([]byte, string) {
	t.Helper()
	at := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	head, prev, err := encodeLine(genesis, 0, at, kindHeader, leadrole.Role{}, &headerPayload{Format: formatName, Version: formatVersion})
	if err != nil {
		t.Fatal(err)
	}
	req := newerRequested{
		Provider: ProviderClaude, Version: "9.9.9", Model: "model<x>&y", Effort: "high",
		PermissionMode: "default", AccessMode: "workspace",
		Future: futureMember{Name: "alpha-pack", Digest: hex.EncodeToString(make([]byte, 32))},
	}
	marshalled, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(marshalled)
	digest := hex.EncodeToString(sum[:])
	out := append([]byte(nil), head...)
	add := func(seq uint64, kind string, body any) {
		t.Helper()
		line, next, err := encodeLine(prev, seq, at.Add(time.Duration(seq)*time.Second), kind, alpha, body)
		if err != nil {
			t.Fatal(err)
		}
		out, prev = append(out, line...), next
	}
	add(1, kindDefine, &struct {
		Manager   Manager        `json:"manager"`
		Requested newerRequested `json:"requested"`
	}{ManagerAtaniq, req})
	intent := "0123456789abcdef0123456789abcdef"
	add(2, kindLaunchBegin, &struct {
		IntentID      string `json:"intent"`
		Epoch         uint64 `json:"epoch"`
		UnitName      string `json:"unit"`
		ProfileDigest string `json:"profile_digest"`
		Note          string `json:"launch_note"`
	}{intent, 1, alpha.UnitName(), digest, "from a newer build"})
	// The outcome, in an envelope with a member this build does not know.
	pb, err := marshalCompact(&outcomePayload{IntentID: intent, Epoch: 1, Outcome: OutcomeLaunched, Unit: UnitEvidence{InvocationID: "inv"}})
	if err != nil {
		t.Fatal(err)
	}
	role := alpha
	content, err := marshalCompact(newerEnvelope{Seq: 3, Time: at.Add(3 * time.Second).Format(timeLayout), Kind: kindLaunchOutcome, Role: &role, Payload: pb, Record: 2})
	if err != nil {
		t.Fatal(err)
	}
	next := chain(prev, content)
	line := append(append([]byte(nil), content[:len(content)-1]...), `,"hash":"`+hex.EncodeToString(next[:])+`"}`+"\n"...)
	out, prev = append(out, line...), next
	add(4, kindState, &statePayload{Epoch: 1, State: StateStopped, Evidence: EvidenceObservation, Reason: Reason{Code: "unit-gone"}, Proof: "control group empty"})
	return out, digest
}

// TestNewerBuildJournalReplays: a journal a newer build wrote, with members
// this build does not know in a profile, a payload and an envelope, opens; the
// launch digest that build recorded matches the profile as stored, and the
// role shows what this build knows of it.
func TestNewerBuildJournalReplays(t *testing.T) {
	alpha := lead(t, "alpha")
	journal, digest := newerJournal(t, alpha)
	dir := writeJournal(t, journal)
	r := openReg(t, dir)
	e, err := r.Get(alpha)
	if err != nil {
		t.Fatal(err)
	}
	if e.Intent == nil || e.Intent.ProfileDigest != digest || e.Intent.Outcome != OutcomeLaunched || e.Shown.State != StateStopped {
		t.Fatalf("entry %+v intent %+v", e, e.Intent)
	}
	if e.Requested.Model != "model<x>&y" || e.Requested.Version != "9.9.9" {
		t.Fatalf("requested %+v", e.Requested)
	}
	// What this build makes of the profile has another digest: the identity is
	// the stored bytes, not the decoded struct.
	if e.Requested.Digest() == digest {
		t.Fatal("the decoded profile has the newer build's digest; the test proves nothing")
	}
}

// TestNewerBuildProfileIsNotLaunched: this build does not launch a profile it
// cannot read whole. The refusal writes nothing, and defining the role again
// with this build's profile lifts it.
func TestNewerBuildProfileIsNotLaunched(t *testing.T) {
	alpha := lead(t, "alpha")
	journal, _ := newerJournal(t, alpha)
	dir := writeJournal(t, journal)
	r := openReg(t, dir)
	size := journalSize(t, dir)
	_, err := r.BeginLaunch(alpha)
	if !errors.Is(err, ErrLaunchProfile) || !errors.Is(err, ErrLaunchRefused) {
		t.Fatalf("BeginLaunch = %v, want ErrLaunchProfile", err)
	}
	if journalSize(t, dir) != size {
		t.Fatal("the refusal wrote")
	}
	okf(t)(r.Define(alpha, ManagerAtaniq, goodProfile()))
	in, err := r.BeginLaunch(alpha)
	if err != nil {
		t.Fatalf("BeginLaunch after the define: %v", err)
	}
	if in.ProfileDigest != goodProfile().Digest() || in.Epoch != 2 {
		t.Fatalf("intent %+v", in)
	}
	before := snapshot(t, r)
	r.Close()
	r2 := openReg(t, dir)
	if !sameEntries(snapshot(t, r2), before) {
		t.Fatal("the reopened journal differs")
	}
}

// TestRequestedInAnotherOrderIsForeign: a profile whose members are all known
// but are stored in another order than this build writes them is not what
// this build would write: it replays, with the digest of its stored bytes,
// and is not launched.
func TestRequestedInAnotherOrderIsForeign(t *testing.T) {
	dir := regDir(t)
	r := openReg(t, dir)
	r.Close()
	data, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	alpha := lead(t, "alpha")
	p := goodProfile()
	type reordered struct {
		Model          string   `json:"model"`
		Provider       Provider `json:"provider"`
		Version        string   `json:"version"`
		Effort         string   `json:"effort"`
		PermissionMode string   `json:"permission_mode"`
		AccessMode     string   `json:"access_mode"`
	}
	data = appendRecord(t, data, kindDefine, alpha, &struct {
		Manager   Manager   `json:"manager"`
		Requested reordered `json:"requested"`
	}{ManagerAtaniq, reordered{p.Model, p.Provider, p.Version, p.Effort, p.PermissionMode, p.AccessMode}})
	d := writeJournal(t, data)
	r2 := openReg(t, d)
	e, err := r2.Get(alpha)
	if err != nil || e.Requested != p {
		t.Fatalf("%+v %v", e, err)
	}
	if !e.foreignProfile || e.profileDigest == p.Digest() {
		t.Fatalf("foreign %v digest %s", e.foreignProfile, e.profileDigest)
	}
	if _, err := r2.BeginLaunch(alpha); !errors.Is(err, ErrLaunchProfile) {
		t.Fatalf("BeginLaunch = %v", err)
	}
}

// TestRequestedDigestOfThisBuild: for every profile this build writes, the
// digest of the stored member is Requested.Digest, also with the characters
// the journal's encoder leaves unescaped and json.Marshal escapes.
func TestRequestedDigestOfThisBuild(t *testing.T) {
	for _, p := range []Requested{
		goodProfile(),
		{Provider: ProviderCodex, Version: "1", Model: "a<b>c&d", Effort: "e", PermissionMode: "p", AccessMode: "a"},
		{Provider: ProviderClaude, Model: "fable"},
	} {
		raw, err := marshalCompact(p)
		if err != nil {
			t.Fatal(err)
		}
		// The journal's encoder leaves < as it is, where json.Marshal escapes
		// it: the profile with one tests the escaping of the digest.
		if strings.ContainsRune(p.Model, '<') && bytes.IndexByte(raw, '<') < 0 {
			t.Fatal("the journal's encoder escapes <")
		}
		digest, foreign, err := requestedDigest(raw, p)
		if err != nil || foreign || digest != p.Digest() {
			t.Errorf("%+v: digest %s foreign %v err %v, want %s", p, digest, foreign, err, p.Digest())
		}
	}
}
