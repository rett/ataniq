package link

import (
	"bytes"
	"fmt"
	"log"
	"testing"
)

func TestClientFormatting(t *testing.T) {
	f := newFixture(t, "witness")
	e := f.serve(t)
	c, err := e.Connect(t.Context(), f.slot, f.home, f.session)
	must(t, err)
	for _, v := range []any{c, *c.(*client)} {
		for _, verb := range []string{"%v", "%+v", "%#v"} {
			var buf bytes.Buffer
			log.New(&buf, "", 0).Printf(verb, v)
			if fmt.Sprintf(verb, v) != "[redacted checkpoint client]" || fmt.Errorf(verb, v).Error() != "[redacted checkpoint client]" || buf.String() != "[redacted checkpoint client]\n" {
				t.Fatalf("client was not redacted with %s", verb)
			}
		}
	}
}
