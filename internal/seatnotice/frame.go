package seatnotice

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// MaxFrame is the largest payload of a frame, in bytes.
const MaxFrame = 4 << 10

// maxDepth is how deeply objects may nest in a frame; the frame's own object
// is the first level, and a subscription is the second.
const maxDepth = 4

// ErrProtocol is what every violation of the wire rules wraps. Its text, and
// the text of what wraps it, is fixed here and holds no byte of what the peer
// sent.
var ErrProtocol = errors.New("seatnotice: protocol violation")

func violation(what string) error { return fmt.Errorf("%w: %s", ErrProtocol, what) }

// readFrame reads one frame from r. A length of zero or above [MaxFrame] is
// refused before any payload byte is read.
func readFrame(r io.Reader) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, violation("no frame")
		}
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, violation("truncated frame")
		}
		return nil, err
	}
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

// writeFrame writes payload as one frame in one call to w.Write.
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
