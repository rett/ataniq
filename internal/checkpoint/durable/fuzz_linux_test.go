package durable

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
	"testing"
)

func FuzzDecode(f *testing.F) {
	key := testKey("journal fuzz fixture")
	pin := public(key)
	seed, err := canonical(record{Seq: 1, Op: "create", Identity: &Identity{"w", "s", "i", 1}})
	must(f, err)
	f.Add(seed)
	f.Add([]byte("{"))
	f.Add([]byte{0xff, 0xff, 0xff, 0xff})
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > maxRecord+ed25519.SignatureSize+4 {
			return
		}
		decode(bytes.NewReader(b), pin)
		// Exercise the decoder past its signature check as well.
		frame := binary.BigEndian.AppendUint32(nil, uint32(len(b)))
		frame = append(frame, b...)
		frame = append(frame, ed25519.Sign(key, append([]byte(journalDomain), b...))...)
		r, _, err := decode(bytes.NewReader(frame), pin)
		if err == nil {
			got, err := canonical(r)
			must(t, err)
			if !bytes.Equal(got, b) {
				t.Fatal("accepted noncanonical frame")
			}
		}
	})
}
