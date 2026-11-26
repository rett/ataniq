package link

import (
	"bytes"
	"strings"
	"testing"

	"ataniq/internal/checkpoint/link/internal/frame"
)

func TestJSONDepth(t *testing.T) {
	for _, depth := range []int{8, 9} {
		raw := strings.Repeat("[", depth) + `"\"[[[[[[[[["` + strings.Repeat("]", depth)
		var value any
		if ok := exactJSON([]byte(raw), &value); ok != (depth == 8) || (depth > 8 && value != nil) {
			t.Fatalf("depth %d decoded to %v", depth, value)
		}
	}
}

func TestReceiptFieldsOwned(t *testing.T) {
	b, err := frame.New(frame.Receipt, []byte("body"), []byte("signature")).Marshal()
	must(t, err)
	m, err := frame.ReadMessage(bytes.NewReader(b))
	must(t, err)
	r := receiptOf(m)
	r.Body = append(r.Body, []byte("extra data")...)
	if string(r.Signature) != "signature" {
		t.Fatalf("append changed signature to %q", r.Signature)
	}
}
