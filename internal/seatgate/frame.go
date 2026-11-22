package seatgate

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"
)

const (
	// MaxFrame is the largest payload of a frame, in bytes. A frame is a
	// 4-byte big-endian length and then that many bytes of one JSON object.
	MaxFrame = 64 << 10

	// MaxDepth is how deeply objects and arrays may nest in a frame; the
	// frame's own object is the first level.
	MaxDepth = 8
)

// ErrProtocol is what every violation of the wire rules wraps: a frame that is
// empty, too large, cut short or not well formed, a message with a field it may
// not have, a request out of order. Its text, and the text of what wraps it,
// is fixed here and holds no byte of what the peer sent.
var ErrProtocol = errors.New("seatgate: protocol violation")

// violation is an error that wraps [ErrProtocol] with a fixed description.
func violation(what string) error { return fmt.Errorf("%w: %s", ErrProtocol, what) }

// readFrame reads one frame from r and returns its payload. A clean end of
// input before the first byte of a frame is io.EOF; one inside a frame is a
// violation. A length of zero or above [MaxFrame] is refused before any
// payload byte is read or any buffer is made for it.
func readFrame(r io.Reader) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, violation("truncated frame")
		}
		return nil, err
	}
	return readBody(r, hdr)
}

func readBody(r io.Reader, hdr [4]byte) ([]byte, error) {
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 {
		return nil, violation("empty frame")
	}
	if n > MaxFrame {
		return nil, violation("frame too large")
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, violation("truncated frame")
		}
		return nil, err
	}
	return buf, nil
}

// readDeadliner is the part of a connection that readFrameTimed needs.
type readDeadliner interface {
	io.Reader
	SetReadDeadline(time.Time) error
}

// readFrameTimed reads one frame from c. The first byte may take up to idle to
// come; once it has come, the rest of the frame (the remaining header and the
// payload) must follow within body. So a peer that sends half a frame and
// stops (a slowloris) is cut off after body, while a peer that is quiet
// between frames is cut off after idle. now is the clock.
func readFrameTimed(c readDeadliner, now func() time.Time, idle, body time.Duration) ([]byte, error) {
	var hdr [4]byte
	if err := c.SetReadDeadline(now().Add(idle)); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(c, hdr[:1]); err != nil {
		return nil, err
	}
	if err := c.SetReadDeadline(now().Add(body)); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(c, hdr[1:]); err != nil {
		return nil, midFrame(err)
	}
	p, err := readBody(c, hdr)
	return p, err
}

// midFrame turns the end of input inside a frame into a violation and leaves
// a timeout as it is.
func midFrame(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return violation("truncated frame")
	}
	return err
}

// writeFrame writes payload as one frame, in one call to w.Write so that two
// writers holding a lock around it cannot interleave. The payload must be
// 1 to [MaxFrame] bytes.
func writeFrame(w io.Writer, payload []byte) error {
	if len(payload) == 0 || len(payload) > MaxFrame {
		return violation("frame size")
	}
	buf := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(buf, uint32(len(payload)))
	copy(buf[4:], payload)
	_, err := w.Write(buf)
	return err
}
