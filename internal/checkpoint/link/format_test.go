package link

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"reflect"
	"testing"
)

func panicText(v any) (result string) {
	defer func() { result = fmt.Sprint(recover()) }()
	panic(v)
}

// Traverse fields directly, including private fields, without calling any
// formatter. The link client's printable structure must contain no key bytes.
func containsPrivate(v reflect.Value, seed []byte, seen map[uintptr]bool) bool {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() || seen[v.Pointer()] {
			return false
		}
		seen[v.Pointer()] = true
		return containsPrivate(v.Elem(), seed, seen)
	case reflect.Interface:
		if !v.IsNil() {
			return containsPrivate(v.Elem(), seed, seen)
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if containsPrivate(v.Field(i), seed, seen) {
				return true
			}
		}
	case reflect.Array, reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			b := make([]byte, v.Len())
			for i := range b {
				b[i] = byte(v.Index(i).Uint())
			}
			return bytes.Contains(b, seed)
		}
	}
	return false
}

func TestClientFormatting(t *testing.T) {
	for _, network := range []string{"pipe", "tcp"} {
		t.Run(network, func(t *testing.T) {
			f := newFixture(t, "witness")
			id := identity(t, "home")
			e := endpointFor(t, id, f.id, f)
			r := serveNetwork(t, network, e.Serve)
			r.ServerPin = id.Fingerprint()
			startPolling(t, f.s, r, memoryWitness{f.w})
			c, err := e.Connect(t.Context(), f.slot, f.home, f.session)
			must(t, err)
			client := c.(*client)
			for _, seed := range [][]byte{f.home.Seed(), f.session.Seed()} {
				if !containsPrivate(reflect.ValueOf(struct{ secret []byte }{seed}), seed, map[uintptr]bool{}) {
					t.Fatal("reflection control missed inline seed")
				}
				if containsPrivate(reflect.ValueOf(client), seed, map[uintptr]bool{}) {
					t.Fatal("private bytes reachable in structural dump")
				}
			}
			for _, v := range []any{c, *client, client.keys, *client.keys} {
				want := v.(fmt.Stringer).String()
				if panicText(v) != want {
					t.Fatal("panic was not redacted")
				}
				for _, verb := range []string{"%v", "%+v", "%#v", "%x"} {
					var buf bytes.Buffer
					log.New(&buf, "", 0).Printf(verb, v)
					if fmt.Sprintf(verb, v) != want || fmt.Errorf(verb, v).Error() != want || buf.String() != want+"\n" {
						t.Fatalf("missing redaction for %s", verb)
					}
				}
			}
			data, err := json.Marshal(c)
			must(t, err)
			if string(data) != `"[redacted checkpoint client]"` {
				t.Fatal("client JSON was not redacted")
			}
		})
	}
}
