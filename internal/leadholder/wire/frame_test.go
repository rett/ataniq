package wire_test

import (
	"bytes"
	"errors"
	"io"
	"runtime"
	"sync"
	"testing"
	"testing/iotest"

	"ataniq/internal/leadholder/wire"
)

// frameTypes lists every frame type with its direction and cap.
var frameTypes = []struct {
	typ  byte
	dir  wire.Direction
	max  int
	min  int
	name string
}{
	{wire.TypeHello, wire.ToHolder, wire.MaxClientPayload, 2, "hello"},
	{wire.TypeInput, wire.ToHolder, wire.MaxClientPayload, 0, "input"},
	{wire.TypePaste, wire.ToHolder, wire.MaxClientPayload, 0, "paste"},
	{wire.TypeResize, wire.ToHolder, wire.MaxClientPayload, 2, "resize"},
	{wire.TypeDetach, wire.ToHolder, 0, 0, "detach"},
	{wire.TypeWelcome, wire.ToClient, wire.MaxHolderPayload, 2, "welcome"},
	{wire.TypeSnapshot, wire.ToClient, wire.MaxSnapshotPayload, 8, "snapshot"},
	{wire.TypeOutput, wire.ToClient, wire.MaxHolderPayload, 8, "output"},
	{wire.TypeModes, wire.ToClient, wire.MaxHolderPayload, 2, "modes"},
	{wire.TypeSize, wire.ToClient, wire.MaxHolderPayload, 2, "size"},
	{wire.TypeState, wire.ToClient, wire.MaxHolderPayload, 2, "state"},
	{wire.TypeRole, wire.ToClient, wire.MaxHolderPayload, 2, "role"},
	{wire.TypeCheck, wire.ToClient, wire.MaxHolderPayload, 2, "check"},
	{wire.TypeNotice, wire.ToClient, wire.MaxHolderPayload, 2, "notice"},
	{wire.TypeExit, wire.ToClient, wire.MaxHolderPayload, 2, "exit"},
	{wire.TypeRequest, wire.ControlRequests, wire.MaxControlPayload, 2, "request"},
	{wire.TypeResponse, wire.ControlResponses, wire.MaxControlPayload, 2, "response"},
}

var directions = []wire.Direction{wire.ToHolder, wire.ToClient, wire.ControlRequests, wire.ControlResponses}

func TestCapsAreTheContract(t *testing.T) {
	if wire.MaxClientPayload != 64<<10 || wire.MaxHolderPayload != 1<<20 || wire.MaxSnapshotPayload != 19<<20 {
		t.Fatalf("caps: %d %d %d", wire.MaxClientPayload, wire.MaxHolderPayload, wire.MaxSnapshotPayload)
	}
	if wire.ProtocolVersion != 1 {
		t.Fatalf("protocol version %d", wire.ProtocolVersion)
	}
	for _, ft := range frameTypes {
		if got, ok := wire.Cap(ft.typ); !ok || got != ft.max {
			t.Errorf("Cap(%q) = %d, %v; want %d", ft.typ, got, ok, ft.max)
		}
		if !wire.Known(ft.typ) || wire.TypeName(ft.typ) != ft.name {
			t.Errorf("type %q: known %v name %q", ft.typ, wire.Known(ft.typ), wire.TypeName(ft.typ))
		}
	}
	if wire.Known('X') || wire.TypeName('X') != "unknown" {
		t.Error("X is a type")
	}
	if _, ok := wire.Cap(0); ok {
		t.Error("type 0 has a cap")
	}
}

func hdr(typ byte, n uint32) []byte {
	return []byte{typ, byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}
}

// countReader counts the bytes that are read from it.
type countReader struct {
	r io.Reader
	n int
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

// zeros yields n zero bytes without holding them.
func zeros(n int) io.Reader { return io.LimitReader(zeroReader{}, int64(n)) }

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestRoundTripEveryTypeAtItsCap(t *testing.T) {
	for _, ft := range frameTypes {
		for _, n := range []int{ft.min, ft.max / 2, ft.max} {
			if n < ft.min {
				continue
			}
			payload := bytes.Repeat([]byte{'x'}, n)
			var buf bytes.Buffer
			if err := wire.Write(&buf, dirOf(ft.typ), ft.typ, payload); err != nil {
				t.Fatalf("%s %d: Write: %v", ft.name, n, err)
			}
			if buf.Len() != wire.HeaderLen+n {
				t.Fatalf("%s %d: wrote %d bytes", ft.name, n, buf.Len())
			}
			if !bytes.Equal(buf.Bytes()[:5], hdr(ft.typ, uint32(n))) {
				t.Fatalf("%s: header % x", ft.name, buf.Bytes()[:5])
			}
			rd := wire.NewReader(&buf, ft.dir)
			f, err := rd.Next()
			if err != nil {
				t.Fatalf("%s %d: Next: %v", ft.name, n, err)
			}
			if f.Type != ft.typ || !bytes.Equal(f.Payload, payload) {
				t.Fatalf("%s %d: frame %q, %d bytes", ft.name, n, f.Type, len(f.Payload))
			}
			if _, err := rd.Next(); err != io.EOF {
				t.Fatalf("%s %d: after the frame: %v, want EOF", ft.name, n, err)
			}
		}
	}
}

func TestReaderRefusesFromTheHeader(t *testing.T) {
	for _, ft := range frameTypes {
		for _, dir := range directions {
			if dir == ft.dir {
				continue
			}
			t.Run("wrong direction "+ft.name+" on "+dir.String(), func(t *testing.T) {
				src := &countReader{r: bytes.NewReader(append(hdr(ft.typ, 2), '{', '}', 'x'))}
				rd := wire.NewReader(src, dir)
				if _, err := rd.Next(); !errors.Is(err, wire.ErrUnknownType) {
					t.Fatalf("err = %v, want ErrUnknownType", err)
				}
				if src.n > wire.HeaderLen {
					t.Fatalf("read %d bytes, want only the header", src.n)
				}
			})
		}
		t.Run("over the cap "+ft.name, func(t *testing.T) {
			for _, n := range []uint32{uint32(ft.max) + 1, uint32(ft.max) + 1<<20, 1 << 31, 0xffffffff} {
				src := &countReader{r: io.MultiReader(bytes.NewReader(hdr(ft.typ, n)), zeros(ft.max+4096))}
				rd := wire.NewReader(src, ft.dir)
				if _, err := rd.Next(); !errors.Is(err, wire.ErrTooLarge) {
					t.Fatalf("declared %d: err = %v, want ErrTooLarge", n, err)
				}
				if src.n > wire.HeaderLen {
					t.Fatalf("declared %d: read %d bytes, want only the header", n, src.n)
				}
			}
		})
		if ft.min > 0 {
			t.Run("under the least "+ft.name, func(t *testing.T) {
				src := &countReader{r: io.MultiReader(bytes.NewReader(hdr(ft.typ, uint32(ft.min-1))), zeros(64))}
				rd := wire.NewReader(src, ft.dir)
				if _, err := rd.Next(); !errors.Is(err, wire.ErrTooSmall) {
					t.Fatalf("err = %v, want ErrTooSmall", err)
				}
				if src.n > wire.HeaderLen {
					t.Fatalf("read %d bytes", src.n)
				}
			})
		}
	}
	// Types that no direction has.
	for _, typ := range []byte{0, 'X', 'h', 'Q', 0xff, '{'} {
		for _, dir := range directions {
			rd := wire.NewReader(bytes.NewReader(append(hdr(typ, 0), 1, 2, 3)), dir)
			if _, err := rd.Next(); !errors.Is(err, wire.ErrUnknownType) {
				t.Errorf("type %#x on %s: err = %v", typ, dir, err)
			}
		}
	}
}

func TestReaderStopsAfterAnError(t *testing.T) {
	var good bytes.Buffer
	if err := wire.Write(&good, wire.ToHolder, wire.TypeInput, []byte("a")); err != nil {
		t.Fatal(err)
	}
	stream := append(hdr('X', 0), good.Bytes()...)
	src := &countReader{r: bytes.NewReader(stream)}
	rd := wire.NewReader(src, wire.ToHolder)
	_, first := rd.Next()
	if !errors.Is(first, wire.ErrUnknownType) {
		t.Fatalf("first: %v", first)
	}
	at := src.n
	for i := 0; i < 3; i++ {
		if _, err := rd.Next(); err != first {
			t.Fatalf("later call %d: %v, want %v", i, err, first)
		}
	}
	if src.n != at {
		t.Fatalf("read %d more bytes after the error", src.n-at)
	}
}

func TestReaderShortReads(t *testing.T) {
	var buf bytes.Buffer
	for _, p := range []string{"one", "", "three"} {
		if err := wire.Write(&buf, wire.ToHolder, wire.TypeInput, []byte(p)); err != nil {
			t.Fatal(err)
		}
	}
	whole := buf.Bytes()
	// Every way of cutting the stream: before a frame is EOF, inside one is
	// an unexpected EOF.
	boundaries := map[int]bool{0: true, 8: true, 13: true, len(whole): true}
	for cut := 0; cut <= len(whole); cut++ {
		rd := wire.NewReader(bytes.NewReader(whole[:cut]), wire.ToHolder)
		var err error
		for err == nil {
			_, err = rd.Next()
		}
		want := io.ErrUnexpectedEOF
		if boundaries[cut] {
			want = io.EOF
		}
		if err != want {
			t.Errorf("cut at %d: %v, want %v", cut, err, want)
		}
	}
	// A reader that returns one byte at a time and one that returns its
	// final data with the error give the same frames.
	for name, wrap := range map[string]func(io.Reader) io.Reader{
		"one byte": iotest.OneByteReader, "data err": iotest.DataErrReader, "half": iotest.HalfReader,
	} {
		rd := wire.NewReader(wrap(bytes.NewReader(whole)), wire.ToHolder)
		var got []string
		for {
			f, err := rd.Next()
			if err != nil {
				if err != io.EOF {
					t.Fatalf("%s: %v", name, err)
				}
				break
			}
			got = append(got, string(f.Payload))
		}
		if len(got) != 3 || got[0] != "one" || got[1] != "" || got[2] != "three" {
			t.Errorf("%s: frames %q", name, got)
		}
	}
	// An error of the source is returned as it is.
	boom := errors.New("boom")
	rd := wire.NewReader(io.MultiReader(bytes.NewReader(whole[:3]), iotest.ErrReader(boom)), wire.ToHolder)
	if _, err := rd.Next(); !errors.Is(err, boom) {
		t.Errorf("source error: %v", err)
	}
}

func TestZeroLengthPayloads(t *testing.T) {
	for _, c := range []struct {
		typ  byte
		dir  wire.Direction
		n    int
		want error
	}{
		{wire.TypeDetach, wire.ToHolder, 0, nil},
		{wire.TypeInput, wire.ToHolder, 0, nil},
		{wire.TypePaste, wire.ToHolder, 0, nil},
		{wire.TypeDetach, wire.ToHolder, 1, wire.ErrTooLarge},
		{wire.TypeHello, wire.ToHolder, 0, wire.ErrTooSmall},
		{wire.TypeOutput, wire.ToClient, 0, wire.ErrTooSmall},
		{wire.TypeOutput, wire.ToClient, 7, wire.ErrTooSmall},
		{wire.TypeOutput, wire.ToClient, 8, nil},
		{wire.TypeSnapshot, wire.ToClient, 7, wire.ErrTooSmall},
		{wire.TypeSnapshot, wire.ToClient, 8, nil},
		{wire.TypeNotice, wire.ToClient, 0, wire.ErrTooSmall},
	} {
		src := io.MultiReader(bytes.NewReader(hdr(c.typ, uint32(c.n))), zeros(c.n))
		f, err := wire.NewReader(src, c.dir).Next()
		if c.want == nil {
			if err != nil || len(f.Payload) != c.n {
				t.Errorf("%s %d: %v, %d bytes", wire.TypeName(c.typ), c.n, err, len(f.Payload))
			}
		} else if !errors.Is(err, c.want) {
			t.Errorf("%s %d: %v, want %v", wire.TypeName(c.typ), c.n, err, c.want)
		}
		// Write agrees with the reader.
		werr := wire.Write(io.Discard, dirOf(c.typ), c.typ, make([]byte, c.n))
		if (werr == nil) != (c.want == nil) {
			t.Errorf("Write %s %d: %v", wire.TypeName(c.typ), c.n, werr)
		}
	}
}

func TestUnknownDirection(t *testing.T) {
	src := &countReader{r: bytes.NewReader(hdr(wire.TypeDetach, 0))}
	rd := wire.NewReader(src, wire.Direction(0))
	if _, err := rd.Next(); !errors.Is(err, wire.ErrDirection) {
		t.Fatalf("err = %v", err)
	}
	if src.n != 0 {
		t.Fatalf("read %d bytes", src.n)
	}
}

// allocated runs f and returns the heap bytes that it allocated.
func allocated(f func()) uint64 {
	var a, b runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&a)
	f()
	runtime.ReadMemStats(&b)
	return b.TotalAlloc - a.TotalAlloc
}

func TestRefusedFrameAllocatesNothingProportional(t *testing.T) {
	const rounds = 200
	// Over the cap: the declared length is huge, the source has the bytes.
	spent := allocated(func() {
		for i := 0; i < rounds; i++ {
			src := io.MultiReader(bytes.NewReader(hdr(wire.TypeOutput, 0xffffffff)), zeros(1<<20))
			if _, err := wire.NewReader(src, wire.ToClient).Next(); !errors.Is(err, wire.ErrTooLarge) {
				t.Fatal(err)
			}
		}
	})
	if limit := uint64(rounds * 1024); spent > limit {
		t.Errorf("over-cap refusals allocated %d bytes in %d rounds, limit %d", spent, rounds, limit)
	}
	// Under the cap but never sent: a Snapshot that declares 19 MiB and
	// delivers 10 bytes allocates one chunk at most.
	spent = allocated(func() {
		for i := 0; i < rounds; i++ {
			src := io.MultiReader(bytes.NewReader(hdr(wire.TypeSnapshot, wire.MaxSnapshotPayload)), zeros(10))
			if _, err := wire.NewReader(src, wire.ToClient).Next(); !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatal(err)
			}
		}
	})
	if limit := uint64(rounds * 80 << 10); spent > limit {
		t.Errorf("a declared 19 MiB that never came allocated %d bytes in %d rounds, limit %d", spent, rounds, limit)
	}
}

func TestLargeSnapshotAtTheCap(t *testing.T) {
	payload := make([]byte, wire.MaxSnapshotPayload)
	payload[7] = 1
	payload[len(payload)-1] = 9
	var buf bytes.Buffer
	if err := wire.Write(&buf, wire.ToClient, wire.TypeSnapshot, payload); err != nil {
		t.Fatal(err)
	}
	f, err := wire.NewReader(&buf, wire.ToClient).Next()
	if err != nil || !bytes.Equal(f.Payload, payload) {
		t.Fatalf("Next: %v", err)
	}
	if err := wire.Write(io.Discard, wire.ToClient, wire.TypeSnapshot, make([]byte, wire.MaxSnapshotPayload+1)); !errors.Is(err, wire.ErrTooLarge) {
		t.Fatalf("one byte over: %v", err)
	}
}

func TestPayloadIsNotReused(t *testing.T) {
	var buf bytes.Buffer
	_ = wire.Write(&buf, wire.ToHolder, wire.TypeInput, []byte("first"))
	_ = wire.Write(&buf, wire.ToHolder, wire.TypeInput, []byte("other"))
	rd := wire.NewReader(&buf, wire.ToHolder)
	a, _ := rd.Next()
	b, _ := rd.Next()
	if string(a.Payload) != "first" || string(b.Payload) != "other" {
		t.Fatalf("%q %q", a.Payload, b.Payload)
	}
}

// recorder records the size of each Write call.
type recorder struct{ calls []int }

func (r *recorder) Write(p []byte) (int, error) {
	r.calls = append(r.calls, len(p))
	return len(p), nil
}

func TestWriteCalls(t *testing.T) {
	var r recorder
	if err := wire.Write(&r, wire.ToHolder, wire.TypeInput, bytes.Repeat([]byte{1}, 1000)); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 1 || r.calls[0] != 1005 {
		t.Fatalf("small frame: calls %v, want one of 1005", r.calls)
	}
	r = recorder{}
	if err := wire.Write(&r, wire.ToHolder, wire.TypeDetach, nil); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 1 || r.calls[0] != 5 {
		t.Fatalf("empty frame: calls %v", r.calls)
	}
	r = recorder{}
	if err := wire.Write(&r, wire.ToClient, wire.TypeOutput, make([]byte, 200<<10)); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 2 || r.calls[0] != 5 || r.calls[1] != 200<<10 {
		t.Fatalf("large frame: calls %v", r.calls)
	}
}

func TestWriteRefusals(t *testing.T) {
	var r recorder
	if err := wire.Write(&r, wire.ToHolder, 'X', nil); !errors.Is(err, wire.ErrUnknownType) {
		t.Errorf("unknown type: %v", err)
	}
	if err := wire.Write(&r, wire.ToHolder, wire.TypeInput, make([]byte, wire.MaxClientPayload+1)); !errors.Is(err, wire.ErrTooLarge) {
		t.Errorf("over the cap: %v", err)
	}
	if err := wire.Write(&r, wire.ToHolder, wire.TypeDetach, []byte{1}); !errors.Is(err, wire.ErrTooLarge) {
		t.Errorf("detach with a payload: %v", err)
	}
	if err := wire.Write(&r, wire.ToClient, wire.TypeNotice, nil); !errors.Is(err, wire.ErrTooSmall) {
		t.Errorf("empty notice: %v", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("a refused frame wrote %v", r.calls)
	}
	if err := wire.Write(shortWriter{}, wire.ToHolder, wire.TypeInput, []byte("abc")); !errors.Is(err, io.ErrShortWrite) {
		t.Errorf("short write: %v", err)
	}
	boom := errors.New("boom")
	if err := wire.Write(errWriter{boom}, wire.ToHolder, wire.TypeInput, []byte("abc")); !errors.Is(err, boom) {
		t.Errorf("write error: %v", err)
	}
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

type errWriter struct{ err error }

func (w errWriter) Write(p []byte) (int, error) { return 0, w.err }

func TestWriterSerialisesFrames(t *testing.T) {
	var buf bytes.Buffer // not safe for concurrent use: the Writer must serialise
	w := wire.NewWriter(&buf, wire.ToClient)
	const writers, each = 8, 50
	var wg sync.WaitGroup
	for g := 0; g < writers; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				// Alternate small and large frames so both paths run.
				n := 100
				if i%2 == 1 {
					n = 70 << 10
				}
				if err := w.WriteFrame(wire.TypeOutput, bytes.Repeat([]byte{byte('a' + g)}, n)); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	rd := wire.NewReader(&buf, wire.ToClient)
	count := 0
	for {
		f, err := rd.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		count++
		for _, c := range f.Payload[1:] {
			if c != f.Payload[0] {
				t.Fatal("a frame mixes bytes of two writers")
			}
		}
	}
	if count != writers*each {
		t.Fatalf("%d frames, want %d", count, writers*each)
	}
}

func TestWriterRefusalDoesNotBreakItButAWriteErrorDoes(t *testing.T) {
	var buf bytes.Buffer
	w := wire.NewWriter(&buf, wire.ToHolder)
	if err := w.WriteFrame('X', nil); !errors.Is(err, wire.ErrUnknownType) {
		t.Fatal(err)
	}
	if err := w.WriteFrame(wire.TypeInput, []byte("ok")); err != nil {
		t.Fatalf("after a refusal: %v", err)
	}
	boom := errors.New("boom")
	bw := wire.NewWriter(errWriter{boom}, wire.ToHolder)
	if err := bw.WriteFrame(wire.TypeInput, []byte("a")); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if err := bw.WriteFrame(wire.TypeInput, []byte("a")); !errors.Is(err, boom) {
		t.Fatalf("after a write error: %v", err)
	}
}

// A payload is kept in a buffer of exactly its size: a Snapshot of 19 MiB is
// not held in 32 MiB, and reading it does not allocate many times its size.
func TestPayloadCapacityIsExact(t *testing.T) {
	for _, n := range []int{8, 100, 64 << 10, 64<<10 + 1, 128 << 10, 200000, 1<<20 + 7, wire.MaxSnapshotPayload} {
		var f wire.Frame
		spent := allocated(func() {
			src := io.MultiReader(bytes.NewReader(hdr(wire.TypeSnapshot, uint32(n))), zeros(n))
			var err error
			if f, err = wire.NewReader(src, wire.ToClient).Next(); err != nil {
				t.Fatal(err)
			}
		})
		if len(f.Payload) != n || cap(f.Payload) != n {
			t.Errorf("%d bytes: len %d cap %d", n, len(f.Payload), cap(f.Payload))
		}
		// The buffers of the doubling add up to under 3n (n itself and, at
		// worst, the 2n before it), with slack for the test's own small
		// allocations. The old growth step allocated about 6n.
		if limit := uint64(3*n + 128<<10); spent > limit {
			t.Errorf("%d bytes: allocated %d, limit %d", n, spent, limit)
		}
	}
	// A payload that stops part-way costs at most four times what arrived.
	for _, got := range []int{10, 64 << 10, 64<<10 + 1, 1 << 20} {
		spent := allocated(func() {
			src := io.MultiReader(bytes.NewReader(hdr(wire.TypeSnapshot, wire.MaxSnapshotPayload)), zeros(got))
			if _, err := wire.NewReader(src, wire.ToClient).Next(); !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatal(err)
			}
		})
		if limit := uint64(4*got + 192<<10); spent > limit {
			t.Errorf("%d bytes of a declared 19 MiB: allocated %d, limit %d", got, spent, limit)
		}
	}
}

// dirOf is the direction of a frame type, for tests that write one.
func dirOf(typ byte) wire.Direction {
	for _, ft := range frameTypes {
		if ft.typ == typ {
			return ft.dir
		}
	}
	return wire.ToHolder
}

// A writer and a reader of one direction agree on what a frame is: Write
// refuses every frame type that the Reader of that direction refuses, before
// it writes a byte.
func TestWriteChecksTheDirection(t *testing.T) {
	for _, ft := range frameTypes {
		for _, dir := range directions {
			payload := make([]byte, ft.min)
			var buf bytes.Buffer
			err := wire.Write(&buf, dir, ft.typ, payload)
			if dir == ft.dir {
				if err != nil {
					t.Errorf("%s on %s: %v", ft.name, dir, err)
				}
				continue
			}
			if !errors.Is(err, wire.ErrUnknownType) || buf.Len() != 0 {
				t.Errorf("%s on %s: err = %v, %d bytes written", ft.name, dir, err, buf.Len())
			}
			// And the Reader of that direction refuses the same frame.
			hdrAndPayload := append(hdr(ft.typ, uint32(len(payload))), payload...)
			if _, rerr := wire.NewReader(bytes.NewReader(hdrAndPayload), dir).Next(); !errors.Is(rerr, wire.ErrUnknownType) {
				t.Errorf("%s on %s: reader err = %v", ft.name, dir, rerr)
			}
		}
	}
	var buf bytes.Buffer
	if err := wire.Write(&buf, wire.Direction(0), wire.TypeInput, nil); !errors.Is(err, wire.ErrDirection) {
		t.Errorf("unknown direction: %v", err)
	}
	if err := wire.Write(&buf, wire.Direction(9), wire.TypeInput, nil); !errors.Is(err, wire.ErrDirection) || buf.Len() != 0 {
		t.Errorf("direction 9: %v", err)
	}
}

func TestWriterChecksTheDirection(t *testing.T) {
	var buf bytes.Buffer
	// A holder's writer to a client cannot write a Hello, a Request or a
	// Response.
	w := wire.NewWriter(&buf, wire.ToClient)
	for _, typ := range []byte{wire.TypeHello, wire.TypeInput, wire.TypeDetach, wire.TypeRequest, wire.TypeResponse, 'X'} {
		if err := w.WriteFrame(typ, nil); !errors.Is(err, wire.ErrUnknownType) {
			t.Errorf("type %q: %v", typ, err)
		}
	}
	if buf.Len() != 0 {
		t.Fatalf("%d bytes written by refused frames", buf.Len())
	}
	// A refusal does not break the writer.
	if err := w.WriteFrame(wire.TypeOutput, []byte("12345678x")); err != nil {
		t.Fatalf("after refusals: %v", err)
	}
	if f, err := wire.NewReader(&buf, wire.ToClient).Next(); err != nil || f.Type != wire.TypeOutput {
		t.Fatalf("%v %v", f, err)
	}
	// A client's writer cannot write what only the holder sends.
	cw := wire.NewWriter(&buf, wire.ToHolder)
	if err := cw.WriteFrame(wire.TypeWelcome, []byte("{}")); !errors.Is(err, wire.ErrUnknownType) {
		t.Errorf("welcome from a client: %v", err)
	}
	if err := cw.WriteFrame(wire.TypeDetach, nil); err != nil {
		t.Errorf("detach from a client: %v", err)
	}
	// A control writer has the control frames of its side.
	if err := wire.NewWriter(io.Discard, wire.ControlRequests).WriteFrame(wire.TypeResponse, []byte("{}")); !errors.Is(err, wire.ErrUnknownType) {
		t.Errorf("response on the request side: %v", err)
	}
	if err := wire.NewWriter(io.Discard, wire.ControlResponses).WriteFrame(wire.TypeResponse, []byte("{}")); err != nil {
		t.Errorf("response on the response side: %v", err)
	}
	// An unknown direction fails every write, as it does for a Reader.
	bad := wire.NewWriter(&buf, wire.Direction(0))
	n := buf.Len()
	for i := 0; i < 2; i++ {
		if err := bad.WriteFrame(wire.TypeInput, []byte("a")); !errors.Is(err, wire.ErrDirection) {
			t.Errorf("unknown direction: %v", err)
		}
	}
	if buf.Len() != n {
		t.Error("a writer of an unknown direction wrote")
	}
}
