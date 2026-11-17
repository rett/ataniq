package wire

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
	// OpInject is in the vocabulary only: in this feature the holder answers
	// it with [CodeDisabled].
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
	// only while there is no viewer.
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
type Request struct {
	Op        Operation `json:"op" wire:"required"`
	Epoch     uint64    `json:"epoch" wire:"required"`
	Role      Role      `json:"role,omitempty"`
	Ticket    string    `json:"ticket,omitempty"`
	CommandID string    `json:"command_id,omitempty"`
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
	return nil
}

// EncodeRequest returns the payload of a control request frame.
func EncodeRequest(r Request) ([]byte, error) { return encodeMessage(TypeRequest, &r) }

// DecodeRequest decodes and validates the payload of a control request frame.
func DecodeRequest(payload []byte) (Request, error) {
	var r Request
	err := decodeMessage(&r, payload)
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
// code and a short text. A status answer carries Status; an answer to any
// other operation is a bare OK.
type Response struct {
	OK     bool    `json:"ok"`
	Code   string  `json:"code,omitempty"`
	Text   string  `json:"text,omitempty"`
	Status *Status `json:"status,omitempty"`
}

func (r *Response) validate() error {
	if r.OK {
		if r.Code != "" || r.Text != "" {
			return invalidf("an ok response has a refusal code or text")
		}
		if r.Status != nil {
			return r.Status.validate()
		}
		return nil
	}
	if r.Status != nil {
		return invalidf("a refusal carries a status")
	}
	if err := checkToken("code", r.Code, MaxToken); err != nil {
		return err
	}
	return checkText("text", r.Text, 0, MaxNoticeText, false)
}

// EncodeResponse returns the payload of a control response frame.
func EncodeResponse(r Response) ([]byte, error) {
	if r.Status != nil && r.Status.Counters == nil {
		s := *r.Status
		s.Counters = map[string]uint64{}
		r.Status = &s
	}
	return encodeMessage(TypeResponse, &r)
}

// DecodeResponse decodes and validates the payload of a control response
// frame.
func DecodeResponse(payload []byte) (Response, error) {
	var r Response
	err := decodeMessage(&r, payload)
	return pick(r, err)
}
