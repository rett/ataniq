package link

import (
	"bytes"
	"strings"
	"testing"

	cp "ataniq/internal/checkpoint"
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

func FuzzReadDecoder(f *testing.F) {
	slot := cp.Slot{Witness: "witness", Authority: "authority", Store: "store"}
	for _, op := range []string{"connect", "read", "settle"} {
		q := cp.ReadRequest{Slot: slot, Challenge: cp.ID{1}, ReceiptID: cp.ID{2}}
		body, err := readBody(q, "witness-key", op)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(body, op)
	}
	f.Fuzz(func(t *testing.T, b []byte, op string) {
		if len(b) > cp.MaxFrame || len(op) > 16 {
			return
		}
		q, err := parseRead(b, "witness-key", op)
		if err != nil {
			return
		}
		exact, err := readBody(q, "witness-key", op)
		if err != nil || !bytes.Equal(exact, b) {
			t.Fatal("ambiguous read request")
		}
	})
}
