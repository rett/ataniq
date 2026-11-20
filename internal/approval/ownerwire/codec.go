package ownerwire

import (
	"strconv"

	"ataniq/internal/approval/record"
)

// The payloads are read and written by the strict reader and the tag codec
// of the record package ([record.Reader], [record.Marshal]), so that a
// payload and a record are refused for the same reasons and cannot drift
// apart. This file sets the reader for a payload and shapes its refusals.

// reader is the strict reader of every payload. The deepest form (the b64
// text of a line of a page) needs four levels, and 8 is the room given.
var reader = record.Reader{MaxDepth: 8, Noun: "payload"}

// decode parses data under the rules of a record and fills dst, a pointer
// to a payload type. limit is the cap of the payload, invalid the code for
// one that is not well formed, and name the payload's name for the detail.
func decode(data []byte, limit int, invalid record.Code, name string, dst any) error {
	if len(data) > limit {
		return tooLarge(name, limit)
	}
	if err := reader.Unmarshal(data, name, dst); err != nil {
		return &record.Error{Code: invalid, Detail: err.Error()}
	}
	return nil
}

// encode writes src, a pointer to a payload type, and holds the cap.
func encode(src any, limit int, invalid record.Code, name string) ([]byte, error) {
	out, err := record.Marshal(src, name)
	if err != nil {
		return nil, &record.Error{Code: invalid, Detail: err.Error()}
	}
	if len(out) > limit {
		return nil, tooLarge(name, limit)
	}
	return out, nil
}

func tooLarge(name string, limit int) *record.Error {
	return &record.Error{Code: record.CodeRecordTooLarge, Detail: name + " is larger than " + strconv.Itoa(limit) + " bytes"}
}
