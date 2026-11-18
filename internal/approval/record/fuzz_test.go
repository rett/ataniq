package record

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
)

// The fuzz tests feed each decoder any bytes. Their seed corpus is every
// vector of the decoder's kind, valid and invalid, so they run as ordinary
// tests on the vectors.
//
// What must hold for any input: the decoder does not panic; a refusal is an
// *Error with a code and a detail that could be shown as it is; and a
// record that decodes can be encoded, in no more bytes than it came in and
// within its cap, to bytes that decode to the same record and encode to
// themselves.

func seed(f *testing.F, kind string) {
	f.Helper()
	names := vectorNames(f, kind+"-*.json")
	if len(names) < 7 {
		f.Fatalf("%d vectors for a %s, want the whole corpus", len(names), kind)
	}
	for _, name := range names {
		f.Add(readVector(f, name))
	}
	f.Add([]byte(nil))
	f.Add([]byte(`{"b64":"/w=="}`))
}

func checkRefusal(t *testing.T, err error) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("the refusal is a %T, not an *Error: %v", err, err)
	}
	if e.Code == "" || e.Detail == "" || !plain(e.Detail) {
		t.Fatalf("the refusal has code %q and detail %q", e.Code, e.Detail)
	}
}

func FuzzDecodeRequest(f *testing.F) {
	seed(f, "request")
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := DecodeRequest(data)
		if err != nil {
			checkRefusal(t, err)
			return
		}
		out, err := EncodeRequest(r)
		if err != nil {
			t.Fatalf("a decoded request does not encode: %v", err)
		}
		if len(out) > len(data) || len(out) > MaxRequestBytes {
			t.Fatalf("%d bytes encode to %d", len(data), len(out))
		}
		back, err := DecodeRequest(out)
		if err != nil || !reflect.DeepEqual(back, r) {
			t.Fatalf("the canonical form decodes to another request (%v)", err)
		}
		if again, err := EncodeRequest(back); err != nil || !bytes.Equal(again, out) {
			t.Fatalf("the canonical form is not stable (%v)", err)
		}
	})
}

func FuzzDecodePending(f *testing.F) {
	seed(f, "pending")
	f.Fuzz(func(t *testing.T, data []byte) {
		p, err := DecodePending(data)
		if err != nil {
			checkRefusal(t, err)
			return
		}
		out, err := EncodePending(p)
		if err != nil {
			t.Fatalf("a decoded pending record does not encode: %v", err)
		}
		if len(out) > len(data) || len(out) > MaxPendingBytes {
			t.Fatalf("%d bytes encode to %d", len(data), len(out))
		}
		back, err := DecodePending(out)
		if err != nil || !reflect.DeepEqual(back, p) {
			t.Fatalf("the canonical form decodes to another pending record (%v)", err)
		}
		if again, err := EncodePending(back); err != nil || !bytes.Equal(again, out) {
			t.Fatalf("the canonical form is not stable (%v)", err)
		}
		if digest, err := BindingDigest(p.Binding); err != nil || digest != p.BindingSHA256 {
			t.Fatalf("a decoded pending record's binding digest is not its binding's (%v)", err)
		}
	})
}

func FuzzDecodeDecision(f *testing.F) {
	seed(f, "decision")
	f.Fuzz(func(t *testing.T, data []byte) {
		d, err := DecodeDecision(data)
		if err != nil {
			checkRefusal(t, err)
			return
		}
		out, err := EncodeDecision(d)
		if err != nil {
			t.Fatalf("a decoded decision does not encode: %v", err)
		}
		if len(out) > len(data) || len(out) > MaxDecisionBytes {
			t.Fatalf("%d bytes encode to %d", len(data), len(out))
		}
		back, err := DecodeDecision(out)
		if err != nil || !reflect.DeepEqual(back, d) {
			t.Fatalf("the canonical form decodes to another decision (%v)", err)
		}
		if again, err := EncodeDecision(back); err != nil || !bytes.Equal(again, out) {
			t.Fatalf("the canonical form is not stable (%v)", err)
		}
	})
}
