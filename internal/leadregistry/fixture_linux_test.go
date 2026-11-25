package leadregistry

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The journal in testdata/v1-base was written by the build of commit 413640fc
// (develop before the pack reference was added to Requested), with the clock
// and the randomness of the tests, and digests.json holds what that build
// read back from it: for every role the digest of its requested profile, the
// profile digest of its launch intent, its epoch, its intent and its shown
// state. The journal has every record kind, a lead whose profile holds the
// characters json.Marshal escapes and the journal's encoder does not (< > &),
// a coordinator with the default profile, a failed launch resolved by the
// owner, a role of the tmux-manager, and a cut acknowledged in a later
// session. The files are never regenerated: they are what an older build left
// on disk, and a later build must read them as that build did.
const v1BaseDir = "testdata/v1-base"

type v1Digest struct {
	Role            string `json:"role"`
	RequestedDigest string `json:"requested_digest"`
	LaunchDigest    string `json:"launch_digest,omitempty"`
	Epoch           uint64 `json:"epoch"`
	Intent          string `json:"intent,omitempty"`
	State           string `json:"state"`
}

func readV1Base(t *testing.T) ([]byte, []v1Digest) {
	t.Helper()
	journal, err := os.ReadFile(filepath.Join(v1BaseDir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(v1BaseDir, "digests.json"))
	if err != nil {
		t.Fatal(err)
	}
	var want []v1Digest
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&want); err != nil {
		t.Fatal(err)
	}
	if len(want) != 4 {
		t.Fatalf("digests.json has %d roles, want 4", len(want))
	}
	return journal, want
}

// TestV1BaseJournalReplaysAsWritten opens the journal an older build wrote and
// finds every role as that build read it: the same requested digest, the same
// launch digest, epoch, intent and state. Opening it changes no byte of it.
func TestV1BaseJournalReplaysAsWritten(t *testing.T) {
	journal, want := readV1Base(t)
	dir := writeJournal(t, journal)
	r, err := Open(dir, testOpts(1))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if rec := r.Recovery(); rec != (Recovery{}) {
		t.Fatalf("recovery %+v: the fixture's cut is acknowledged", rec)
	}
	list := snapshot(t, r)
	if len(list) != len(want) {
		t.Fatalf("%d roles, want %d", len(list), len(want))
	}
	for i, e := range list {
		w := want[i]
		got := v1Digest{Role: e.Role.String(), RequestedDigest: e.Requested.Digest(), Epoch: e.Epoch, State: string(e.Shown.State)}
		if e.Intent != nil {
			got.LaunchDigest, got.Intent = e.Intent.ProfileDigest, e.Intent.ID
		}
		if got != w {
			t.Errorf("role %d:\n got %+v\nwant %+v", i, got, w)
		}
		if e.Intent != nil && e.Intent.ProfileDigest != e.Requested.Digest() {
			t.Errorf("%s: the launch digest is not the requested profile's", e.Role)
		}
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, journal) {
		t.Fatal("opening the journal changed it")
	}
}

// TestV1BaseJournalLinesReencodeByteForByte reads every line of the older
// journal through the replay path and encodes the record again with this
// build's types: each line comes out byte for byte, hash included, so no
// member this build added is written for a record that had none, and the
// chain of a later append rests on the same bytes.
func TestV1BaseJournalLinesReencodeByteForByte(t *testing.T) {
	journal, _ := readV1Base(t)
	lines := bytes.SplitAfter(journal, []byte("\n"))
	if last := lines[len(lines)-1]; len(last) == 0 {
		lines = lines[:len(lines)-1]
	}
	var prev [sha256.Size]byte
	for i, line := range lines {
		rec, sum, err := decodeLine(prev, uint64(i), bytes.TrimSuffix(line, []byte("\n")))
		if err != nil {
			t.Fatalf("line %d: %v", i+1, err)
		}
		again, sum2, err := encodeLine(prev, rec.seq, rec.at, rec.kind, rec.role, rec.payload)
		if err != nil {
			t.Fatalf("line %d: encode: %v", i+1, err)
		}
		if !bytes.Equal(again, line) || sum2 != sum {
			t.Fatalf("line %d is not encoded as it was written:\n got %s\nwant %s", i+1, again, line)
		}
		prev = sum
	}
	if len(lines) != 24 {
		t.Fatalf("%d lines, want 24", len(lines))
	}
}

// TestV1BaseRequestedDigests pins the requested-profile digests of the older
// journal against the profiles this build decodes from it: the digest of a
// profile that sets no member added later is the one recorded.
func TestV1BaseRequestedDigests(t *testing.T) {
	_, want := readV1Base(t)
	pa := Requested{Provider: ProviderClaude, Version: "2.1.325", Model: "model<a>&b", Effort: "high",
		PermissionMode: "default", AccessMode: "workspace"}
	pb := Requested{Provider: ProviderCodex, Version: "0.42.0", Model: "model-b", Effort: "medium",
		PermissionMode: "on-request", AccessMode: "read-only"}
	pc := Requested{}.withCoordinatorDefaults()
	for _, c := range []struct {
		role string
		p    Requested
	}{{"fixture-a/coord/feat-one", pc}, {"fixture-a/lead", pa}, {"fixture-b/lead", pb}, {"supervisor", pb}} {
		found := false
		for _, w := range want {
			if w.Role == c.role {
				found = true
				if got := c.p.Digest(); got != w.RequestedDigest {
					t.Errorf("%s: digest %s, recorded %s", c.role, got, w.RequestedDigest)
				}
			}
		}
		if !found {
			t.Errorf("%s is not in digests.json", c.role)
		}
	}
}
