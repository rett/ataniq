package wire

import "encoding/json"

// Operation is what the daemon asks of the holder on the control socket.
type Operation string

const (
	// OpTicket gives the holder a single-use attach ticket that the caller
	// minted (see [NewTicket]) for one attach role. The holder does not mint
	// tickets: the daemon gives the holder the ticket and the console the
	// same ticket (the lead-hosting design, section 2.1).
	OpTicket Operation = "ticket"
	// OpStatus asks for the holder's [Status].
	OpStatus Operation = "status"
	// OpStop asks the holder to stop its lead.
	OpStop Operation = "stop"
	// OpReleaseWriter removes the current writer.
	OpReleaseWriter Operation = "release-writer"
	// OpInject asks the holder to deliver a message to its lead. The request
	// carries the message's id and text; the answer is a [Response] with an
	// [InjectResult], or a refusal ([CodeDisabled], [CodeWrongEpoch],
	// [CodeBusy], [CodeBadRequest]) when nothing was written. A holder that
	// has no deliverer answers [CodeDisabled].
	OpInject Operation = "inject"
)

func (o Operation) check() error {
	switch o {
	case OpTicket, OpStatus, OpStop, OpReleaseWriter, OpInject:
		return nil
	}
	return invalidf("op is %q", string(o))
}

// Refusal codes of a [Response]. A decoder accepts any well-formed code.
const (
	// CodeDisabled answers an operation that exists but is off, as inject is.
	CodeDisabled = "disabled"
	// CodeWrongEpoch answers a request for another epoch than the holder's.
	CodeWrongEpoch = "wrong-epoch"
	// CodeBusy answers a ticket request for a role that is taken. A writer
	// ticket is accepted only while there is no writer, and a viewer ticket
	// only while there is no viewer. It also answers an inject request while
	// another delivery runs.
	CodeBusy = "busy"
	// CodeBadRequest answers a request that does not make sense.
	CodeBadRequest = "bad-request"
)

// Request is the payload of a control request. Every request carries the
// epoch it is meant for (1 or more; the holder refuses another).
//
// For OpTicket, Ticket is the ticket the caller minted (its form is checked
// here, whether it is single-use and unexpired is the holder's affair) and
// Role is the attach role it is for: [RoleWriter] or [RoleViewer]. The answer
// is a [Response] that is ok, or a refusal such as [CodeBusy]. No other
// operation has a Ticket or a Role.
//
// CommandID is optional for every operation: a token the daemon chooses for
// the command, at most [MaxCommandID] bytes, that the holder writes in its
// event log so that the command can be matched with the daemon's record.
//
// For OpInject, MessageID (see [CheckMessageID]) and Text (see
// [CheckInjectText]) carry the message. They are allowed on OpInject only and
// only together; an OpInject with neither is the form of an earlier version
// and still decodes, and the holder answers it with [CodeBadRequest] when it
// would deliver. A MessageID or a Text on any other operation is refused.
//
// Version is the control version of the frame (see [ControlVersion]): 0, the
// member left out, is version 1. [Request.FrameVersion] reads it.
type Request struct {
	Op        Operation `json:"op" wire:"required"`
	Epoch     uint64    `json:"epoch" wire:"required"`
	Role      Role      `json:"role,omitempty"`
	Ticket    string    `json:"ticket,omitempty"`
	CommandID string    `json:"command_id,omitempty"`
	MessageID string    `json:"message_id,omitempty"`
	Text      string    `json:"text,omitempty"`
	Version   int       `json:"version,omitempty"`
}

// ControlVersion is the version of the control frames (a [Request] and a
// [Response]) that this package speaks. The frames are versioned from 1: a
// frame carries its version in the member "version", and a frame without it
// is version 1, which is every frame a build before the member wrote. A
// version 1 frame is written without the member, so it is byte for byte what
// those builds write, and their decoders, which refuse a member they do not
// know, read it as before. A frame of a later version is written only to a
// peer that has said it reads that version (the holder's control hello, which
// does not exist yet); this package reads version 1 only and refuses a frame
// of any other version, an explicit 0 or null included, as it refuses any
// frame it cannot read.
const ControlVersion = 1

// checkControlVersion checks the version member of a control frame as it was
// decoded: v is the value, and present says the member was in the payload.
func checkControlVersion(v int, present bool) error {
	if v == ControlVersion || (v == 0 && !present) {
		return nil
	}
	return invalidf("control version %d; this build reads version %d", v, ControlVersion)
}

// FrameVersion is the control version of the request: [ControlVersion] when
// the member is left out.
func (r Request) FrameVersion() int { return frameVersion(r.Version) }

// FrameVersion is the control version of the response: [ControlVersion] when
// the member is left out.
func (r Response) FrameVersion() int { return frameVersion(r.Version) }

func frameVersion(v int) int {
	if v == 0 {
		return ControlVersion
	}
	return v
}

// hasMember reports whether the JSON object of a payload that has already
// been decoded has a top-level member named key.
func hasMember(payload []byte, key string) bool {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(payload, &obj); err != nil {
		return false
	}
	_, ok := obj[key]
	return ok
}

// checkCommandID checks a command id: 1 to [MaxCommandID] bytes of letters,
// digits and the signs - _ . : (so a UUID, a ULID and a counter all fit).
func checkCommandID(s string) error {
	if len(s) < 1 || len(s) > MaxCommandID {
		return invalidf("command_id is %d bytes, want 1 to %d", len(s), MaxCommandID)
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == ':') {
			return invalidf("command_id has a character outside A-Z a-z 0-9 - _ . :")
		}
	}
	return nil
}

func (r *Request) validate() error {
	if err := checkControlVersion(r.Version, false); err != nil {
		return err
	}
	if err := r.Op.check(); err != nil {
		return err
	}
	if err := checkEpoch(r.Epoch); err != nil {
		return err
	}
	if r.CommandID != "" {
		if err := checkCommandID(r.CommandID); err != nil {
			return err
		}
	}
	if r.Op != OpInject && (r.MessageID != "" || r.Text != "") {
		return invalidf("message_id or text is set for op %q", string(r.Op))
	}
	if r.Op == OpTicket {
		if err := r.Role.check("role"); err != nil {
			return err
		}
		return CheckTicket(r.Ticket)
	}
	if r.Role != "" {
		return invalidf("role is set for op %q", string(r.Op))
	}
	if r.Ticket != "" {
		return invalidf("ticket is set for op %q", string(r.Op))
	}
	if r.Op == OpInject && (r.MessageID != "" || r.Text != "") {
		if err := CheckMessageID(r.MessageID); err != nil {
			return err
		}
		return CheckInjectText(r.Text)
	}
	return nil
}

// EncodeRequest returns the payload of a control request frame. A request of
// version 1 is written without the version member (see [ControlVersion]); a
// request of another version is refused.
func EncodeRequest(r Request) ([]byte, error) {
	if r.Version == ControlVersion {
		r.Version = 0
	}
	return encodeMessage(TypeRequest, &r)
}

// DecodeRequest decodes and validates the payload of a control request frame.
// A request without a version member is version 1; one of another version is
// refused (see [ControlVersion]).
func DecodeRequest(payload []byte) (Request, error) {
	var r Request
	err := decodeMessage(&r, payload)
	if err == nil {
		err = checkControlVersion(r.Version, hasMember(payload, "version"))
	}
	return pick(r, err)
}

// Status is the holder's answer to a status request: what the cockpit shows
// of a lead. Idle is the idle classification ([IdleUnknown] in this feature)
// and Title the title of the lead's screen, as a [State] frame carries them;
// Facts are the three lifetime facts.
type Status struct {
	Lead          string            `json:"lead"`
	Epoch         uint64            `json:"epoch" wire:"required"`
	Runtime       string            `json:"runtime"`
	Idle          string            `json:"idle" wire:"required"`
	Title         string            `json:"title" wire:"required"`
	Size          Size              `json:"size"`
	Caps          Caps              `json:"caps"`
	WriterPresent bool              `json:"writer_present" wire:"required"`
	ViewerPresent bool              `json:"viewer_present" wire:"required"`
	Counters      map[string]uint64 `json:"counters"`
	Facts         Facts             `json:"facts"`
}

func (s *Status) validate() error {
	if err := checkEpoch(s.Epoch); err != nil {
		return err
	}
	if err := checkText("lead", s.Lead, 1, MaxLead, false); err != nil {
		return err
	}
	if err := checkText("runtime", s.Runtime, 1, MaxRuntime, false); err != nil {
		return err
	}
	if err := checkToken("idle", s.Idle, MaxToken); err != nil {
		return err
	}
	if err := checkText("title", s.Title, 0, MaxTitle, false); err != nil {
		return err
	}
	if err := s.Size.validate(); err != nil {
		return err
	}
	if err := s.Caps.validate(); err != nil {
		return err
	}
	if err := checkCounters(s.Counters); err != nil {
		return err
	}
	return s.Facts.validate()
}

// Response is the payload of a control response: OK, or a refusal with a
// code and a short text. A status answer carries Status; the answer of a
// delivery that was tried carries Inject; an answer to any other operation is
// a bare OK. Inject is only on an ok response and never together with Status.
// Version is the control version of the frame, as on a [Request].
type Response struct {
	OK      bool          `json:"ok"`
	Code    string        `json:"code,omitempty"`
	Text    string        `json:"text,omitempty"`
	Status  *Status       `json:"status,omitempty"`
	Inject  *InjectResult `json:"inject,omitempty"`
	Version int           `json:"version,omitempty"`
}

func (r *Response) validate() error {
	if err := checkControlVersion(r.Version, false); err != nil {
		return err
	}
	if r.OK {
		if r.Code != "" || r.Text != "" {
			return invalidf("an ok response has a refusal code or text")
		}
		if r.Status != nil && r.Inject != nil {
			return invalidf("a response carries both a status and an inject result")
		}
		if r.Inject != nil {
			return r.Inject.Validate()
		}
		if r.Status != nil {
			return r.Status.validate()
		}
		return nil
	}
	if r.Status != nil {
		return invalidf("a refusal carries a status")
	}
	if r.Inject != nil {
		return invalidf("a refusal carries an inject result")
	}
	if err := checkToken("code", r.Code, MaxToken); err != nil {
		return err
	}
	return checkText("text", r.Text, 0, MaxNoticeText, false)
}

// EncodeResponse returns the payload of a control response frame. A response
// of version 1 is written without the version member (see [ControlVersion]);
// a response of another version is refused.
func EncodeResponse(r Response) ([]byte, error) {
	if r.Version == ControlVersion {
		r.Version = 0
	}
	if r.Status != nil && r.Status.Counters == nil {
		s := *r.Status
		s.Counters = map[string]uint64{}
		r.Status = &s
	}
	return encodeMessage(TypeResponse, &r)
}

// DecodeResponse decodes and validates the payload of a control response
// frame. A response without a version member is version 1; one of another
// version is refused (see [ControlVersion]).
func DecodeResponse(payload []byte) (Response, error) {
	var r Response
	err := decodeMessage(&r, payload)
	if err == nil {
		err = checkControlVersion(r.Version, hasMember(payload, "version"))
	}
	return pick(r, err)
}
