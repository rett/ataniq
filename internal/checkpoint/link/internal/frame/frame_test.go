package frame

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	cp "ataniq/internal/checkpoint"
)

func TestFrameLimits(t *testing.T) {
	full := New(OwnerRead, make([]byte, cp.MaxFrame-6))
	encoded, err := full.Marshal()
	if err != nil || len(encoded) != cp.MaxFrame+4 {
		t.Fatal(len(encoded), err)
	}
	m, err := ReadMessage(bytes.NewReader(encoded))
	if err != nil || !bytes.Equal(m.Fields[0], full.Fields[0]) {
		t.Fatal(err)
	}
	if _, err := New(OwnerRead, make([]byte, cp.MaxFrame-5)).Marshal(); err == nil {
		t.Fatal("encoded oversize frame")
	}
	for _, b := range [][]byte{
		binary.BigEndian.AppendUint32(nil, cp.MaxFrame+1),
		{0, 0, 0, 0}, {0, 0, 0, 2, 1, byte(Granted)}, {0, 0, 0, 2, 2, 255},
		{0, 0, 0, 3, 2, byte(Granted), 0}, {0, 0, 0, 6, 2, byte(Release), 255, 255, 255, 255},
	} {
		if _, err := ReadMessage(bytes.NewReader(b)); !errors.Is(err, ErrProtocol) {
			t.Fatalf("accepted %x: %v", b, err)
		}
	}
}

func FuzzDecoder(f *testing.F) {
	for k := Read; int(k) < len(arity); k++ {
		fields := make([][]byte, arity[k])
		for i := range fields {
			fields[i] = []byte("field")
		}
		b, _ := New(k, fields...).Marshal()
		f.Add(b)
		f.Add(b[:len(b)-1])
	}
	f.Add([]byte{0xff, 0xff, 0xff, 0xff})
	f.Fuzz(func(t *testing.T, b []byte) {
		r := bytes.NewReader(b)
		m, err := ReadMessage(r)
		if err != nil {
			return
		}
		exact, err := m.Marshal()
		if err != nil || !bytes.Equal(exact, b[:len(b)-r.Len()]) {
			t.Fatal("ambiguous frame")
		}
	})
}
