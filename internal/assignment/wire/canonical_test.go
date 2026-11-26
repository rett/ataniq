package wire

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"ataniq/internal/assignment"
)

func TestCanonicalJSON(t *testing.T) {
	for input, want := range map[string]string{
		`{"\ue000": {}, "\ud800\udc00": [], "é":[true,false,null,-3,-0]}`:   `{"é":[true,false,null,-3,0],"𐀀":[],"":{}}`,
		`"\u000B\u0000\u0008\u0009\u000a\u000c\u000d\/\"\\<>&\u2028\u2029"`: `"\u000b\u0000\b\t\n\f\r/\"\\<>&` + "\u2028\u2029\"",
		`["\\udfff","�","e\u0301","é"]`:                                     `["\\udfff","�","é","é"]`,
	} {
		got, err := Canonicalize([]byte(input))
		if err != nil || string(got) != want {
			t.Fatalf("%s: got %q, %v, want %q", input, got, err, want)
		}
	}
	for _, bad := range []string{`[{"a":1,"\u0061":2}]`, `1.0`, `1e2`, `01`, `"\ud800"`, `"\udfff"`,
		`"\ud800\ud800"`, `"\ud800\\udc00"`, `9007199254740992`, `true false`, "\"\xff\""} {
		if _, err := Canonicalize([]byte(bad)); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func sample() assignment.PublicEnvelope {
	return assignment.PublicEnvelope{Schema: 1, Algorithm: "Ed25519", EvidenceDigests: []string{},
		EventID: strings.Repeat("a", 64), Kind: assignment.Seed, Phase: assignment.PhaseCommittedClosed,
		Revision: "1", Generation: "1", CommitRevision: "1", PreviousGeneration: "0", PreviousRevision: "0"}
}

func TestProjection(t *testing.T) {
	p := sample()
	p.ProjectID = "<a>\u2028\\u2028😀"
	p.Revision = "18446744073709551615"
	for _, sig := range []string{"", "example\narmor"} {
		p.Signature = sig
		body, err := Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.HasPrefix(body, []byte(`{"admission":false,`)) {
			t.Fatal(string(body))
		}
		reordered := append(bytes.Clone(body[len(`{"admission":false`):len(body)-1]), `,  "admission" : false}`...)
		reordered[0] = '{'
		decoded, err := Unmarshal(reordered)
		if err != nil || !reflect.DeepEqual(decoded, p) {
			t.Fatalf("%+v, %v", decoded, err)
		}
	}
	p.ProjectID = "\xff"
	if _, err := Marshal(p); err == nil {
		t.Fatal("encoded malformed UTF-8")
	}
}

func TestProjectionRefusals(t *testing.T) {
	b, err := Marshal(sample())
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{
		{`"admission":false,`, ""}, {`"admission":false`, `"admission":null`},
		{`"admission"`, `"Admission"`}, {`"id":"",`, `"extra":0,"id":"",`},
		{`"id":""`, `"id":"","\u0069d":""`}, {`"evidence_digests":[]`, `"evidence_digests":null`},
		{`"schema":1`, `"schema":2`}, {`"schema":1`, `"schema":1.0`}, {`"schema":1`, `"schema":"1"`},
		{`"Seed"`, `"Unknown"`}, {`"Ed25519"`, `"other"`}, {`"revision":"1"`, `"revision":1`},
		{`"revision":"1"`, `"revision":"00"`}, {`"revision":"1"`, `"revision":"18446744073709551616"`},
		{`"role_id":""`, `"role_id":"\udfff"`}, {`"role_id":""`, `"signature":"","role_id":""`},
	} {
		s := strings.Replace(string(b), pair[0], pair[1], 1)
		if _, err := Unmarshal([]byte(s)); err == nil {
			t.Fatalf("accepted %s", s)
		}
	}
	for _, s := range []string{"", string(b) + "\n", " " + string(b), string(b[:len(b)-1]), string(b) + string(b), "{\n" + string(b[1:])} {
		if _, err := Unmarshal([]byte(s)); err == nil {
			t.Fatalf("accepted malformed frame %q", s)
		}
	}
	p := sample()
	p.ProjectID = strings.Repeat("x", recordLimit-len(b))
	full, err := Marshal(p)
	if err != nil || len(full) != recordLimit {
		t.Fatal(len(full), err)
	}
	if _, err := Unmarshal(full); err != nil {
		t.Fatal(err)
	}
	if _, err := Unmarshal(append(full, ' ')); !errors.Is(err, assignment.ErrLimit) {
		t.Fatal(err)
	}
	p.ProjectID += "x"
	if _, err := Marshal(p); !errors.Is(err, assignment.ErrLimit) {
		t.Fatal(err)
	}
}

func FuzzCanonicalDecoder(f *testing.F) {
	b, err := Marshal(sample())
	if err != nil {
		f.Fatal(err)
	}
	for _, s := range []string{string(b), `[{"b":0,"a":"\ud800\udc00"}]`, `{"a":0,"a":1}`, `"\udfff"`, `-0`, ""} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		c, err := Canonicalize(b)
		if err != nil {
			return
		}
		again, err := Canonicalize(c)
		if err != nil || !bytes.Equal(c, again) {
			t.Fatal("unstable canonicalization", err)
		}
		if p, err := Unmarshal(b); err == nil {
			got, err := Marshal(p)
			if err != nil || !bytes.Equal(got, c) {
				t.Fatal("unstable projection", err)
			}
		}
	})
}
