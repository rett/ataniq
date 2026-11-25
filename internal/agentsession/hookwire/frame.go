// Package hookwire implements version 1 of the hook port. Each short connection
// carries one request and, for a fetch, at most one reply. The caller supplies
// one complete length-prefixed frame, with the payload bounded before reading
// it from the connection. The codec needs neither a connection nor EOF.
package hookwire

import (
	"bytes"
	"encoding/binary"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	Version = 1
	Event   = "event"
	Fetch   = "fetch"
	Message = "message"
	None    = "none"

	// Payload bounds exclude the four-byte big-endian length.
	MaxRequestBytes = 8 << 10
	MaxReplyBytes   = 9 << 10
	MaxNoneBytes    = 32
	MaxTextBytes    = 4 << 10
	MaxInputBytes   = 1 << 20
	MaxFieldBytes   = 256
	MaxFields       = 16
	MaxDigests      = 4
	maxNameBytes    = 32
	maxRowBytes     = 40
)

var (
	rowID       = regexp.MustCompile(`^[a-z0-9.-]+$`)
	fieldName   = regexp.MustCompile(`^[a-z_]+$`)
	digestValue = regexp.MustCompile(`^[0-9a-f]{64}$`)
	messageID   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
)

func validRow(s string) bool  { return len(s) <= maxRowBytes && rowID.MatchString(s) }
func validName(s string) bool { return len(s) <= maxNameBytes && fieldName.MatchString(s) }

// Request is the sandbox-to-holder frame. Fields and Digests are JSON objects,
// empty when Cut is true. Size counts bytes kept, excluding the lookahead byte.
type Request struct {
	Version int               `json:"v"`
	Kind    string            `json:"k"`
	Row     string            `json:"row"`
	Fields  map[string]string `json:"fields"`
	Digests map[string]string `json:"digests"`
	Size    int               `json:"size"`
	Cut     bool              `json:"cut"`
}

func (r Request) validate() error {
	if r.Version != Version || (r.Kind != Event && r.Kind != Fetch) || !validRow(r.Row) {
		return errors.New("invalid hook request header")
	}
	if r.Size < 0 || r.Size > MaxInputBytes || len(r.Fields) > MaxFields || len(r.Digests) > MaxDigests {
		return errors.New("hook request exceeds bounds")
	}
	if r.Cut && (len(r.Fields) != 0 || len(r.Digests) != 0) {
		return errors.New("cut input carries extracted values")
	}
	for name, s := range r.Fields {
		if !validName(name) || len(s) > MaxFieldBytes || !utf8.ValidString(s) || strings.IndexFunc(s, nonPrintable) >= 0 {
			return errors.New("invalid hook field")
		}
	}
	for name, s := range r.Digests {
		if !validName(name) || !digestValue.MatchString(s) {
			return errors.New("invalid hook digest")
		}
	}
	return nil
}

func nonPrintable(r rune) bool { return !unicode.IsPrint(r) }

// Reply is the holder-to-sandbox frame. Only Message carries ID and Text.
// Text includes the matching seat header, relay line and already admitted body.
type Reply struct {
	Version int    `json:"v"`
	Kind    string `json:"k"`
	ID      string `json:"id,omitzero"`
	Text    string `json:"text,omitzero"`
}

func (r Reply) validate() error {
	if r.Version != Version {
		return errors.New("invalid hook reply version")
	}
	switch r.Kind {
	case None:
		if r.ID != "" || r.Text != "" {
			return errors.New("none carries message members")
		}
	case Message:
		if !messageID.MatchString(r.ID) || len(r.Text) > MaxTextBytes || !utf8.ValidString(r.Text) {
			return errors.New("invalid hook message")
		}
		header := "[ataniq-msg id=" + r.ID + " from=seat kind=chat]\nRelayed note from the owner. It carries no approval and no grant.\n"
		body, ok := strings.CutPrefix(r.Text, header)
		if !ok || strings.Trim(body, " \n") == "" || strings.IndexFunc(body, func(r rune) bool { return r != '\n' && nonPrintable(r) }) >= 0 {
			return errors.New("invalid relayed text")
		}
	default:
		return errors.New("unknown hook reply kind")
	}
	return nil
}

// EncodeRequest returns the complete frame or nil on error.
func EncodeRequest(r Request) ([]byte, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	return frame(r, MaxRequestBytes)
}

// EncodeReply returns the complete frame or nil on error. Encoding uses the
// JSON v2 default of leaving printable HTML characters literal.
func EncodeReply(r Reply) ([]byte, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	if r.Kind == None {
		return frame(r, MaxNoneBytes)
	}
	return frame(r, MaxReplyBytes)
}

func frame(value any, limit int) ([]byte, error) {
	body, err := json.Marshal(value, json.Deterministic(true))
	if err != nil {
		return nil, err
	}
	if len(body) > limit {
		return nil, errors.New("hook frame exceeds limit")
	}
	return append(binary.BigEndian.AppendUint32(nil, uint32(len(body))), body...), nil
}

// A JSON null is never a substitute for a member of a concrete wire type.
var strictMembers = json.WithUnmarshalers(json.UnmarshalFromFunc(func(d *jsontext.Decoder, _ any) error {
	if d.PeekKind() == 'n' {
		return errors.New("unexpected null")
	}
	return errors.ErrUnsupported
}))

func decodeJSON(body []byte, dst any) error {
	d := jsontext.NewDecoder(bytes.NewReader(body))
	if err := json.UnmarshalDecode(d, dst, json.RejectUnknownMembers(true), strictMembers); err != nil {
		return err
	}
	if d.InputOffset() != int64(len(body)) {
		return errors.New("trailing bytes")
	}
	return nil
}

func unframe(data []byte, limit int, dst any) error {
	if len(data) < 4 || len(data)-4 > limit || binary.BigEndian.Uint32(data) != uint32(len(data)-4) {
		return errors.New("invalid hook frame length")
	}
	return decodeJSON(data[4:], dst)
}

// DecodeRequest checks the wire grammar. Admission also checks the request
// against the selected build's hook row before using the observation.
func DecodeRequest(data []byte) (*Request, error) {
	// Size zero and cut false are valid, so detect their absence explicitly.
	wire := struct {
		Request
		Cut *bool `json:"cut"`
	}{Request: Request{Size: -1}}
	if err := unframe(data, MaxRequestBytes, &wire); err != nil {
		return nil, err
	}
	if wire.Cut == nil || wire.Fields == nil || wire.Digests == nil {
		return nil, errors.New("missing request member")
	}
	wire.Request.Cut = *wire.Cut
	if err := wire.validate(); err != nil {
		return nil, err
	}
	return &wire.Request, nil
}

// DecodeReply accepts one complete message or none frame.
func DecodeReply(data []byte) (*Reply, error) {
	var wire struct {
		Reply
		ID   *string `json:"id"`
		Text *string `json:"text"`
	}
	if err := unframe(data, MaxReplyBytes, &wire); err != nil {
		return nil, err
	}
	if wire.Kind == None && (wire.ID != nil || wire.Text != nil || len(data)-4 > MaxNoneBytes) {
		return nil, errors.New("invalid none frame")
	}
	if wire.ID != nil {
		wire.Reply.ID = *wire.ID
	}
	if wire.Text != nil {
		wire.Reply.Text = *wire.Text
	}
	if err := wire.validate(); err != nil {
		return nil, err
	}
	return &wire.Reply, nil
}
