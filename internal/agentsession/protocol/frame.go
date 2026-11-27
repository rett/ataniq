package protocol

import (
	"bytes"
	"errors"
	"io"
)

// MaxFrameBytes includes the terminating LF. Codec payloads exclude it.
const MaxFrameBytes = 64 << 10

var (
	ErrFrameTooLarge = errors.New("protocol frame exceeds limit")
	ErrInvalidFrame  = errors.New("invalid protocol frame")
)

func checkPayload(payload []byte) error {
	if len(payload) >= MaxFrameBytes {
		return ErrFrameTooLarge
	}
	if len(payload) == 0 || bytes.IndexByte(payload, '\n') >= 0 {
		return ErrInvalidFrame
	}
	return nil
}

// Framer splits a byte stream at LF. Its zero value starts a new stream.
type Framer struct {
	line     []byte
	dropping bool
}

// Feed calls accept once per complete line, including LF, and returns the
// number of oversized frames dropped. The callback borrows the line until it
// returns. An oversized unterminated line costs at most one buffer and one drop.
func (f *Framer) Feed(chunk []byte, accept func([]byte)) (dropped int) {
	for len(chunk) > 0 {
		n := bytes.IndexByte(chunk, '\n') + 1
		complete := n != 0
		if !complete {
			n = len(chunk)
		}
		if !f.dropping {
			room := MaxFrameBytes - len(f.line)
			if n > room || n == room && !complete {
				dropped++
				f.dropping = true
			} else {
				f.line = append(f.line, chunk[:n]...)
				if complete {
					accept(f.line)
				}
			}
		}
		if complete {
			f.line = f.line[:0]
			f.dropping = false
		}
		chunk = chunk[n:]
	}
	return dropped
}

// Finish closes the stream and resets the framer. A final fragment cannot
// become a frame. An oversized fragment was already counted by Feed.
func (f *Framer) Finish() error {
	truncated := !f.dropping && len(f.line) != 0
	*f = Framer{}
	if truncated {
		return io.ErrUnexpectedEOF
	}
	return nil
}
