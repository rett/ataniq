package wire_test

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"ataniq/internal/leadholder/wire"
)

// These fixtures contain the payload bytes, including the bytes hashed in
// the intent. Pass them through the real frame encoder and reader.
func preStartFrame(t *testing.T, dir wire.Direction, payload []byte) []byte {
	t.Helper()
	typ := wire.TypeRequest
	if dir == wire.ControlResponses {
		typ = wire.TypeResponse
	}
	var buf bytes.Buffer
	if err := wire.Write(&buf, dir, typ, payload); err != nil {
		t.Fatal(err)
	}
	frame, err := wire.NewReader(&buf, dir).Next()
	if err != nil || frame.Type != typ || buf.Len() != 0 {
		t.Fatalf("frame type %q, remaining %d, error %v", frame.Type, buf.Len(), err)
	}
	return frame.Payload
}

func preStartFixture(t *testing.T, name string, dir wire.Direction) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "prestart", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return preStartFrame(t, dir, b)
}

func expectBytes(t *testing.T, want, got []byte, err error) {
	t.Helper()
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("got %s, error %v; want %s", got, err, want)
	}
}

func testPreStartVersions(t *testing.T) {
	offerBytes := preStartFixture(t, "offer-v2", wire.ControlRequests)
	offer, err := wire.DecodeControlOffer(offerBytes)
	if err != nil {
		t.Fatal(err)
	}
	helloBytes := preStartFixture(t, "hello-v2", wire.ControlResponses)
	hello, err := wire.DecodeControlHello(helloBytes)
	if err != nil {
		t.Fatal(err)
	}
	specBytes := preStartFixture(t, "launch-spec-v1", wire.ControlRequests)
	want := wire.LaunchRequirements{
		Build:      wire.HolderBuild{Module: "ataniq", Version: "v1.0.0", Revision: "abc123"},
		Pack:       wire.PackIdentity{Name: "sample@1.0.0+r1", DeclarationDigest: "b42d849b14aa2fd6ad03d886d0ab30efa195bfbba30478f850394e164d417e5f"},
		SpecDigest: "9fa09898a45171af266102ce216972f45e9b5bd4f72b574538f51f88769970a0",
		HookFrame:  1, Session: true,
	}
	t.Run("N-N", func(t *testing.T) {
		b, err := wire.EncodeControlOffer(offer)
		expectBytes(t, offerBytes, b, err)
		v, err := wire.NegotiateVersion(offer.Range, hello.Range)
		if err != nil || v != 2 {
			t.Fatalf("selected %d, %v", v, err)
		}
		h := hello
		h.Selected = v
		b, err = wire.EncodeControlHello(h)
		expectBytes(t, helloBytes, b, err)
		if got, err := wire.DisplayVersion(offer.Range, &h); err != nil || got != 2 {
			t.Fatalf("display version %d, %v", got, err)
		}
		s, err := wire.NegotiateLaunch(offer.Range, &h, want, specBytes)
		if err != nil || s.Pack != want.Pack || s.Epoch != 7 || string(s.TrustFile) != "{}\n" {
			t.Fatalf("negotiated spec %+v, %v", s, err)
		}
		b, err = wire.EncodeLaunchSpec(s)
		expectBytes(t, specBytes, b, err)
	})

	statusRequest := []byte(`{"op":"status","epoch":7}`)
	statusAnswer := preStartFixture(t, "status-v1", wire.ControlResponses)
	t.Run("N-daemon_previous-holder", func(t *testing.T) {
		// The v1 holder's existing decoder refuses the pre-start offer.
		if _, err := wire.DecodeRequest(offerBytes); !errors.Is(err, wire.ErrInvalid) {
			t.Fatalf("legacy request decoder: %v", err)
		}
		v, err := wire.DisplayVersion(offer.Range, nil)
		if err != nil || v != 1 {
			t.Fatalf("legacy display version %d, %v", v, err)
		}
		b, err := wire.EncodeRequest(wire.Request{Op: wire.OpStatus, Epoch: 7, Version: int(v)})
		expectBytes(t, statusRequest, b, err)
		if r, err := wire.DecodeResponse(statusAnswer); err != nil || !r.OK || r.Status == nil || r.Status.Lead != "project/lead" {
			t.Fatalf("legacy display: %+v, %v", r, err)
		}
		_, err = wire.NegotiateLaunch(offer.Range, nil, want, specBytes)
		if err != wire.RefuseLaunchVersion {
			t.Fatalf("legacy launch: %v", err)
		}
	})
	t.Run("previous-daemon_N-holder", func(t *testing.T) {
		r, err := wire.DecodeRequest(preStartFrame(t, wire.ControlRequests, statusRequest))
		if err != nil || r.Op != wire.OpStatus || r.FrameVersion() != 1 {
			t.Fatalf("v1 request: %+v, %v", r, err)
		}
		legacy := wire.VersionRange{Min: 1, Max: 1}
		v, err := wire.NegotiateVersion(legacy, hello.Range)
		if err != nil || v != 1 {
			t.Fatalf("display version %d, %v", v, err)
		}
		s := sampleStatus
		s.Runtime, s.Title = "sample", "sample"
		s.Facts.TUIExited, s.Facts.TreeGone = wire.FactNo, wire.FactUnknown
		s.WriterPresent, s.Counters = false, nil
		b, err := wire.EncodeResponse(wire.Response{OK: true, Status: &s, Version: int(v)})
		expectBytes(t, statusAnswer, b, err)
		if _, err := wire.DecodeResponse(helloBytes); !errors.Is(err, wire.ErrInvalid) {
			t.Fatalf("legacy response decoder accepted hello: %v", err)
		}
		h := hello
		h.Selected = v
		if _, err := wire.NegotiateLaunch(legacy, &h, want, specBytes); err != wire.RefuseLaunchVersion {
			t.Fatalf("v1 launch: %v", err)
		}
	})

	for _, tc := range []struct {
		name, old, replacement string
		reason                 wire.NegotiationRefusal
	}{
		{"build", "abc123", "def456", wire.RefuseBuild},
		{"holder spec digest", want.SpecDigest, strings.Repeat("a", 64), wire.RefuseSpecDigest},
		{"pack release", "1.0.0+r1", "1.0.0+r2", wire.RefusePack},
		{"declaration digest", want.Pack.DeclarationDigest, strings.Repeat("b", 64), wire.RefusePack},
		{"session", `"session":true`, `"session":false`, wire.RefuseCapability},
		{"hook frame", `"hook_frames":[1]`, `"hook_frames":[2]`, wire.RefuseCapability},
		{"downgraded", `"selected":2`, `"selected":1`, wire.RefuseSelection},
		{"disjoint", `"min":1,"max":2},"selected":2`, `"min":3,"max":4},"selected":3`, wire.RefuseNoVersion},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := strings.Replace(string(helloBytes), tc.old, tc.replacement, 1)
			h, err := wire.DecodeControlHello(preStartFrame(t, wire.ControlResponses, []byte(b)))
			if err != nil {
				t.Fatal(err)
			}
			got, err := wire.NegotiateLaunch(offer.Range, &h, want, specBytes)
			if err != tc.reason || !reflect.DeepEqual(got, wire.LaunchSpec{}) {
				t.Fatalf("got %+v, %v, want refusal %v", got, err, tc.reason)
			}
		})
	}
	t.Run("spec digest", func(t *testing.T) {
		b := bytes.Replace(specBytes, []byte(`"epoch":7`), []byte(`"epoch": 7`), 1)
		if _, err := wire.NegotiateLaunch(offer.Range, &hello, want, b); err != wire.RefuseSpecDigest {
			t.Fatalf("changed bytes: %v", err)
		}
	})
	t.Run("pack in spec", func(t *testing.T) {
		b := bytes.Replace(specBytes, []byte(want.Pack.Name), []byte("other@1.0.0+r1"), 1)
		w := want
		w.SpecDigest = fmt.Sprintf("%x", sha256.Sum256(b))
		h := hello
		h.SpecDigest = w.SpecDigest
		if _, err := wire.NegotiateLaunch(offer.Range, &h, w, b); err != wire.RefusePack {
			t.Fatalf("other pack in digest-checked spec: %v", err)
		}
	})
}

func TestPreStartRejectsInvalidBuffers(t *testing.T) {
	offer := string(preStartFixture(t, "offer-v2", wire.ControlRequests))
	hello := string(preStartFixture(t, "hello-v2", wire.ControlResponses))
	spec := string(preStartFixture(t, "launch-spec-v1", wire.ControlRequests))
	for _, tc := range []struct {
		name, payload, old, replacement string
	}{
		{"offer unknown", offer, `"min":1`, `"Min":1`},
		{"offer duplicate", offer, `"min":1`, `"min":1,"min":2`},
		{"offer reversed", offer, `"min":1`, `"min":3`},
		{"offer version", offer, `"version":2`, `"version":3`},
		{"hello version", hello, `"version":2`, `"version":3`},
		{"hello unknown", hello, `"session":true`, `"extra":0,"session":true`},
		{"hello duplicate", hello, `"modified":false`, `"modified":true,"modified":false`},
		{"hello session missing", hello, `,"session":true`, ""},
		{"hello hooks null", hello, `[1]`, `null`},
		{"hello hooks repeated", hello, `[1]`, `[1,1]`},
		{"hello build too long", hello, "abc123", strings.Repeat("a", 65)},
		{"spec version", spec, `"version":1`, `"version":2`},
		{"spec unknown", spec, `"dir":true`, `"dir":true,"class":"ask"`},
		{"spec duplicate", spec, `"dir":true`, `"dir":true,"dir":false`},
		{"spec long argument", spec, `"sample","--model"`, `"` + strings.Repeat("s", 4097) + `","--model"`},
		{"spec profile digest", spec, "0a246cd7", "1a246cd7"},
		{"spec byte array", spec, `"e30K"`, `[123,125,10]`},
		{"spec trailing", spec, `"e30K"}`, `"e30K"}{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := []byte(strings.Replace(tc.payload, tc.old, tc.replacement, 1))
			if string(b) == tc.payload {
				t.Fatal("mutation missed the fixture")
			}
			var err error
			switch tc.payload {
			case offer:
				_, err = wire.DecodeControlOffer(b)
			case hello:
				_, err = wire.DecodeControlHello(b)
			case spec:
				_, err = wire.DecodeLaunchSpec(b, fmt.Sprintf("%x", sha256.Sum256(b)))
			}
			if !errors.Is(err, wire.ErrInvalid) {
				t.Fatalf("got %v, want invalid payload", err)
			}
		})
	}
	oversize := bytes.Repeat([]byte(" "), wire.MaxControlPayload+1)
	_, a := wire.DecodeControlOffer(oversize)
	_, b := wire.DecodeControlHello(oversize)
	_, c := wire.DecodeLaunchSpec(oversize, "")
	for _, err := range []error{a, b, c} {
		if !errors.Is(err, wire.ErrTooLarge) {
			t.Fatalf("oversized buffer: %v", err)
		}
	}
}

func TestNegotiateVersion(t *testing.T) {
	for _, tc := range []struct {
		a, b    wire.VersionRange
		version uint16
		err     error
	}{
		{wire.VersionRange{Min: 1, Max: 3}, wire.VersionRange{Min: 2, Max: 4}, 3, nil},
		{wire.VersionRange{Min: 1, Max: 2}, wire.VersionRange{Min: 2, Max: 4}, 2, nil},
		{wire.VersionRange{Min: 1, Max: 2}, wire.VersionRange{Min: 3, Max: 4}, 0, wire.RefuseNoVersion},
		{wire.VersionRange{}, wire.VersionRange{Min: 1, Max: 1}, 0, wire.RefuseRange},
	} {
		for _, pair := range [][2]wire.VersionRange{{tc.a, tc.b}, {tc.b, tc.a}} {
			if v, err := wire.NegotiateVersion(pair[0], pair[1]); v != tc.version || err != tc.err {
				t.Errorf("%v: got %d, %v; want %d, %v", pair, v, err, tc.version, tc.err)
			}
		}
	}
}

func TestControlHelloRejectsAmbiguousPacks(t *testing.T) {
	payload := preStartFixture(t, "hello-v2", wire.ControlResponses)
	h, err := wire.DecodeControlHello(payload)
	if err != nil {
		t.Fatal(err)
	}
	p := h.Packs[0]
	p.Name = "sample@1.0.0+r2"
	h.Packs = append(h.Packs, p)
	if b, err := wire.EncodeControlHello(h); b != nil || !errors.Is(err, wire.ErrInvalid) {
		t.Fatalf("encoded repeated pack name: %s, %v", b, err)
	}
}
