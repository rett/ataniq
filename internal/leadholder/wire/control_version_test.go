package wire_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/leadholder/wire"
)

// TestControlFrameWithoutVersionIsV1: a request or a response without the
// version member, which is every frame an earlier build wrote, reads as
// version 1; one that says version 1 reads the same.
func TestControlFrameWithoutVersionIsV1(t *testing.T) {
	for name, in := range map[string]string{
		"request without a version": `{"op":"status","epoch":3}`,
		"request of version 1":      `{"op":"status","epoch":3,"version":1}`,
		"version first":             `{"version":1,"op":"status","epoch":3}`,
	} {
		r, err := wire.DecodeRequest([]byte(in))
		if err != nil || r.FrameVersion() != 1 || r.Op != wire.OpStatus || r.Epoch != 3 {
			t.Errorf("%s: %+v %v", name, r, err)
		}
	}
	for name, in := range map[string]string{
		"response without a version": `{"ok":true}`,
		"response of version 1":      `{"ok":false,"code":"busy","version":1}`,
	} {
		r, err := wire.DecodeResponse([]byte(in))
		if err != nil || r.FrameVersion() != 1 {
			t.Errorf("%s: %+v %v", name, r, err)
		}
	}
	if v := (wire.Request{}).FrameVersion(); v != 1 || wire.ControlVersion != 1 {
		t.Fatalf("the zero request is version %d", v)
	}
}

// TestControlFrameOfAnotherVersionIsRefused: this build reads version 1 only;
// an explicit 0, a null and a later version are refused like any frame it
// cannot read, on decode and on encode.
func TestControlFrameOfAnotherVersionIsRefused(t *testing.T) {
	for _, v := range []string{"0", "2", "null", "-1", `"1"`, "1.5"} {
		if _, err := wire.DecodeRequest([]byte(`{"op":"status","epoch":3,"version":` + v + `}`)); !errors.Is(err, wire.ErrInvalid) {
			t.Errorf("request version %s: %v", v, err)
		}
		if _, err := wire.DecodeResponse([]byte(`{"ok":true,"version":` + v + `}`)); !errors.Is(err, wire.ErrInvalid) {
			t.Errorf("response version %s: %v", v, err)
		}
	}
	if _, err := wire.EncodeRequest(wire.Request{Op: wire.OpStatus, Epoch: 1, Version: 2}); !errors.Is(err, wire.ErrInvalid) {
		t.Errorf("encode a request of version 2: %v", err)
	}
	if _, err := wire.EncodeResponse(wire.Response{OK: true, Version: 2}); !errors.Is(err, wire.ErrInvalid) {
		t.Errorf("encode a response of version 2: %v", err)
	}
}

// TestControlFrameV1IsWrittenWithoutTheMember: a version 1 frame, the version
// given or not, is written without the member, so it is byte for byte what a
// build before the member wrote and what its strict decoder reads.
func TestControlFrameV1IsWrittenWithoutTheMember(t *testing.T) {
	for _, v := range []int{0, 1} {
		b, err := wire.EncodeRequest(wire.Request{Op: wire.OpStop, Epoch: 2, CommandID: "c-1", Version: v})
		if err != nil || string(b) != `{"op":"stop","epoch":2,"command_id":"c-1"}` {
			t.Errorf("request, version %d: %s %v", v, b, err)
		}
		b, err = wire.EncodeResponse(wire.Response{OK: false, Code: "busy", Text: "t", Version: v})
		if err != nil || string(b) != `{"ok":false,"code":"busy","text":"t"}` {
			t.Errorf("response, version %d: %s %v", v, b, err)
		}
	}
	// The golden requests of the canary slice, which the daemon and the holder
	// tests hold byte for byte, decode as version 1 and encode as they are.
	for _, name := range []string{"ticket-request.json", "inject-request.json"} {
		golden, err := os.ReadFile(filepath.Join("testdata", "canary", name))
		if err != nil {
			t.Fatal(err)
		}
		r, err := wire.DecodeRequest(golden)
		if err != nil || r.FrameVersion() != 1 {
			t.Fatalf("%s: %+v %v", name, r, err)
		}
		again, err := wire.EncodeRequest(r)
		if err != nil || !bytes.Equal(again, golden) {
			t.Fatalf("%s is not encoded as it was:\n got %s\nwant %s", name, again, golden)
		}
		if strings.Contains(string(golden), `"version"`) {
			t.Fatalf("%s has a version member", name)
		}
	}
}
