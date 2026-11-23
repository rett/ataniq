package seatgate

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func framed(payload string) []byte {
	b := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(b, uint32(len(payload)))
	copy(b[4:], payload)
	return b
}

func TestReadFrame(t *testing.T) {
	t.Run("one frame", func(t *testing.T) {
		got, err := readFrame(bytes.NewReader(framed(`{"a":1}`)))
		if err != nil || string(got) != `{"a":1}` {
			t.Fatalf("%q %v", got, err)
		}
	})
	t.Run("two frames in a row", func(t *testing.T) {
		r := bytes.NewReader(append(framed(`{"a":1}`), framed(`{"b":2}`)...))
		a, err1 := readFrame(r)
		b, err2 := readFrame(r)
		_, err3 := readFrame(r)
		if err1 != nil || err2 != nil || string(a) != `{"a":1}` || string(b) != `{"b":2}` || err3 != io.EOF {
			t.Fatalf("%q %q %v %v %v", a, b, err1, err2, err3)
		}
	})
	t.Run("the largest frame", func(t *testing.T) {
		p := strings.Repeat("x", MaxFrame)
		got, err := readFrame(bytes.NewReader(framed(p)))
		if err != nil || len(got) != MaxFrame {
			t.Fatalf("%d %v", len(got), err)
		}
	})
	refused := map[string][]byte{
		"zero length":       {0, 0, 0, 0},
		"one past the max":  framed(strings.Repeat("x", MaxFrame+1)),
		"huge length":       {0xff, 0xff, 0xff, 0xff, 'x'},
		"high bit length":   {0x80, 0, 0, 0},
		"truncated header":  {0, 0, 1},
		"truncated payload": append([]byte{0, 0, 0, 10}, "abc"...),
		"header only":       {0, 0, 0, 5},
	}
	for name, in := range refused {
		_, err := readFrame(bytes.NewReader(in))
		if !errors.Is(err, ErrProtocol) {
			t.Errorf("%s: %v, want a violation", name, err)
		}
	}
	if _, err := readFrame(bytes.NewReader(nil)); err != io.EOF {
		t.Errorf("empty input: %v, want io.EOF", err)
	}
}

// A length far past the cap must be refused without a buffer for it.
func TestReadFrameAllocatesNothingForAHugeLength(t *testing.T) {
	in := []byte{0x7f, 0xff, 0xff, 0xff}
	n := testing.AllocsPerRun(20, func() { readFrame(bytes.NewReader(in)) })
	if n > 10 {
		t.Fatalf("%v allocations", n)
	}
}

func TestWriteFrame(t *testing.T) {
	var b bytes.Buffer
	if err := writeFrame(&b, []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b.Bytes(), framed(`{"a":1}`)) {
		t.Fatalf("%v", b.Bytes())
	}
	if err := writeFrame(&b, nil); !errors.Is(err, ErrProtocol) {
		t.Errorf("empty payload: %v", err)
	}
	if err := writeFrame(&b, make([]byte, MaxFrame+1)); !errors.Is(err, ErrProtocol) {
		t.Errorf("oversize payload: %v", err)
	}
	if err := writeFrame(&b, make([]byte, MaxFrame)); err != nil {
		t.Errorf("the largest payload: %v", err)
	}
}

func TestReadFrameTimed(t *testing.T) {
	short := 150 * time.Millisecond
	t.Run("a whole frame", func(t *testing.T) {
		a, b := net.Pipe()
		defer a.Close()
		defer b.Close()
		go b.Write(framed(`{"a":1}`))
		got, err := readFrameTimed(a, time.Now, time.Second, time.Second)
		if err != nil || string(got) != `{"a":1}` {
			t.Fatalf("%q %v", got, err)
		}
	})
	t.Run("silence between frames ends at the idle time", func(t *testing.T) {
		a, b := net.Pipe()
		defer a.Close()
		defer b.Close()
		start := time.Now()
		_, err := readFrameTimed(a, time.Now, short, 10*time.Second)
		if !errors.Is(err, os.ErrDeadlineExceeded) || time.Since(start) > 3*time.Second {
			t.Fatalf("%v after %v", err, time.Since(start))
		}
	})
	t.Run("half a frame ends at the body time, not the idle time", func(t *testing.T) {
		a, b := net.Pipe()
		defer a.Close()
		defer b.Close()
		go b.Write([]byte{0, 0, 0, 20, '{'}) // the header and one byte, then nothing
		start := time.Now()
		_, err := readFrameTimed(a, time.Now, 10*time.Second, short)
		if !errors.Is(err, os.ErrDeadlineExceeded) || time.Since(start) > 3*time.Second {
			t.Fatalf("%v after %v", err, time.Since(start))
		}
	})
	t.Run("half a header ends at the body time", func(t *testing.T) {
		a, b := net.Pipe()
		defer a.Close()
		defer b.Close()
		go b.Write([]byte{0, 0})
		start := time.Now()
		_, err := readFrameTimed(a, time.Now, 10*time.Second, short)
		if !errors.Is(err, os.ErrDeadlineExceeded) || time.Since(start) > 3*time.Second {
			t.Fatalf("%v after %v", err, time.Since(start))
		}
	})
	t.Run("a close inside a frame is a violation", func(t *testing.T) {
		a, b := tcpPair(t)
		defer a.Close()
		go func() { b.Write([]byte{0, 0, 0, 20, '{'}); b.Close() }()
		_, err := readFrameTimed(a, time.Now, time.Second, time.Second)
		if !errors.Is(err, ErrProtocol) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("a close between frames is a clean end", func(t *testing.T) {
		a, b := tcpPair(t)
		defer a.Close()
		b.Close()
		if _, err := readFrameTimed(a, time.Now, time.Second, time.Second); err != io.EOF {
			t.Fatalf("%v", err)
		}
	})
	t.Run("a bad length is refused", func(t *testing.T) {
		a, b := net.Pipe()
		defer a.Close()
		defer b.Close()
		go b.Write([]byte{0, 0, 0, 0})
		if _, err := readFrameTimed(a, time.Now, time.Second, time.Second); !errors.Is(err, ErrProtocol) {
			t.Fatalf("%v", err)
		}
	})
}

// tcpPair returns the two ends of one loopback connection.
func tcpPair(t *testing.T) (a, b net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	acc := make(chan net.Conn, 1)
	go func() { c, _ := ln.Accept(); acc <- c }()
	a, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	b = <-acc
	return a, b
}
