package leadregistry

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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

// TestNewerBuildValueIsDamage: the tolerance of other builds covers members
// this build does not know, not values. A value this build does not know in a
// member it knows (a provider, a desired state, a shown state) fails the
// record's check, and the journal does not open (ErrCorrupt); the same record
// with a known value and a member this build does not know opens.
func TestNewerBuildValueIsDamage(t *testing.T) {
	alpha := lead(t, "alpha")
	dir := regDir(t)
	r := openReg(t, dir)
	okf(t)(r.Define(alpha, ManagerAtaniq, goodProfile()))
	r.Close()
	base, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	newer := goodProfile()
	newer.Provider = "a-provider-of-a-newer-build"
	for name, c := range map[string]struct {
		kind       string
		known, bad any
	}{
		"provider": {kindDefine,
			&definePayload{Manager: ManagerAtaniq, Requested: goodProfile()},
			&definePayload{Manager: ManagerAtaniq, Requested: newer}},
		"desired state": {kindDesired,
			map[string]any{"desired": DesiredRunning, "hold": true},
			map[string]any{"desired": "paused"}},
		"shown state": {kindState,
			map[string]any{"epoch": 0, "state": StateDesired, "evidence": EvidenceOwner, "reason": map[string]any{"code": "owner-wants"}, "note": "x"},
			map[string]any{"epoch": 0, "state": "hibernating", "evidence": EvidenceOwner, "reason": map[string]any{"code": "owner-wants"}}},
	} {
		if err := openErr(t, appendRecord(t, base, c.kind, alpha, c.known)); err != nil {
			t.Errorf("%s: a known value with an unknown member: %v", name, err)
		}
		if err := openErr(t, appendRecord(t, base, c.kind, alpha, c.bad)); !errors.Is(err, ErrCorrupt) {
			t.Errorf("%s: an unknown value: %v, want ErrCorrupt", name, err)
		}
	}
}

// repeatedDefine appends to a fresh journal a define record for alpha whose
// payload is the raw JSON object given, chained as a writer would chain it,
// and opens the result.
func repeatedDefine(t *testing.T, alpha leadrole.Role, payload string) *Registry {
	t.Helper()
	dir := regDir(t)
	openReg(t, dir).Close()
	data, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid([]byte(payload)) {
		t.Fatalf("not JSON: %s", payload)
	}
	data = appendRecord(t, data, kindDefine, alpha, json.RawMessage(payload))
	return openReg(t, writeJournal(t, data))
}

// TestRepeatedRequestedIsNotLaunched is the reviewer's case: a define record
// whose requested member is there twice, the first time with a member this
// build does not know and the second time as this build writes it. The typed
// decoding merges the two and the raw one keeps the second, so without the
// check of repeated members the profile looked like this build's own and was
// launched. It replays, and is not launched; the same holds when the first
// occurrence is a case alias that encoding/json matches to the member.
func TestRepeatedRequestedIsNotLaunched(t *testing.T) {
	alpha := lead(t, "alpha")
	p := goodProfile()
	canonical, err := marshalCompact(p)
	if err != nil {
		t.Fatal(err)
	}
	newer, err := json.Marshal(newerRequested{
		Provider: p.Provider, Version: p.Version, Model: p.Model, Effort: p.Effort,
		PermissionMode: p.PermissionMode, AccessMode: p.AccessMode,
		Future: futureMember{Name: "a-restriction", Digest: hex.EncodeToString(make([]byte, 32))},
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, first := range map[string]string{
		"exact":      `"requested"`,
		"case alias": `"Requested"`,
		"escaped":    `"re\u0071uested"`,
	} {
		t.Run(name, func(t *testing.T) {
			r := repeatedDefine(t, alpha, `{"manager":"ataniq",`+first+`:`+string(newer)+`,"requested":`+string(canonical)+`}`)
			e, err := r.Get(alpha)
			if err != nil || e.Requested != p {
				t.Fatalf("%+v %v", e, err)
			}
			if !e.foreignProfile {
				t.Fatal("a profile whose requested member is repeated is taken as this build's")
			}
			if _, err := r.BeginLaunch(alpha); !errors.Is(err, ErrLaunchProfile) {
				t.Fatalf("BeginLaunch = %v, want ErrLaunchProfile", err)
			}
			// A define again with this build's profile lifts it.
			okf(t)(r.Define(alpha, ManagerAtaniq, p))
			if _, err := r.BeginLaunch(alpha); err != nil {
				t.Fatalf("BeginLaunch after the define: %v", err)
			}
		})
	}
}

// TestNestedRepeatedMemberIsNotLaunched: a member repeated deeper in the
// requested profile (in the pack reference, or a member of this build's
// profile given twice with the same value, exactly or as a case alias) is not
// launched either, and the record replays.
func TestNestedRepeatedMemberIsNotLaunched(t *testing.T) {
	alpha := lead(t, "alpha")
	const head = `{"manager":"ataniq","requested":{"provider":"claude","version":"2.1.0","model":"model-a","effort":"high","permission_mode":"default","access_mode":"workspace"`
	for name, payload := range map[string]string{
		"a member twice":         head + `,"model":"model-a"}}`,
		"a member and its alias": head + `,"MODEL":"model-a"}}`,
		"in a nested object":     head + `,"future":{"a":[{"b":1,"b":2}]}}}`,
		"in a nested alias":      head + `,"future":{"name":1,"Name":1}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			r := repeatedDefine(t, alpha, payload)
			e, err := r.Get(alpha)
			if err != nil {
				t.Fatal(err)
			}
			if !e.foreignProfile {
				t.Fatal("a profile with a repeated member is taken as this build's")
			}
			if _, err := r.BeginLaunch(alpha); !errors.Is(err, ErrLaunchProfile) {
				t.Fatalf("BeginLaunch = %v, want ErrLaunchProfile", err)
			}
		})
	}
}

// TestRepeatBesideACanonicalProfileIsNotLaunched is the reviewer's isolated
// case: the requested profile is exactly as this build writes it, so its own
// comparison finds nothing foreign, and the only thing wrong with the record
// is a member repeated in metadata beside it, nested or not, exactly or as a
// case alias. Only the check of repeated members keeps it from launch; with
// that check off the profile is taken as this build's.
func TestRepeatBesideACanonicalProfileIsNotLaunched(t *testing.T) {
	alpha := lead(t, "alpha")
	p := goodProfile()
	canonical, err := marshalCompact(p)
	if err != nil {
		t.Fatal(err)
	}
	head := `{"manager":"ataniq","requested":` + string(canonical)
	for name, payload := range map[string]string{
		"in nested metadata":       head + `,"meta":{"source":{"a":1,"a":1}}}`,
		"an alias in nested array": head + `,"meta":[{"note":"x","Note":"x"}]}`,
		"a metadata member twice":  head + `,"meta":1,"meta":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			r := repeatedDefine(t, alpha, payload)
			e, err := r.Get(alpha)
			if err != nil || e.Requested != p {
				t.Fatalf("%+v %v", e, err)
			}
			if !e.foreignProfile {
				t.Fatal("a canonical profile beside a repeated member is taken as this build's")
			}
			if _, err := r.BeginLaunch(alpha); !errors.Is(err, ErrLaunchProfile) {
				t.Fatalf("BeginLaunch = %v, want ErrLaunchProfile", err)
			}
		})
	}
	// The same record without the repeat is this build's profile, beside
	// metadata it does not know: it launches.
	r := repeatedDefine(t, alpha, head+`,"meta":{"source":{"a":1}}}`)
	if _, err := r.BeginLaunch(alpha); err != nil {
		t.Fatalf("BeginLaunch without the repeat: %v", err)
	}
}

// rawDefine appends to a fresh journal a define record for alpha whose
// envelope after its role is the raw JSON members given (a payload member
// among them), chained as a writer would chain it, and opens the result.
func rawDefine(t *testing.T, alpha leadrole.Role, members string) *Registry {
	t.Helper()
	dir := regDir(t)
	openReg(t, dir).Close()
	data, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	lines := journalLines(data)
	_, prev, err := decodeLine(genesis, 0, bytes.TrimSuffix(lines[0], []byte("\n")))
	if err != nil {
		t.Fatal(err)
	}
	role, err := json.Marshal(alpha)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte(`{"seq":1,"time":"` + (&fakeClock{}).Now().UTC().Format(timeLayout) + `","kind":"define","role":` + string(role) + `,` + members + `}`)
	if !json.Valid(content) {
		t.Fatalf("not JSON: %s", content)
	}
	sum := chain(prev, content)
	line := append(append([]byte(nil), content[:len(content)-1]...), `,"hash":"`+hex.EncodeToString(sum[:])+`"}`+"\n"...)
	return openReg(t, writeJournal(t, append(data, line...)))
}

// TestRepeatedPayloadIsNotLaunched is the reviewer's case of the envelope: a
// correctly chained define line with two payload members (or a payload and
// its case alias). The envelope's decoding keeps the last, which is this
// build's own profile, so a check of that payload alone finds nothing, though
// the earlier one carried a member this build does not know or metadata. The
// line replays, the role shows the last payload, and it is not launched
// until it is defined again.
func TestRepeatedPayloadIsNotLaunched(t *testing.T) {
	alpha := lead(t, "alpha")
	p := goodProfile()
	canonical, err := marshalCompact(p)
	if err != nil {
		t.Fatal(err)
	}
	newer, err := json.Marshal(newerRequested{
		Provider: p.Provider, Version: p.Version, Model: p.Model, Effort: p.Effort,
		PermissionMode: p.PermissionMode, AccessMode: p.AccessMode,
		Future: futureMember{Name: "a-restriction", Digest: hex.EncodeToString(make([]byte, 32))},
	})
	if err != nil {
		t.Fatal(err)
	}
	last := `{"manager":"ataniq","requested":` + string(canonical) + `}`
	for name, first := range map[string]string{
		"an unknown profile member": `"payload":{"manager":"ataniq","requested":` + string(newer) + `}`,
		"harmless metadata":         `"payload":{"manager":"ataniq","requested":` + string(canonical) + `,"note":"x"}`,
		"a case alias":              `"Payload":{"manager":"ataniq","requested":` + string(newer) + `}`,
	} {
		t.Run(name, func(t *testing.T) {
			r := rawDefine(t, alpha, first+`,"payload":`+last)
			e, err := r.Get(alpha)
			if err != nil || e.Requested != p {
				t.Fatalf("%+v %v", e, err)
			}
			if !e.foreignProfile {
				t.Fatal("an ambiguous envelope is taken as this build's profile")
			}
			if _, err := r.BeginLaunch(alpha); !errors.Is(err, ErrLaunchProfile) {
				t.Fatalf("BeginLaunch = %v, want ErrLaunchProfile", err)
			}
			okf(t)(r.Define(alpha, ManagerAtaniq, p))
			if _, err := r.BeginLaunch(alpha); err != nil {
				t.Fatalf("BeginLaunch after the define: %v", err)
			}
		})
	}
	// The reviewer's case of the hash member: a canonical payload with a
	// "hash" member (or its case alias) before the line's final one. The
	// content alone has no repeat; the line has.
	for _, name := range []string{"hash", "Hash"} {
		t.Run("an earlier "+name+" member", func(t *testing.T) {
			r := rawDefine(t, alpha, `"payload":`+last+`,"`+name+`":"`+hex.EncodeToString(make([]byte, 32))+`"`)
			e, err := r.Get(alpha)
			if err != nil || e.Requested != p || !e.foreignProfile {
				t.Fatalf("%+v %v", e, err)
			}
			if _, err := r.BeginLaunch(alpha); !errors.Is(err, ErrLaunchProfile) {
				t.Fatalf("BeginLaunch = %v, want ErrLaunchProfile", err)
			}
		})
	}
	// One payload member, as a writer makes it: launched.
	r := rawDefine(t, alpha, `"payload":`+last)
	if _, err := r.BeginLaunch(alpha); err != nil {
		t.Fatalf("BeginLaunch of one payload: %v", err)
	}
}

// TestRepeatsMember: the names are compared as decoded and folded as
// encoding/json folds them; names in different objects, and equal strings that
// are values and not names, are no repeat.
func TestRepeatsMember(t *testing.T) {
	for doc, want := range map[string]bool{
		`{}`:  false,
		`[]`:  false,
		`"x"`: false,
		`{"a":1,"b":{"a":1},"c":[{"a":1},{"a":2}]}`: false,
		`{"a":"a","b":["a","a"]}`:                   false,
		`{"a":{},"b":[],"c":[[],{}]}`:               false,
		`{"a":1,"a":2}`:                             true,
		`{"a":1,"A":2}`:                             true,
		`{"\u0061":1,"a":2}`:                        true,
		`{"x":[1,{"k":1,"K":2}]}`:                   true,
		`{"x":{"y":{"z":{"q":1,"q":1}}}}`:           true,
		`{"x":[[{"ok":1}],{"s":1,"ſ":2}]}`:          true, // long s folds to S
		`{"k":1,"K":2}`:                             true, // the Kelvin sign folds to K
		`{"a-b":1,"a_b":2}`:                         false,
	} {
		got, err := repeatsMember([]byte(doc))
		if err != nil || got != want {
			t.Errorf("%s: %v %v, want %v", doc, got, err, want)
		}
	}
}

// TestFoldNameIsEncodingJSONs: two names fold to one exactly when
// encoding/json decodes the one into a field named by the other.
func TestFoldNameIsEncodingJSONs(t *testing.T) {
	names := []string{"requested", "Requested", "REQUESTED", "requeSted", "k", "K", "K", "s", "S", "ſ",
		"é", "É", "ẞ", "ß", "ss", "a_b", "a-b", "ab", "σ", "Σ", "ς", "ı", "I", "i", "İ"}
	for _, field := range names {
		typ := reflect.StructOf([]reflect.StructField{{
			Name: "F", Type: reflect.TypeOf(0), Tag: reflect.StructTag(`json:"` + field + `"`),
		}})
		for _, member := range names {
			doc, err := json.Marshal(map[string]int{member: 1})
			if err != nil {
				t.Fatal(err)
			}
			v := reflect.New(typ)
			if err := json.Unmarshal(doc, v.Interface()); err != nil {
				t.Fatal(err)
			}
			matched := v.Elem().Field(0).Int() == 1
			if folded := foldName(member) == foldName(field); folded != matched {
				t.Errorf("member %q, field %q: encoding/json matches %v, foldName says %v", member, field, matched, folded)
			}
		}
	}
}
