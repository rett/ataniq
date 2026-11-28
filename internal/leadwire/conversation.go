package leadwire

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"
)

// The desk's conversation read: one operation of its own, beside the six of
// lead control and the three of the chat switch, listed apart by
// [ConversationOperations]. It is the desk's form of the one conversation page
// value that the seat reads too (internal/appcontract's Page): the same
// message ID, the same four page states, the same opaque cursors. No daemon
// serves it yet; these are the shapes and the strict decoders a daemon and the
// console's client will share.
//
//	conversation.read  [ConversationReadParams] -> [ConversationReadResult]
//
// A read takes the lead's key, a direction and an opaque cursor, and answers
// one page. With no cursor it answers the newest entries and is backward; a
// cursor is passed back exactly as the page gave it. A cursor the daemon
// cannot use is not an error: the answer is a page whose state is
// [PageStateReset], holding nothing else, and the desk discards its page and
// cursors and reads afresh. Like the chat switch it is for the daemon's own
// user on the Unix socket; the seat gate does not serve it, so its answers are
// not bound by [MaxRelayBytes] but by [MaxConversationResultBytes].
//
// The constant is an Op* constant like the others, so that
// tests/pack_inventory_test.go binds it to the desk's operation inventory and
// holds a classified row for it in its golden; the row is classed read and
// names no handler until the daemon adds one.
const OpConversationRead = "conversation.read"

// ConversationOperations returns the conversation operations, in a new slice:
// conversation.read.
func ConversationOperations() []string { return []string{OpConversationRead} }

// The directions of a read.
const (
	ReadBackward = "backward"
	ReadForward  = "forward"
)

// The states of a page: what the reader does at its older edge.
const (
	// PageStateMore: older entries can be read with the page's Older cursor.
	PageStateMore = "more"
	// PageStateEnd: the start of the readable segment is accounted for.
	PageStateEnd = "end"
	// PageStateGap: a known loss lies before the first entry. It has no size.
	PageStateGap = "gap"
	// PageStateReset: the continuation cannot be used; read afresh.
	PageStateReset = "reset"
)

// PageStates returns the four page states and "more", in a new slice, in the
// order above.
func PageStates() []string {
	return []string{PageStateMore, PageStateEnd, PageStateGap, PageStateReset}
}

// The kinds of an entry: its provenance label, assigned by the authority.
// [EntryKindWithheld] marks an authorized omission and holds no content.
const (
	EntryKindReply       = "reply"
	EntryKindInstruction = "instruction"
	EntryKindReceipt     = "receipt"
	EntryKindSystem      = "system"
	EntryKindWithheld    = "withheld"
)

// EntryKinds returns the five kinds above, in a new slice, in that order.
func EntryKinds() []string {
	return []string{EntryKindReply, EntryKindInstruction, EntryKindReceipt, EntryKindSystem, EntryKindWithheld}
}

// Caps of the conversation read.
const (
	// MaxConversationLimit is the most entries a page holds.
	MaxConversationLimit = 200
	// MaxCursorBytes is the longest cursor a read or a page carries.
	MaxCursorBytes = 256
	// MaxConversationText is the longest text of an entry, in bytes.
	MaxConversationText = 64 << 10
	// MaxConversationParamsBytes is the most the params of a read occupy.
	MaxConversationParamsBytes = 4 << 10
	// MaxConversationResultBytes is the most one answer occupies, in bytes.
	MaxConversationResultBytes = 1 << 20
	// MaxConversationWord is the longest identifier, revision or command
	// word of a page, in bytes.
	MaxConversationWord = 128
)

type (
	// ConversationReadParams are the params of conversation.read. Every key
	// is required. Cursor is "" for the first read, which is backward; Limit
	// 0 asks for the daemon's default page.
	ConversationReadParams struct {
		Key       string `json:"key"`
		Direction string `json:"direction"`
		Cursor    string `json:"cursor"`
		Limit     int    `json:"limit"`
	}

	// ConversationReadResult is the result of conversation.read: the lead's
	// key, the page's state, its entries in ascending order, and the two
	// cursors. Older is "" unless State is [PageStateMore]. Newer continues
	// a forward read: it is present on the first page and on every forward
	// page, and "" on an older page and at a reset. A producer sends an empty
	// array, never null, for a page without entries.
	ConversationReadResult struct {
		Key     string              `json:"key"`
		State   string              `json:"state"`
		Entries []ConversationEntry `json:"entries"`
		Older   string              `json:"older"`
		Newer   string              `json:"newer"`
	}

	// ConversationMessageID is the identity of an entry as one viewer's
	// disclosure projection reads it: the tuple of internal/appmodel's
	// MessageID. Compare whole values only within one viewer and projection, and
	// drop them with the page on reset_required; the shared vectors are
	// internal/appmodel/testdata/message-ids-v1.json.
	ConversationMessageID struct {
		Authority    string `json:"authority"`
		Lead         string `json:"lead"`
		Conversation string `json:"conversation"`
		Generation   uint64 `json:"generation"`
		Seq          uint64 `json:"seq"`
	}

	// ConversationEntry is one entry. Text is untrusted. At is the time the
	// authority recorded, in RFC 3339, "" for a withheld entry, which has no
	// other field set. Command, Receipt and Status are null unless the kind
	// uses them: an instruction names its command, a receipt also holds the
	// immutable admission record, and either shows the command's current
	// status when the reader may see it.
	ConversationEntry struct {
		ID      ConversationMessageID `json:"id"`
		Kind    string                `json:"kind"`
		Text    string                `json:"text"`
		At      string                `json:"at"`
		Command *ConversationCommand  `json:"command"`
		Receipt *ConversationReceipt  `json:"receipt"`
		Status  *ConversationStatus   `json:"status"`
	}

	// ConversationCommand is the key of a command: authority, enrolled
	// client and command id.
	ConversationCommand struct {
		Authority string `json:"authority"`
		Client    string `json:"client"`
		Command   string `json:"command"`
	}

	// ConversationReceipt is the immutable admission record of a command: the
	// SHA-256 of its binding in 64 lower-case hex digits, the revision after
	// admission and the historical cursor.
	ConversationReceipt struct {
		Digest           string `json:"digest"`
		AdmittedRevision string `json:"admitted_revision"`
		Cursor           string `json:"cursor"`
	}

	// ConversationStatus is the command's current, separately revisioned
	// status: queued, held, cancelled or applied.
	ConversationStatus struct {
		Revision string `json:"revision"`
		State    string `json:"state"`
	}
)

// DecodeConversationReadParams decodes and judges the params of a read:
// every key present, no other, no duplicate, nothing after the value, and
// every value in its domain.
func DecodeConversationReadParams(data []byte) (ConversationReadParams, error) {
	var p ConversationReadParams
	if len(data) > MaxConversationParamsBytes {
		return p, errors.New("conversation.read params too large")
	}
	if err := decodeStrict(data, &p); err != nil {
		return ConversationReadParams{}, err
	}
	if p.Key == "" || len(p.Key) > MaxKey || !utf8.ValidString(p.Key) {
		return ConversationReadParams{}, errors.New("conversation.read: bad key")
	}
	if p.Direction != ReadBackward && p.Direction != ReadForward {
		return ConversationReadParams{}, errors.New("conversation.read: bad direction")
	}
	if len(p.Cursor) > MaxCursorBytes || (p.Cursor == "" && p.Direction != ReadBackward) {
		return ConversationReadParams{}, errors.New("conversation.read: bad cursor")
	}
	if p.Limit < 0 || p.Limit > MaxConversationLimit {
		return ConversationReadParams{}, errors.New("conversation.read: bad limit")
	}
	return p, nil
}

// DecodeConversationReadResult decodes and judges one answer, strictly as
// above, and refuses a page that contradicts itself: a state outside the
// four, cursors a state cannot have, entries out of order or of mixed
// segments, a withheld entry with content, or a field a kind cannot carry.
func DecodeConversationReadResult(data []byte) (ConversationReadResult, error) {
	var r ConversationReadResult
	if len(data) > MaxConversationResultBytes {
		return r, errors.New("conversation.read result too large")
	}
	if err := decodeStrict(data, &r); err != nil {
		return ConversationReadResult{}, err
	}
	if err := r.check(); err != nil {
		return ConversationReadResult{}, err
	}
	return r, nil
}

var errPage = errors.New("conversation.read: invalid page")

func (r ConversationReadResult) check() error {
	if r.Key == "" || len(r.Key) > MaxKey || len(r.Older) > MaxCursorBytes || len(r.Newer) > MaxCursorBytes || len(r.Entries) > MaxConversationLimit {
		return errPage
	}
	switch r.State {
	case PageStateMore:
		if r.Older == "" {
			return errPage
		}
	case PageStateEnd, PageStateGap:
		if r.Older != "" {
			return errPage
		}
	case PageStateReset:
		if len(r.Entries) != 0 || r.Older != "" || r.Newer != "" {
			return errPage
		}
	default:
		return errPage
	}
	var prev ConversationMessageID
	for i, e := range r.Entries {
		if err := e.check(); err != nil {
			return err
		}
		if i > 0 && (e.ID.Generation != prev.Generation || e.ID.Seq <= prev.Seq ||
			e.ID.Authority != prev.Authority || e.ID.Lead != prev.Lead || e.ID.Conversation != prev.Conversation) {
			return errPage
		}
		prev = e.ID
	}
	return nil
}

func (e ConversationEntry) check() error {
	for _, w := range []string{e.ID.Authority, e.ID.Lead, e.ID.Conversation} {
		if !word(w) {
			return errPage
		}
	}
	if e.ID.Generation == 0 || e.ID.Generation > 1<<63-1 || e.ID.Seq == 0 || e.ID.Seq > 1<<63-1 ||
		len(e.Text) > MaxConversationText || !utf8.ValidString(e.Text) {
		return errPage
	}
	if e.Kind == EntryKindWithheld {
		if e.Text != "" || e.At != "" || e.Command != nil || e.Receipt != nil || e.Status != nil {
			return errPage
		}
		return nil
	}
	if _, err := time.Parse(time.RFC3339Nano, e.At); err != nil {
		return errPage
	}
	switch e.Kind {
	case EntryKindReply, EntryKindSystem:
		if e.Command != nil || e.Receipt != nil || e.Status != nil {
			return errPage
		}
		return nil
	case EntryKindInstruction:
		if e.Command == nil || e.Receipt != nil {
			return errPage
		}
	case EntryKindReceipt:
		if e.Command == nil || e.Receipt == nil {
			return errPage
		}
	default:
		return errPage
	}
	if !word(e.Command.Authority) || !word(e.Command.Client) || !word(e.Command.Command) {
		return errPage
	}
	if rc := e.Receipt; rc != nil {
		d, err := hex.DecodeString(rc.Digest)
		if err != nil || len(d) != 32 || rc.Digest != strings.ToLower(rc.Digest) || !word(rc.AdmittedRevision) || len(rc.Cursor) > MaxCursorBytes {
			return errPage
		}
	}
	if st := e.Status; st != nil {
		switch st.State {
		case "queued", "held", "cancelled", "applied":
		default:
			return errPage
		}
		if !word(st.Revision) {
			return errPage
		}
	}
	return nil
}

// word reports whether s is 1 through MaxConversationWord bytes of the
// application identifier alphabet: ASCII letters, digits and . _ : -.
func word(s string) bool {
	if s == "" || len(s) > MaxConversationWord {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == ':' || c == '-') {
			return false
		}
	}
	return true
}

// decodeStrict decodes data into the pointer v. Every object, at every depth,
// must hold exactly the keys its struct names (every key is required, a null
// is a value, a key repeated or not named is refused), and nothing may follow
// the value.
func decodeStrict(data []byte, v any) error {
	if err := requireKeys(data, reflect.TypeOf(v).Elem()); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("data after the value")
	}
	return nil
}

var errKeys = errors.New("json: object keys are not exactly the required keys")

// requireKeys walks raw by the shape of t: a struct must be an object with
// exactly its tagged keys, once each; a pointer may be null; a slice must be
// an array (not null) whose elements are walked.
func requireKeys(raw []byte, t reflect.Type) error {
	raw = bytes.TrimSpace(raw)
	if t.Kind() == reflect.Pointer {
		if string(raw) == "null" {
			return nil
		}
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		members, err := objectMembers(raw)
		if err != nil {
			return err
		}
		if len(members) != t.NumField() {
			return errKeys
		}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			sub, ok := members[name]
			if !ok {
				return errKeys
			}
			if err := requireKeys(sub, f.Type); err != nil {
				return err
			}
		}
	case reflect.Slice:
		var elems []json.RawMessage
		if string(raw) == "null" {
			return errKeys
		}
		if err := json.Unmarshal(raw, &elems); err != nil {
			return err
		}
		for _, e := range elems {
			if err := requireKeys(e, t.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

// objectMembers returns the members of one JSON object, refusing a repeated key.
func objectMembers(raw []byte) (map[string][]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, errKeys
	}
	members := map[string][]byte{}
	for dec.More() {
		tok, err := dec.Token()
		name, ok := tok.(string)
		if err != nil || !ok {
			return nil, errKeys
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		if _, dup := members[name]; dup {
			return nil, errors.New("json: repeated object key")
		}
		members[name] = v
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, errKeys
	}
	return members, nil
}
