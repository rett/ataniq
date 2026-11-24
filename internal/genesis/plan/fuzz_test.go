package plan

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"ataniq/internal/genesis/brief"
	"ataniq/internal/genesis/internal/canon"
)

func checkRefusal(t testing.TB, err error) {
	t.Helper()
	var e *brief.Error
	if !errors.As(err, &e) {
		t.Fatalf("the refusal is a %T, not a *brief.Error: %v", err, err)
	}
	if e.Code == "" || e.Detail == "" || !canon.Plain(e.Detail) {
		t.Fatalf("the refusal has code %q and detail %q", e.Code, e.Detail)
	}
}

// FuzzDecode feeds the decoder any bytes. What must hold: it does not
// panic; a refusal is a *brief.Error with a code and a plain detail; a plan
// that decodes holds to Check, encodes in no more bytes than it came in and
// within its cap, and its canonical form decodes to the same plan and
// encodes to itself. The envelope's decoder is fed the same bytes, and
// holds the same.
func FuzzDecode(f *testing.F) {
	names, err := filepath.Glob(filepath.Join("testdata", "*.json"))
	if err != nil {
		f.Fatal(err)
	}
	for _, name := range names {
		if strings.HasPrefix(filepath.Base(name), "diff-") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	p := sample(f)
	v := string(encode(f, p))
	f.Add([]byte(v))
	f.Add([]byte(strings.Replace(v, `"plan":1`, `"plan":1.5`, 1)))
	f.Add([]byte(strings.Replace(v, `"depends_on":[]`, `"depends_on":["t1"]`, 1)))
	env, err := EncodeEnvelope(p)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(env)
	f.Add([]byte(`{"plan":{}}`))
	f.Add([]byte(nil))
	f.Fuzz(func(t *testing.T, data []byte) {
		if p, err := Decode(data); err != nil {
			checkRefusal(t, err)
		} else {
			holds(t, p, data, Decode, Encode)
		}
		if p, err := DecodeEnvelope(data); err != nil {
			checkRefusal(t, err)
		} else {
			holds(t, p, data, DecodeEnvelope, EncodeEnvelope)
		}
	})
}

func holds(t *testing.T, p Plan, data []byte, decode func([]byte) (Plan, error), encode func(Plan) ([]byte, error)) {
	t.Helper()
	if err := Check(p); err != nil {
		t.Fatalf("a decoded plan does not hold to Check: %v", err)
	}
	out, err := encode(p)
	if err != nil {
		t.Fatalf("a decoded plan does not encode: %v", err)
	}
	if len(out) > len(data) || len(out) > MaxEnvelopeBytes {
		t.Fatalf("%d bytes encode to %d", len(data), len(out))
	}
	back, err := decode(out)
	if err != nil || !reflect.DeepEqual(back, p) {
		t.Fatalf("the canonical form decodes to another plan (%v)", err)
	}
	if again, err := encode(back); err != nil || !bytes.Equal(again, out) {
		t.Fatalf("the canonical form is not stable (%v)", err)
	}
	for _, n := range p.Notes {
		if brief.SecretShaped(string(n.Text)) {
			t.Fatal("a decoded note is shaped like a key")
		}
	}
}
