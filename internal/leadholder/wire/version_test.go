package wire_test

import (
	"errors"
	"strings"
	"testing"

	"ataniq/internal/leadholder/wire"
)

func TestHelloVersion(t *testing.T) {
	good := `{"version":1,"ticket":"` + ticket64 + `","name":"n","columns":80,"rows":24,"colors":{}}`
	for name, c := range map[string]struct {
		in   string
		want int
		ok   bool
	}{
		"ours":                {good, 1, true},
		"another":             {strings.Replace(good, `"version":1`, `"version":2`, 1), 2, true},
		"unknown field":       {`{"version":2,"newthing":true}`, 2, true},
		"unknown nested":      {`{"newthing":{"a":[1,{"b":[[[[[[[[[[2]]]]]]]]]]}]},"version":3,"ticket":5}`, 3, true},
		"unknown first":       {`{"zzz":"x","version":7}`, 7, true},
		"other fields wrong":  {`{"version":4,"name":5,"columns":"wide"}`, 4, true},
		"white space":         {" \n{ \"version\" : 12 }\n", 12, true},
		"trailing text":       {`{"version":2} trailing`, 2, true},
		"largest":             {`{"version":65535}`, 65535, true},
		"escaped key":         {`{"version":9}`, 9, true},
		"empty":               {``, 0, false},
		"not an object":       {`[1]`, 0, false},
		"number":              {`2`, 0, false},
		"null":                {`null`, 0, false},
		"no version":          {`{"ticket":"x"}`, 0, false},
		"empty object":        {`{}`, 0, false},
		"upper-case key":      {`{"Version":2}`, 0, false},
		"version in a nested": {`{"hello":{"version":2}}`, 0, false},
		"string version":      {`{"version":"2"}`, 0, false},
		"float version":       {`{"version":2.5}`, 0, false},
		"exponent version":    {`{"version":1e0}`, 0, false},
		"null version":        {`{"version":null}`, 0, false},
		"object version":      {`{"version":{"n":2}}`, 0, false},
		"array version":       {`{"version":[2]}`, 0, false},
		"zero":                {`{"version":0}`, 0, false},
		"negative":            {`{"version":-1}`, 0, false},
		"too big":             {`{"version":65536}`, 0, false},
		"huge":                {`{"version":99999999999999999999}`, 0, false},
		"repeated version":    {`{"version":1,"version":2}`, 0, false},
		"repeated by escape":  {`{"version":1,"version":1}`, 0, false},
		"truncated":           {`{"version":2,"x":`, 0, false},
		"truncated after":     {`{"version":2`, 0, false},
		"truncated before":    {`{"x":[1,2`, 0, false},
		"deeply nested":       {`{"x":` + strings.Repeat("[", 50000) + strings.Repeat("]", 50000) + `,"version":2}`, 0, false},
		"bad syntax":          {`{"version":2,,}`, 0, false},
		"key not a string":    {`{1:2}`, 0, false},
		"leading garbage":     {`x{"version":2}`, 0, false},
	} {
		got, ok := wire.HelloVersion([]byte(c.in))
		if got != c.want || ok != c.ok {
			t.Errorf("%s: HelloVersion = %d, %v; want %d, %v", name, got, ok, c.want, c.ok)
		}
		// A Welcome is read the same way.
		got, ok = wire.WelcomeVersion([]byte(c.in))
		if got != c.want || ok != c.ok {
			t.Errorf("%s: WelcomeVersion = %d, %v; want %d, %v", name, got, ok, c.want, c.ok)
		}
	}
}

// The helper is the way back from a strict decoder's refusal: this is what a
// holder and a client do with a later version that added a field.
func TestVersionAfterAStrictRefusal(t *testing.T) {
	hello := `{"version":2,"ticket":"` + ticket64 + `","name":"n","columns":80,"rows":24,"colors":{},"newthing":1}`
	if _, err := wire.DecodeHello([]byte(hello)); !errors.Is(err, wire.ErrInvalid) {
		t.Fatalf("DecodeHello: %v", err)
	}
	if v, ok := wire.HelloVersion([]byte(hello)); !ok || v != 2 || v == wire.ProtocolVersion {
		t.Fatalf("HelloVersion = %d, %v", v, ok)
	}
	p, _ := wire.EncodeWelcome(wire.Welcome{Version: 2, Role: wire.RoleViewer, Lead: "l", Epoch: 1, Runtime: "claude", Caps: wire.DefaultCaps()})
	later := append(append([]byte{}, p[:len(p)-1]...), []byte(`,"newthing":{"a":1}}`)...)
	if _, err := wire.DecodeWelcome(later); !errors.Is(err, wire.ErrInvalid) {
		t.Fatalf("DecodeWelcome: %v", err)
	}
	if v, ok := wire.WelcomeVersion(later); !ok || v != 2 {
		t.Fatalf("WelcomeVersion = %d, %v", v, ok)
	}
	// A Hello of another version without new fields decodes, and the version
	// is the one the helper reads.
	p, err := wire.EncodeHello(wire.Hello{Version: 3, Ticket: ticket64, Name: "n", Columns: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	h, err := wire.DecodeHello(p)
	if v, ok := wire.HelloVersion(p); err != nil || !ok || v != h.Version {
		t.Fatalf("%+v %v; HelloVersion = %d, %v", h, err, v, ok)
	}
}

func TestVersionHelperIsBounded(t *testing.T) {
	pad := func(n int) string { return `{"version":2,"pad":"` + strings.Repeat("x", n) + `"}` }
	// Exactly at the cap of the frame type is read; one byte over is not.
	atHello := pad(wire.MaxClientPayload - len(pad(0)))
	if len(atHello) != wire.MaxClientPayload {
		t.Fatalf("test setup: %d bytes", len(atHello))
	}
	if v, ok := wire.HelloVersion([]byte(atHello)); !ok || v != 2 {
		t.Errorf("a Hello at the cap: %d, %v", v, ok)
	}
	if _, ok := wire.HelloVersion([]byte(atHello + " ")); ok {
		t.Error("a Hello over the cap was read")
	}
	// A Welcome may be larger than a Hello.
	atWelcome := pad(wire.MaxHolderPayload - len(pad(0)))
	if v, ok := wire.WelcomeVersion([]byte(atWelcome)); !ok || v != 2 {
		t.Errorf("a Welcome at the cap: %d, %v", v, ok)
	}
	if _, ok := wire.WelcomeVersion([]byte(atWelcome + " ")); ok {
		t.Error("a Welcome over the cap was read")
	}
	if _, ok := wire.HelloVersion([]byte(atWelcome)); ok {
		t.Error("a Welcome-sized payload was read as a Hello")
	}
	// What it reads does not cost more than the payload it was given.
	payload := []byte(`{"a":[` + strings.Repeat(`{"b":"cccccccccccccccc"},`, 2000) + `1],"version":2}`)
	spent := allocated(func() {
		for i := 0; i < 20; i++ {
			if v, ok := wire.HelloVersion(payload); !ok || v != 2 {
				t.Fatal(v, ok)
			}
		}
	})
	if limit := uint64(20 * 8 * len(payload)); spent > limit {
		t.Errorf("20 reads of %d bytes allocated %d, limit %d", len(payload), spent, limit)
	}
}
