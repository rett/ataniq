package agentsession_test

import (
	"bytes"
	"encoding/json"
	"maps"
	"os"
	"strings"
	"testing"

	"ataniq/internal/agentsession"
)

// Byte limits of the frozen ataniq.session-ref/1 grammar.
const (
	authorityLimit = 162
	projectLimit   = 63
	featureLimit   = 63
)

func referenceInputs() (agentsession.RefIdentity, agentsession.LaunchIntent, agentsession.LaunchedPack) {
	return agentsession.RefIdentity{Authority: "slot A", Incarnation: strings.Repeat("0123456789abcdef", 2)},
		agentsession.LaunchIntent{Project: "sample", Role: "sample/lead", ID: strings.Repeat("fedcba9876543210", 2), Epoch: 7},
		agentsession.LaunchedPack{Name: "alpha@1.2.3+build.4+rev5", DeclarationDigest: strings.Repeat("a", 64), SandboxDigest: strings.Repeat("b", 64)}
}

func reference(t *testing.T) agentsession.SessionRef {
	t.Helper()
	ref, err := agentsession.BuildSessionRef(referenceInputs())
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func TestSessionRefGolden(t *testing.T) {
	golden, err := os.ReadFile("sessiontest/testdata/session-ref.golden")
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.TrimSuffix(golden, []byte("\n"))
	ref := reference(t)
	var decoded agentsession.SessionRef
	if err := json.Unmarshal(want, &decoded); err != nil || decoded != ref {
		t.Fatalf("decode golden: %+v, %v", decoded, err)
	}
	got, err := json.Marshal(decoded)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("encode reference: %s, %v", got, err)
	}
}

func TestSessionRefIdentityAndKeys(t *testing.T) {
	identity, intent, pack := referenceInputs()
	ref := reference(t)
	launchKey, groupKey, ok := ref.Keys(identity)
	if !ok || launchKey != (agentsession.PerLaunchKey{Authority: identity.Authority, Launch: intent.ID}) ||
		groupKey != (agentsession.GroupingKey{Authority: identity.Authority, Conversation: intent.ID}) {
		t.Fatal(launchKey, groupKey, ok)
	}

	// Exercise the two projections independently even though S1 builds both
	// IDs from one intent.
	next := ref
	next.Launch = strings.Repeat("1", 32)
	nextLaunch, nextGroup, ok := next.Keys(identity)
	if !ok || launchKey == nextLaunch || groupKey != nextGroup {
		t.Fatal("a new launch of the same conversation must change only the per-launch key")
	}
	identity.Incarnation = strings.Repeat("2", 32)
	reenrolled, err := agentsession.BuildSessionRef(identity, intent, pack)
	if err != nil || reenrolled.StoreIncarnation != identity.Incarnation || reenrolled == ref {
		t.Fatalf("fresh store at the same authority, intent, role and epoch: %+v, %v", reenrolled, err)
	}
	if _, _, ok := reenrolled.Keys(identity); !ok {
		t.Fatal("current store refused its reference")
	}
	if launch, group, ok := ref.Keys(identity); ok || launch != (agentsession.PerLaunchKey{}) || group != (agentsession.GroupingKey{}) {
		t.Fatal("stale reference shared a key with the current store", launch, group)
	}
	for _, id := range []agentsession.RefIdentity{{}, {Authority: identity.Authority}, {Incarnation: identity.Incarnation}} {
		if got, err := agentsession.BuildSessionRef(id, intent, pack); err == nil || got != (agentsession.SessionRef{}) {
			t.Fatalf("incomplete identity produced %+v, %v", got, err)
		}
	}
}

func TestSessionRefStrictObject(t *testing.T) {
	ref := reference(t)
	b, err := json.Marshal(ref)
	if err != nil {
		t.Fatal(err)
	}
	refuses := func(t *testing.T, b []byte) {
		t.Helper()
		got := ref
		if err := json.Unmarshal(b, &got); err == nil || got != ref {
			t.Fatalf("invalid object accepted or destination changed: %s", b)
		}
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(b, &members); err != nil {
		t.Fatal(err)
	}
	for name, raw := range members {
		t.Run(name, func(t *testing.T) {
			refuses(t, []byte(`{"`+name+`":`+string(raw)+","+string(b[1:])))
			if name == "launch" {
				refuses(t, []byte(`{"\u006caunch":`+string(raw)+","+string(b[1:])))
			}
			for _, replacement := range []json.RawMessage{nil, json.RawMessage("null"), json.RawMessage("true")} {
				modified := maps.Clone(members)
				if replacement == nil {
					delete(modified, name)
				} else {
					modified[name] = replacement
				}
				wire, err := json.Marshal(modified)
				if err != nil {
					t.Fatal(err)
				}
				refuses(t, wire)
			}
		})
	}
	for _, prefix := range []string{`{"extra":1,`, `{"Authority":"slot A",`} {
		refuses(t, []byte(prefix+string(b[1:])))
	}
	for _, malformed := range []string{"null", "[]", "{}", string(b) + "{}", string(b[:len(b)-1])} {
		refuses(t, []byte(malformed))
	}
}

func TestSessionRefWireValues(t *testing.T) {
	ref := reference(t)
	wire, err := json.Marshal(ref)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]string{
		"authority":                {`""`, `"slot\u001f"`, `"slot\u007f"`, `"é"`, `"` + strings.Repeat("x", authorityLimit+1) + `"`},
		"store_incarnation":        {`""`, `"` + strings.Repeat("A", 32) + `"`},
		"project":                  {`"-start"`, `"end-"`, `"A"`, `"a/b"`, `"` + strings.Repeat("x", projectLimit+1) + `"`},
		"role":                     {`"lead"`, `"other/lead"`, `"sample/coord/"`, `"sample/coord/bad/feature"`},
		"conversation":             {`"0"`, `"` + strings.Repeat("c", 32) + `"`},
		"launch":                   {`"0"`, `"` + strings.Repeat("C", 32) + `"`},
		"home_generation":          {"0", "2"},
		"conversation_incarnation": {"0", "2"},
		"launch_epoch":             {"0", "-1", "1.5", "18446744073709551616", `"7"`},
		"binding_generation":       {"0", "2"},
		"pack":                     {`"alpha"`, `"A@1+r"`, `"alpha@+rev"`, `"alpha@1+"`, `"alpha@1+bad rev"`, `"alpha@1@2+r"`},
		"pack_digest":              {`"0"`, `"` + strings.Repeat("F", 64) + `"`},
		"sandbox_digest":           {`""`, `"` + strings.Repeat("g", 64) + `"`},
		"transport":                {`"protocol"`, `"headless"`},
		"custody":                  {`"third-party"`},
	}
	for member, inputs := range cases {
		t.Run(member, func(t *testing.T) {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(wire, &fields); err != nil {
				t.Fatal(err)
			}
			for _, input := range inputs {
				fields[member] = json.RawMessage(input)
				b, err := json.Marshal(fields)
				if err != nil {
					t.Fatal(err)
				}
				var decoded agentsession.SessionRef
				if err := json.Unmarshal(b, &decoded); err == nil {
					t.Errorf("accepted %s = %s", member, input)
				}
				// Where the Go type can represent this value, encoding it must
				// enforce the same grammar as decoding it.
				type unchecked agentsession.SessionRef
				var invalid unchecked
				if json.Unmarshal(b, &invalid) == nil {
					if _, err := json.Marshal(agentsession.SessionRef(invalid)); err == nil {
						t.Errorf("encoded %s = %s", member, input)
					}
				}
			}
		})
	}
}

func TestSessionRefGrammarBoundaries(t *testing.T) {
	id, launch, pack := referenceInputs()
	id.Authority = " " + strings.Repeat("~", authorityLimit-1)
	launch.Project = strings.Repeat("x", projectLimit)
	launch.Epoch = ^uint64(0)
	for _, role := range []string{"supervisor", launch.Project + "/lead", launch.Project + "/coord/" + strings.Repeat("0", featureLimit)} {
		launch.Role = role
		got, err := agentsession.BuildSessionRef(id, launch, pack)
		if err != nil || got.Authority != id.Authority || got.Role != role {
			t.Fatalf("build with role %q: %+v, %v", role, got, err)
		}
	}
}
