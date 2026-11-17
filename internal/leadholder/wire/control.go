package wire

// Operation is what the daemon asks of the holder on the control socket.
type Operation string

const (
	// OpTicket asks for a single-use attach ticket for a role.
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
	// CodeBusy answers a ticket for a role that is taken.
	CodeBusy = "busy"
	// CodeBadRequest answers a request that does not make sense.
	CodeBadRequest = "bad-request"
)

// Request is the payload of a control request. Every request carries the
// epoch it is meant for; the holder refuses another. Role is the role wanted
// and is set for a ticket only.
type Request struct {
	Op    Operation `json:"op"`
	Epoch uint64    `json:"epoch"`
	Role  Role      `json:"role,omitempty"`
}

func (r *Request) validate() error {
	if err := r.Op.check(); err != nil {
		return err
	}
	if r.Op == OpTicket {
		return r.Role.check("role")
	}
	if r.Role != "" {
		return invalidf("role is set for op %q", string(r.Op))
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

// Status is the holder's answer to a status request.
type Status struct {
	Lead          string            `json:"lead"`
	Epoch         uint64            `json:"epoch"`
	Runtime       string            `json:"runtime"`
	Size          Size              `json:"size"`
	Caps          Caps              `json:"caps"`
	WriterPresent bool              `json:"writer_present"`
	ViewerPresent bool              `json:"viewer_present"`
	Counters      map[string]uint64 `json:"counters"`
	Facts         Facts             `json:"facts"`
}

func (s *Status) validate() error {
	if err := checkText("lead", s.Lead, 1, MaxLead, false); err != nil {
		return err
	}
	if err := checkText("runtime", s.Runtime, 1, MaxRuntime, false); err != nil {
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
// code and a short text. A ticket answer carries Ticket and a status answer
// carries Status.
type Response struct {
	OK     bool    `json:"ok"`
	Code   string  `json:"code,omitempty"`
	Text   string  `json:"text,omitempty"`
	Ticket string  `json:"ticket,omitempty"`
	Status *Status `json:"status,omitempty"`
}

func (r *Response) validate() error {
	if r.OK {
		if r.Code != "" || r.Text != "" {
			return invalidf("an ok response has a refusal code or text")
		}
		if r.Ticket != "" {
			if err := CheckTicket(r.Ticket); err != nil {
				return err
			}
		}
		if r.Status != nil {
			if r.Ticket != "" {
				return invalidf("a response has both a ticket and a status")
			}
			return r.Status.validate()
		}
		return nil
	}
	if r.Ticket != "" || r.Status != nil {
		return invalidf("a refusal carries a ticket or a status")
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
