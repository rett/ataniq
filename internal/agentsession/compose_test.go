package agentsession

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"testing"
	"time"
)

func claim(b Basis, s State, src ClaimSource) *Claim { return &Claim{State: s, Basis: b, Source: src} }
func TestTC1ToTC9AndTC11TC12(t *testing.T) {
	base := Inputs{Transport: *claim("transport", "idle", ""), Sight: "rules"}
	for _, s := range []State{"busy", "blocked", "idle", "unknown"} {
		in := base
		in.Hook = claim("self", s, "hook")
		if r := Compose(in); r.Deliverable != (s == "idle" || s == "unknown") {
			t.Fatal(s, r)
		}
		in.Hook = nil
		if r := Compose(in); !r.Deliverable || r.Permit != "transport" {
			t.Fatal(r)
		}
	}
	for _, policy := range []SelfDelivery{"", "checked"} {
		for _, sight := range Sight("").All() {
			in := base
			in.Policy.Self = policy
			in.Sight = sight
			for _, s := range []State{"idle", "asking", "unknown", "busy", "blocked"} {
				for _, src := range ClaimSource("").All() {
					in.Transport.State = "unknown"
					in.Self = claim("self", s, src)
					want := policy == "checked" && sight == "rules" && s == "idle" && src == "client"
					if r := Compose(in); r.Deliverable != want || want && r.Permit != "self-checked" {
						t.Fatal(in, r)
					}
				}
			}
			in.Transport.State = "idle"
			in.Self = nil
			if r := Compose(in); r.Deliverable != (sight != "blind") || sight == "blind" && !slices.Contains(r.Refusal, "untested build") {
				t.Fatal(r)
			}
			for _, self := range []*Claim{nil, claim("self", "idle", "client"), claim("self", "busy", "client"), claim("self", "blocked", "client"), claim("self", "asking", "client")} {
				in.Self = self
				in.Veto = Veto{"dialog"}
				for _, hook := range []*Claim{nil, claim("self", "blocked", "hook"), claim("self", "idle", "hook"), nil} {
					in.Hook = hook
					if r := Compose(in); r.Deliverable || r.Refusal[0] != "dialog" {
						t.Fatal(r)
					}
				}
			}
		}
	}
	in := base
	in.Ended = true
	if r := Compose(in); r.State != "ended" || r.Basis != "host" || r.Deliverable {
		t.Fatal(r)
	}
}
func TestTC4TC4aTC5DroppingClaims(t *testing.T) {
	for _, row := range []struct {
		slot Slot
		c    *Claim
		why  DroppedReason
	}{
		{"transport", claim("self", "idle", "hook"), "slot-basis"}, {"transport", claim("self", "idle", ""), "slot-basis"}, {"transport", claim("screen", "asking", ""), "basis-not-allowed"}, {"transport", claim("host", "idle", ""), "basis-not-allowed"}, {"transport", claim("transport", "idle", "client"), "slot-source"}, {"transport", claim("transport", "ended", ""), "basis-not-allowed"},
		{"self", claim("transport", "idle", "client"), "slot-basis"}, {"self", claim("self", "idle", "hook"), "slot-source"}, {"self", claim("self", "ended", "client"), "basis-not-allowed"},
		{"hook", claim("transport", "idle", "hook"), "slot-basis"}, {"hook", claim("self", "idle", "client"), "slot-source"}, {"hook", claim("self", "idle", ""), "slot-source"}, {"hook", claim("self", "asking", "hook"), "hook-asking"}, {"hook", claim("self", "ended", "hook"), "basis-not-allowed"},
	} {
		for _, policy := range []SelfDelivery{"", "checked"} {
			for _, self := range []*Claim{nil, claim("self", "idle", "client")} {
				for _, hook := range []*Claim{nil, claim("self", "idle", "hook")} {
					in := Inputs{Transport: *claim("transport", "unknown", ""), Sight: "rules", Policy: Policy{policy}, Self: self, Hook: hook}
					switch row.slot {
					case "transport":
						in.Transport = *row.c
					case "self":
						in.Self = row.c
					case "hook":
						in.Hook = row.c
					}
					r := Compose(in)
					if !slices.Equal(r.Dropped, []DroppedClaim{{row.slot, row.why}}) || !slices.ContainsFunc(r.Claims, func(c Claim) bool { return reflect.DeepEqual(c, *row.c) }) {
						t.Fatal(row, r)
					}
					if row.slot == "transport" {
						if r.Deliverable || !slices.Contains(r.Refusal, "transport dropped") || slices.Contains(r.Refusal, "transport cannot tell") {
							t.Fatal(r)
						}
						continue
					}
					if row.slot == "self" {
						in.Self = nil
					} else {
						in.Hook = nil
					}
					other := Compose(in)
					r.Dropped = nil
					r.Claims = other.Claims
					if !reflect.DeepEqual(r, other) {
						t.Fatal(row, r, other)
					}
				}
			}
		}
	}
}
func TestTC14DecodeInputs(t *testing.T) {
	raw := []byte(`{"Transport":{"State":"unknown","Basis":"screen"},"Self":{"State":"idle","Basis":"self","Source":"client"},"Sight":"rules","Policy":{"Self":"x"}}`)
	in, err := DecodeInputs(raw)
	if err != nil || in.Policy.Self != "" {
		t.Fatal(in, err)
	}
	before := Compose(in)
	in.Policy.Self = "x"
	if before.Deliverable || !reflect.DeepEqual(before, Compose(in)) {
		t.Fatal(in)
	}
	b, _ := json.Marshal(in)
	if !bytes.Contains(b, []byte(`"Policy":{"Self":""}`)) {
		t.Fatal(string(b))
	}
	for _, tc := range []struct {
		old, new, path string
		reason         InputReason
	}{
		{"rules", "bad", "Inputs.Sight", "unknown-value"}, {"unknown", "", "Inputs.Transport.State", "empty-value"}, {"screen", "bad", "Inputs.Transport.Basis", "unknown-value"},
		{`"Sight"`, `"Sight":"none","Sight"`, "Inputs.Sight", "duplicate"}, {`"State"`, `"state"`, "Inputs.Transport.state", "unknown-value"},
	} {
		bad := bytes.Replace(raw, []byte(tc.old), []byte(tc.new), 1)
		zero, err := DecodeInputs(bad)
		requireError(t, err, tc.path, tc.reason)
		if !reflect.DeepEqual(zero, Inputs{}) {
			t.Fatal(zero)
		}
		var v Inputs
		if json.Unmarshal(bad, &v) == nil && (tc.reason == "empty-value" || tc.reason == "unknown-value" && tc.old != `"State"`) {
			requireError(t, ValidateInputs(v), tc.path, tc.reason)
			if !reflect.DeepEqual(Compose(v), Record{State: "unknown", Refusal: []string{"invalid input"}}) {
				t.Fatal(v)
			}
		}
	}
	for _, suffix := range []byte{'0', ' ', '\n'} {
		_, err = DecodeInputs(append(raw, suffix))
		requireError(t, err, "Inputs", "inconsistent")
	}
}
func TestClaimTimestampAdmission(t *testing.T) {
	base := Inputs{Transport: *claim("screen", "idle", ""), Sight: "rules"}
	for _, slot := range []string{"Transport", "Self", "Hook"} {
		for _, at := range []time.Time{
			{}, time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC),
			time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC),
			time.Date(2025, 1, 1, 0, 0, 0, 0, time.FixedZone("", -24*60*60)),
		} {
			in := base
			switch slot {
			case "Transport":
				in.Transport.At = at
			case "Self":
				in.Self = claim("self", "blocked", "client")
				in.Self.At = at
			case "Hook":
				in.Hook = claim("self", "asking", "hook")
				in.Hook.At = at
			}
			err := ValidateInputs(in)
			r := Compose(in)
			if _, encodeErr := json.Marshal(r); encodeErr != nil {
				t.Errorf("%s: cannot encode decision: %v", slot, encodeErr)
			}
			if _, encodeErr := at.MarshalJSON(); encodeErr != nil {
				requireError(t, err, "Inputs."+slot+".At", "inconsistent")
				if !reflect.DeepEqual(r, Record{State: "unknown", Refusal: []string{"invalid input"}}) {
					t.Fatal(r)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		}
	}
}
func TestDecodeClaimTimestamp(t *testing.T) {
	raw := []byte(`{"Transport":{"State":"idle","Basis":"screen","At":"2025-01-02T03:04:05+02:00"},"Sight":"rules"}`)
	t.Run("unencodable", func(t *testing.T) {
		in, err := DecodeInputs(bytes.Replace(raw, []byte("+02:00"), []byte("+24:00"), 1))
		requireError(t, err, "Inputs.Transport.At", "inconsistent")
		if !reflect.DeepEqual(in, Inputs{}) {
			t.Fatal(in)
		}
	})
	t.Run("independent of local zone", func(t *testing.T) {
		saved := time.Local
		defer func() { time.Local = saved }()
		var claims []Claim
		for _, offset := range []int{0, 2 * 60 * 60} {
			time.Local = time.FixedZone("local", offset)
			in, err := DecodeInputs(raw)
			if err != nil {
				t.Fatal(err)
			}
			r := Compose(in)
			encoded, err := json.Marshal(r)
			if err != nil || !r.Deliverable || !bytes.Contains(encoded, []byte("2025-01-02T03:04:05+02:00")) {
				t.Fatal(r, err)
			}
			claims = append(claims, r.Claims[0])
		}
		if !reflect.DeepEqual(claims[0], claims[1]) {
			t.Fatal(claims)
		}
	})
}

func FuzzAcceptedRecordEncodes(f *testing.F) {
	f.Add([]byte(`{"Transport":{"State":"idle","Basis":"screen","At":"2025-01-02T03:04:05+24:00"},"Sight":"rules"}`))
	f.Add([]byte(`{"Transport":{"State":"idle","Basis":"screen","At":"9999-12-31T23:59:59.999999999Z"},"Sight":"rules"}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		in, err := DecodeInputs(raw)
		if err != nil {
			return
		}
		if _, err := json.Marshal(Compose(in)); err != nil {
			t.Fatal(err)
		}
	})
}

func TestDecodeInputsFirstMember(t *testing.T) {
	for _, row := range []struct {
		raw, path string
		why       InputReason
	}{
		{`{"Transport":{"State":"typo","Basis":"screen"},"Sight":7}`, "Inputs.Transport.State", "unknown-value"},
		{`{"Sight":7,"Transport":{"Basis":"screen","State":"typo"}}`, "Inputs.Transport.State", "unknown-value"},
		{`{"Transport":{"Detail":1,"At":2,"Basis":7}}`, "Inputs.Transport.State", "empty-value"},
		{`{"Transport":{"Basis":7,"State":"typo"}}`, "Inputs.Transport.State", "unknown-value"},
		{`{"Sight":7,"Sight":"rules","Transport":{"State":"typo"}}`, "Inputs.Transport.State", "unknown-value"},
		{`{"Sight":"rules","Sight":1,"Transport":1,"Transport":2}`, "Inputs.Transport", "duplicate"},
		{`{"z":1,"a":1}`, "Inputs.a", "unknown-value"},
		{`{"a":1,"z":1}`, "Inputs.a", "unknown-value"},
	} {
		in, err := DecodeInputs([]byte(row.raw))
		requireError(t, err, row.path, row.why)
		if !reflect.DeepEqual(in, Inputs{}) {
			t.Fatal(in)
		}
	}
}

func TestTC15NoClaims(t *testing.T) {
	in := Inputs{Sight: "rules"}
	requireError(t, ValidateInputs(in), "Inputs.Transport.State", "empty-value")
	want := Record{State: "unknown", Refusal: []string{"invalid input"}}
	if got := Compose(in); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	decoded, err := DecodeInputs([]byte(`{"Sight":"rules"}`))
	requireError(t, err, "Inputs.Transport.State", "empty-value")
	if !reflect.DeepEqual(decoded, Inputs{}) {
		t.Fatal(decoded)
	}
}

func TestTC15RecordGoldens(t *testing.T) {
	b, err := os.ReadFile("sessiontest/testdata/compose.golden")
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		In     Inputs
		Record json.RawMessage
	}
	if err = json.Unmarshal(b, &rows); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		got, err := json.Marshal(Compose(row.In))
		var want bytes.Buffer
		if err != nil || json.Compact(&want, row.Record) != nil || !bytes.Equal(got, want.Bytes()) {
			t.Fatalf("got %s\nwant %s", got, want.Bytes())
		}
	}
}
