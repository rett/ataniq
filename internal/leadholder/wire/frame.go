package wire

import (
	"errors"
	"fmt"
	"io"
	"sync"
)

// ProtocolVersion is the version of the attach protocol that this package
// speaks. Both sides send it, in Hello and Welcome.
const ProtocolVersion = 1

// HeaderLen is the length of a frame header: one type byte and a four-byte
// big-endian payload length.
const HeaderLen = 5

// Payload caps. The caps of the two attach directions are the contract of
// the lead-hosting design (section 1.3), except that the Snapshot cap is
// 19 MiB where the design says 16 MiB (see MaxSnapshotPayload); the control
// cap is this package's.
const (
	// MaxClientPayload is the largest payload from a client to the holder.
	MaxClientPayload = 64 << 10
	// MaxHolderPayload is the largest payload from the holder to a client,
	// except a Snapshot.
	MaxHolderPayload = 1 << 20
	// MaxSnapshotPayload is the largest Snapshot payload: 19 MiB, the 8
	// bytes of position and the largest snapshot of the session pane,
	// sessionpane.MaxSnapshotBytes, which is the bound of the snapshot
	// format for the largest pane (18.11 MiB: both screens, a cluster of 64
	// bytes in every cell, a style of its own for every cell, 256 links of
	// the longest size on each screen). The lead-hosting design's figure of
	// 16 MiB counted one screen. A test of the session pane checks that the
	// two constants agree.
	MaxSnapshotPayload = 19 << 20
	// MaxControlPayload is the largest payload on the control socket, in both
	// directions.
	MaxControlPayload = 64 << 10
)

// Frame types, client to holder.
const (
	TypeHello  = byte('H')
	TypeInput  = byte('I')
	TypePaste  = byte('P')
	TypeResize = byte('R')
	TypeDetach = byte('D')
)

// Frame types, holder to client.
const (
	TypeWelcome  = byte('W')
	TypeSnapshot = byte('S')
	TypeOutput   = byte('O')
	TypeModes    = byte('M')
	TypeSize     = byte('Z')
	TypeState    = byte('T')
	TypeRole     = byte('L')
	TypeCheck    = byte('C')
	TypeNotice   = byte('N')
	TypeExit     = byte('E')
)

// Frame types of the control socket.
const (
	TypeRequest  = byte('q')
	TypeResponse = byte('r')
)

// Direction says which frames a [Reader] accepts: the frames that travel one
// way on one socket.
type Direction int

const (
	// ToHolder is the attach socket as the holder reads it: frames from a
	// client.
	ToHolder Direction = iota + 1
	// ToClient is the attach socket as a client reads it: frames from the
	// holder.
	ToClient
	// ControlRequests is the control socket as the holder reads it.
	ControlRequests
	// ControlResponses is the control socket as the daemon reads it.
	ControlResponses
)

func (d Direction) String() string {
	switch d {
	case ToHolder:
		return "client to holder"
	case ToClient:
		return "holder to client"
	case ControlRequests:
		return "control request"
	case ControlResponses:
		return "control response"
	}
	return fmt.Sprintf("direction(%d)", int(d))
}

// Errors of the framing layer. They are wrapped with detail; test with
// [errors.Is].
var (
	// ErrUnknownType is a frame type that the direction does not have.
	ErrUnknownType = errors.New("wire: unknown frame type")
	// ErrTooLarge is a payload over the cap of its type.
	ErrTooLarge = errors.New("wire: payload over the cap")
	// ErrTooSmall is a payload shorter than its type can be.
	ErrTooSmall = errors.New("wire: payload too short for its type")
	// ErrDirection is a Reader made for a direction this package lacks.
	ErrDirection = errors.New("wire: unknown direction")
)

// typeInfo is what the framing layer knows of one frame type.
type typeInfo struct {
	dir      Direction
	min, max int
	json     bool
}

// jsonMin is the shortest JSON object, "{}".
const jsonMin = 2

var types = map[byte]typeInfo{
	TypeHello:  {dir: ToHolder, min: jsonMin, max: MaxClientPayload, json: true},
	TypeInput:  {dir: ToHolder, min: 0, max: MaxClientPayload},
	TypePaste:  {dir: ToHolder, min: 0, max: MaxClientPayload},
	TypeResize: {dir: ToHolder, min: jsonMin, max: MaxClientPayload, json: true},
	TypeDetach: {dir: ToHolder, min: 0, max: 0},

	TypeWelcome:  {dir: ToClient, min: jsonMin, max: MaxHolderPayload, json: true},
	TypeSnapshot: {dir: ToClient, min: positionLen, max: MaxSnapshotPayload},
	TypeOutput:   {dir: ToClient, min: positionLen, max: MaxHolderPayload},
	TypeModes:    {dir: ToClient, min: jsonMin, max: MaxHolderPayload, json: true},
	TypeSize:     {dir: ToClient, min: jsonMin, max: MaxHolderPayload, json: true},
	TypeState:    {dir: ToClient, min: jsonMin, max: MaxHolderPayload, json: true},
	TypeRole:     {dir: ToClient, min: jsonMin, max: MaxHolderPayload, json: true},
	TypeCheck:    {dir: ToClient, min: jsonMin, max: MaxHolderPayload, json: true},
	TypeNotice:   {dir: ToClient, min: jsonMin, max: MaxHolderPayload, json: true},
	TypeExit:     {dir: ToClient, min: jsonMin, max: MaxHolderPayload, json: true},

	TypeRequest:  {dir: ControlRequests, min: jsonMin, max: MaxControlPayload, json: true},
	TypeResponse: {dir: ControlResponses, min: jsonMin, max: MaxControlPayload, json: true},
}

// Known reports whether typ is a frame type of any direction.
func Known(typ byte) bool {
	_, ok := types[typ]
	return ok
}

// Cap returns the largest payload a frame of type typ may carry, and false
// for an unknown type. A Detach frame has a cap of zero.
func Cap(typ byte) (int, bool) {
	ti, ok := types[typ]
	return ti.max, ok
}

// TypeName returns the name of a frame type for logs, or "unknown".
func TypeName(typ byte) string {
	switch typ {
	case TypeHello:
		return "hello"
	case TypeInput:
		return "input"
	case TypePaste:
		return "paste"
	case TypeResize:
		return "resize"
	case TypeDetach:
		return "detach"
	case TypeWelcome:
		return "welcome"
	case TypeSnapshot:
		return "snapshot"
	case TypeOutput:
		return "output"
	case TypeModes:
		return "modes"
	case TypeSize:
		return "size"
	case TypeState:
		return "state"
	case TypeRole:
		return "role"
	case TypeCheck:
		return "check"
	case TypeNotice:
		return "notice"
	case TypeExit:
		return "exit"
	case TypeRequest:
		return "request"
	case TypeResponse:
		return "response"
	}
	return "unknown"
}

// Frame is one frame that a [Reader] returned. Payload is the reader's own
// copy and is never reused by a later Next.
type Frame struct {
	Type    byte
	Payload []byte
}

// readChunk is the first buffer of a Reader. A payload up to this size is
// allocated at once; a larger one grows by doubling as its bytes arrive, so a
// peer that declares a large payload and sends nothing costs one chunk.
const readChunk = 64 << 10

// Reader reads the frames of one direction. It is not safe for concurrent
// use. After any error from Next, every later call returns the same error and
// reads nothing: a connection that broke the framing is not resynchronised.
type Reader struct {
	r   io.Reader
	dir Direction
	hdr [HeaderLen]byte
	err error
}

// valid reports whether d is one of the four directions.
func (d Direction) valid() bool {
	switch d {
	case ToHolder, ToClient, ControlRequests, ControlResponses:
		return true
	}
	return false
}

func errDirection(d Direction) error { return fmt.Errorf("%w: %d", ErrDirection, int(d)) }

// NewReader returns a Reader of frames that travel in direction dir. An
// unknown direction makes every Next return [ErrDirection].
func NewReader(r io.Reader, dir Direction) *Reader {
	rd := &Reader{r: r, dir: dir}
	if !dir.valid() {
		rd.err = errDirection(dir)
	}
	return rd
}

// Next reads one frame. It returns [io.EOF] when the stream ends between
// frames and [io.ErrUnexpectedEOF] when it ends inside one. An unknown type,
// a payload over the cap of its type and a payload too short for it are
// refused from the header alone, before the payload is read or allocated.
func (rd *Reader) Next() (Frame, error) {
	if rd.err != nil {
		return Frame{}, rd.err
	}
	f, err := rd.next()
	if err != nil {
		rd.err = err
		return Frame{}, err
	}
	return f, nil
}

func (rd *Reader) next() (Frame, error) {
	if _, err := io.ReadFull(rd.r, rd.hdr[:]); err != nil {
		return Frame{}, err
	}
	typ := rd.hdr[0]
	n := uint64(rd.hdr[1])<<24 | uint64(rd.hdr[2])<<16 | uint64(rd.hdr[3])<<8 | uint64(rd.hdr[4])
	ti, ok := types[typ]
	if !ok || ti.dir != rd.dir {
		return Frame{}, fmt.Errorf("%w: 0x%02x on %s", ErrUnknownType, typ, rd.dir)
	}
	if n > uint64(ti.max) {
		return Frame{}, fmt.Errorf("%w: %s frame declares %d bytes, cap %d", ErrTooLarge, TypeName(typ), n, ti.max)
	}
	if n < uint64(ti.min) {
		return Frame{}, fmt.Errorf("%w: %s frame declares %d bytes, least %d", ErrTooSmall, TypeName(typ), n, ti.min)
	}
	payload, err := readPayload(rd.r, int(n))
	if err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return Frame{}, err
	}
	return Frame{Type: typ, Payload: payload}, nil
}

// readPayload reads exactly n bytes. The buffer starts at one chunk (or n, if
// that is smaller) and doubles each time it fills, up to exactly n: the
// capacity of what it returns is n, the buffers it allocates along the way add
// up to less than 3n, and a peer that declares a large payload and stops costs
// a buffer of at most twice what it sent.
func readPayload(r io.Reader, n int) ([]byte, error) {
	buf := make([]byte, 0, min(n, readChunk))
	for len(buf) < n {
		if len(buf) == cap(buf) {
			grown := make([]byte, len(buf), min(n, 2*cap(buf)))
			copy(grown, buf)
			buf = grown
		}
		m, err := io.ReadFull(r, buf[len(buf):cap(buf)])
		buf = buf[:len(buf)+m]
		if err != nil {
			return nil, err
		}
	}
	return buf, nil
}

// smallFrame is the largest payload that [Write] copies behind its header to
// send both in one call to the underlying writer.
const smallFrame = 64 << 10

// Write writes one frame that travels in direction dir to w: the direction of
// the [Reader] on the other end. It refuses an unknown direction
// ([ErrDirection]), a type that the direction does not have
// ([ErrUnknownType], as the Reader would refuse it), a payload over the cap of
// its type and one too short for it, before it writes anything. A holder that
// writes a Hello to a client, or a client that writes a Welcome, gets an error
// here and not a connection that the peer's Reader has to close.
//
// A frame with a payload of up to 64 KiB is written with one call to w.Write,
// so a writer that makes each call atomic (a datagram or a locked stream)
// never interleaves two frames. A larger payload is written in two calls;
// callers that share one connection between goroutines must use a [Writer],
// which serialises whole frames.
func Write(w io.Writer, dir Direction, typ byte, payload []byte) error {
	if !dir.valid() {
		return errDirection(dir)
	}
	ti, ok := types[typ]
	if !ok || ti.dir != dir {
		return fmt.Errorf("%w: 0x%02x on %s", ErrUnknownType, typ, dir)
	}
	if len(payload) > ti.max {
		return fmt.Errorf("%w: %s payload is %d bytes, cap %d", ErrTooLarge, TypeName(typ), len(payload), ti.max)
	}
	if len(payload) < ti.min {
		return fmt.Errorf("%w: %s payload is %d bytes, least %d", ErrTooSmall, TypeName(typ), len(payload), ti.min)
	}
	hdr := [HeaderLen]byte{typ, byte(len(payload) >> 24), byte(len(payload) >> 16), byte(len(payload) >> 8), byte(len(payload))}
	if len(payload) <= smallFrame {
		buf := make([]byte, 0, HeaderLen+len(payload))
		buf = append(buf, hdr[:]...)
		buf = append(buf, payload...)
		return writeAll(w, buf)
	}
	if err := writeAll(w, hdr[:]); err != nil {
		return err
	}
	return writeAll(w, payload)
}

func writeAll(w io.Writer, b []byte) error {
	n, err := w.Write(b)
	if err == nil && n < len(b) {
		err = io.ErrShortWrite
	}
	return err
}

// Writer writes the frames of one direction to one connection from any number
// of goroutines. A frame is written whole before the next one starts. After a
// write error the connection is out of step, so every later call returns that
// error.
type Writer struct {
	mu  sync.Mutex
	w   io.Writer
	dir Direction
	err error
}

// NewWriter returns a Writer on w of frames that travel in direction dir, the
// direction of the [Reader] on the other end. An unknown direction makes
// every WriteFrame return [ErrDirection].
func NewWriter(w io.Writer, dir Direction) *Writer {
	wr := &Writer{w: w, dir: dir}
	if !dir.valid() {
		wr.err = errDirection(dir)
	}
	return wr
}

// WriteFrame writes one frame; see [Write] for what it refuses. A frame
// refused before any byte is written does not break the Writer.
func (w *Writer) WriteFrame(typ byte, payload []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return w.err
	}
	if err := Write(w.w, w.dir, typ, payload); err != nil {
		if !errors.Is(err, ErrUnknownType) && !errors.Is(err, ErrTooLarge) && !errors.Is(err, ErrTooSmall) {
			w.err = err
		}
		return err
	}
	return nil
}
