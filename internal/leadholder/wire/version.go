package wire

import (
	"bytes"
	"encoding/json"
	"strconv"
)

// HelloVersion reads the version of a Hello payload and nothing else, for a
// holder that has to answer a client of another protocol version. It is
// lenient where [DecodeHello] is strict: the payload may have fields this
// package does not know, and the other fields are not checked. It reports
// false when the payload is longer than a Hello may be ([MaxClientPayload]),
// is not a JSON object, or has no "version", more than one, or one that is
// not an integer from 1 to [MaxDimension].
//
// A holder uses it when [DecodeHello] fails or returns a Version other than
// [ProtocolVersion]: with a version that is not its own it sends a
// refused-version notice and closes; without a version the payload is
// garbage.
func HelloVersion(payload []byte) (int, bool) { return payloadVersion(payload, MaxClientPayload) }

// WelcomeVersion is [HelloVersion] for a Welcome payload (at most
// [MaxHolderPayload] bytes): a client that cannot decode a Welcome uses it to
// tell a newer holder from garbage.
func WelcomeVersion(payload []byte) (int, bool) { return payloadVersion(payload, MaxHolderPayload) }

// payloadVersion finds the top-level "version" of a JSON object. It reads
// the object once with the token decoder and keeps no value: a value it skips
// is dropped as soon as it is read, so the work and the memory are bounded by
// the payload length, which is checked first. Text after the object is not
// read.
func payloadVersion(data []byte, limit int) (int, bool) {
	if len(data) > limit {
		return 0, false
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return 0, false
	}
	version, found := 0, false
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return 0, false
		}
		key, ok := tok.(string)
		if !ok {
			return 0, false
		}
		if key != "version" {
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return 0, false
			}
			continue
		}
		if found {
			return 0, false
		}
		tok, err = dec.Token()
		if err != nil {
			return 0, false
		}
		num, ok := tok.(json.Number)
		if !ok {
			return 0, false
		}
		n, err := strconv.Atoi(num.String())
		if err != nil || n < 1 || n > MaxDimension {
			return 0, false
		}
		version, found = n, true
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return 0, false
	}
	return version, found
}
