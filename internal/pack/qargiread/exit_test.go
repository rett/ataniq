package qargiread

import (
	"strings"
	"testing"
)

func TestRefusalLineBytes(t *testing.T) {
	const start = `qargi-refusal/1 {"exit":1,"padding":"`
	const end = `"}`
	line := start + strings.Repeat("x", 4096-len(start)-len(end)) + end
	if got := readRefusals(line + "\n"); got.unreadable || got.exit != "1" {
		t.Fatalf("rejected 4096-byte line")
	}
	for _, bad := range []string{line[:len(start)] + "x" + line[len(start):], start + "\xff" + end, line + "\r", `qargi-refusal/1  {"exit":1}`} {
		if !readRefusals(bad).unreadable {
			t.Fatalf("accepted invalid refusal of %d bytes", len(bad))
		}
	}
}
