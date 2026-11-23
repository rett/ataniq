package brief

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"ataniq/internal/genesis/internal/canon"
)

func checkRefusal(t testing.TB, err error) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("the refusal is a %T, not an *Error: %v", err, err)
	}
	if e.Code == "" || e.Detail == "" || !canon.Plain(e.Detail) {
		t.Fatalf("the refusal has code %q and detail %q", e.Code, e.Detail)
	}
}

// FuzzDecode feeds the decoder any bytes. What must hold: it does not
// panic; a refusal is an *Error with a code and a plain detail; a brief
// that decodes is valid, encodes in no more bytes than it came in and
// within its cap, and its canonical form decodes to the same brief and
// encodes to itself.
func FuzzDecode(f *testing.F) {
	for _, name := range []string{"brief-valid.json", "brief-valid.pretty.json"} {
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	v := valid(f)
	f.Add([]byte(strings.Replace(v, `"rounds":3`, `"rounds":3.5`, 1)))
	f.Add([]byte(strings.Replace(v, `"notes":[`, `"notes":["`+keyShape(f, "assign-short")+`",`, 1)))
	f.Add([]byte(`{"a":{"other":"x"}}`))
	f.Add([]byte(nil))
	f.Fuzz(func(t *testing.T, data []byte) {
		b, err := Decode(data)
		if err != nil {
			checkRefusal(t, err)
			return
		}
		if err := b.Validate(); err != nil {
			t.Fatalf("a decoded brief is not valid: %v", err)
		}
		out, err := Encode(b)
		if err != nil {
			t.Fatalf("a decoded brief does not encode: %v", err)
		}
		if len(out) > len(data) || len(out) > MaxBriefBytes {
			t.Fatalf("%d bytes encode to %d", len(data), len(out))
		}
		back, err := Decode(out)
		if err != nil || !reflect.DeepEqual(back, b) {
			t.Fatalf("the canonical form decodes to another brief (%v)", err)
		}
		if again, err := Encode(back); err != nil || !bytes.Equal(again, out) {
			t.Fatalf("the canonical form is not stable (%v)", err)
		}
		for _, f := range b.Fields {
			if f.Confirmed && f.Provenance != SupervisorInferred {
				t.Fatal("a decoded field is confirmed without being inferred")
			}
		}
	})
}
