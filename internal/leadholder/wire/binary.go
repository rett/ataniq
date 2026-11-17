package wire

import (
	"encoding/binary"
	"fmt"
	"unicode/utf8"
)

// positionLen is the length of the position that starts an Output and a
// Snapshot payload.
const positionLen = 8

// MaxOutputData is the most PTY bytes one Output frame carries.
const MaxOutputData = MaxHolderPayload - positionLen

// MaxSnapshotData is the most snapshot bytes one Snapshot frame carries.
const MaxSnapshotData = MaxSnapshotPayload - positionLen

func encodePositioned(typ byte, pos uint64, data []byte) ([]byte, error) {
	max, _ := Cap(typ)
	if len(data) > max-positionLen {
		return nil, fmt.Errorf("%w: %s data is %d bytes, cap %d", ErrTooLarge, TypeName(typ), len(data), max-positionLen)
	}
	out := make([]byte, positionLen+len(data))
	binary.BigEndian.PutUint64(out, pos)
	copy(out[positionLen:], data)
	return out, nil
}

func decodePositioned(typ byte, payload []byte) (uint64, []byte, error) {
	max, _ := Cap(typ)
	if len(payload) < positionLen {
		return 0, nil, fmt.Errorf("%w: %s payload is %d bytes, want a position of %d", ErrTooSmall, TypeName(typ), len(payload), positionLen)
	}
	if len(payload) > max {
		return 0, nil, fmt.Errorf("%w: %s payload is %d bytes, cap %d", ErrTooLarge, TypeName(typ), len(payload), max)
	}
	return binary.BigEndian.Uint64(payload), payload[positionLen:], nil
}

// EncodeOutput returns the payload of an Output frame: the 8-byte big-endian
// position of the first byte in the output stream, then the raw PTY bytes.
func EncodeOutput(pos uint64, data []byte) ([]byte, error) {
	return encodePositioned(TypeOutput, pos, data)
}

// DecodeOutput splits the payload of an Output frame. The returned data is a
// slice of payload.
func DecodeOutput(payload []byte) (pos uint64, data []byte, err error) {
	return decodePositioned(TypeOutput, payload)
}

// EncodeSnapshot returns the payload of a Snapshot frame: the 8-byte
// big-endian output position it corresponds to, then the opaque snapshot bytes
// that the session pane encodes.
func EncodeSnapshot(pos uint64, snapshot []byte) ([]byte, error) {
	return encodePositioned(TypeSnapshot, pos, snapshot)
}

// DecodeSnapshot splits the payload of a Snapshot frame. The returned bytes
// are a slice of payload.
func DecodeSnapshot(payload []byte) (pos uint64, snapshot []byte, err error) {
	return decodePositioned(TypeSnapshot, payload)
}

// The payload of an Input frame is the raw bytes, already encoded for the
// session (keys, mouse, focus reports), and needs no helper: the frame
// reader has checked its length. EncodePaste and DecodePaste build and check
// the payload of a Paste frame.

// EncodePaste returns the payload of a Paste frame: the text itself, which
// must be valid UTF-8 and at most [MaxClientPayload] bytes.
func EncodePaste(text string) ([]byte, error) {
	if len(text) > MaxClientPayload {
		return nil, fmt.Errorf("%w: paste text is %d bytes, cap %d", ErrTooLarge, len(text), MaxClientPayload)
	}
	if !utf8.ValidString(text) {
		return nil, invalidf("paste text is not valid UTF-8")
	}
	return []byte(text), nil
}

// DecodePaste returns the text of a Paste frame, refusing a payload that is
// not valid UTF-8 or is over the cap.
func DecodePaste(payload []byte) (string, error) {
	if len(payload) > MaxClientPayload {
		return "", fmt.Errorf("%w: paste payload is %d bytes, cap %d", ErrTooLarge, len(payload), MaxClientPayload)
	}
	if !utf8.Valid(payload) {
		return "", invalidf("paste text is not valid UTF-8")
	}
	return string(payload), nil
}
