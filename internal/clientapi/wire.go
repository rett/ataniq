package clientapi

import (
	"bytes"
	"context"
	"encoding/json"
	"time"
	"unicode/utf8"
)

// Protocol is the protocol name of this version, sent in every request and
// response.
const Protocol = "ataniq.client/1"

// The limits of the wire. They are advertised in the hello result.
const (
	// MaxRequestBytes is the longest request line, without its LF.
	MaxRequestBytes = 64 << 10
	// MaxResponseBytes is the longest response line, with its LF.
	MaxResponseBytes = 1 << 20
	// MaxRequestIDLen is the longest request_id.
	MaxRequestIDLen = 64
)

// The operations of version 1. Both are read-only.
const (
	// OpHello asks for the protocols, limits and operations the server offers.
	OpHello = "hello"
	// OpStatus asks for the daemon block and the leads.
	OpStatus = "status"
)

// Operations returns the operations version 1 offers, in a fresh slice.
func Operations() []string { return []string{OpHello, OpStatus} }

// SupportedProtocols returns the protocol names the server speaks, in a fresh
// slice.
func SupportedProtocols() []string { return []string{Protocol} }

// ErrorCode is the machine-readable code of an error response. The codes are
// constants.
type ErrorCode string

// The error codes.
const (
	// CodeInvalidRequest: the line is not UTF-8, not JSON, not an object, or
	// lacks or mistypes a required field.
	CodeInvalidRequest ErrorCode = "invalid-request"
	// CodeTooLarge: the request line is over the request cap. The connection
	// is closed after this response.
	CodeTooLarge ErrorCode = "too-large"
	// CodeUnsupportedProtocol: the protocol name is not spoken by the server.
	// The error carries the supported protocols.
	CodeUnsupportedProtocol ErrorCode = "unsupported-protocol"
	// CodeUnsupportedOperation: the operation is not "hello" or "status".
	CodeUnsupportedOperation ErrorCode = "unsupported-operation"
	// CodeUnauthorized: the peer UID differs from the server's. The
	// connection is closed after this response.
	CodeUnauthorized ErrorCode = "unauthorized"
	// CodeBusy: the server is at its connection cap. The connection is closed
	// after this response.
	CodeBusy ErrorCode = "busy"
	// CodeInternal: the handler failed or panicked, or the response would
	// exceed the response cap.
	CodeInternal ErrorCode = "internal"
)

// Fixed texts of the error messages. None includes anything a peer sent.
const (
	msgInvalidRequest       = "the request is not a valid request object"
	msgTooLarge             = "the request line exceeds the request limit"
	msgUnsupportedProtocol  = "the protocol is not supported"
	msgUnsupportedOperation = "the operation is not supported"
	msgUnauthorized         = "the peer is not authorized"
	msgBusy                 = "the server is at its connection limit"
	msgInternal             = "the request could not be served"
	msgResponseTooLarge     = "the response exceeds the response limit"
)

// ErrorBody is the error member of an error response.
type ErrorBody struct {
	// Code is the machine-readable code.
	Code ErrorCode `json:"code"`
	// Message is a fixed text for the code; it never echoes peer input.
	Message string `json:"message"`
	// SupportedProtocols is set only with CodeUnsupportedProtocol.
	SupportedProtocols []string `json:"supported_protocols,omitempty"`
}

// Request is the request object a client sends.
type Request struct {
	// Protocol is the protocol name.
	Protocol string `json:"protocol"`
	// RequestID is chosen by the client, 1 to 64 printable ASCII characters,
	// and echoed in the response.
	RequestID string `json:"request_id"`
	// Op is "hello" or "status".
	Op string `json:"op"`
}

// Response is a response as a client reads it. The server writes the same
// shape with either Result or Error.
type Response struct {
	// Protocol is the protocol name the server answered in.
	Protocol string `json:"protocol"`
	// RequestID echoes the request's; it is empty only when the server could
	// not read a valid one, or answered a connection-level refusal.
	RequestID string `json:"request_id"`
	// OK is true when Result is set and false when Error is set.
	OK bool `json:"ok"`
	// Result is the operation's result.
	Result json.RawMessage `json:"result,omitempty"`
	// Error describes the failure.
	Error *ErrorBody `json:"error,omitempty"`
}

// Limits are the limits a server advertises.
type Limits struct {
	// MaxRequestBytes is the longest request line, without its LF.
	MaxRequestBytes int `json:"max_request_bytes"`
	// MaxResponseBytes is the longest response line, with its LF.
	MaxResponseBytes int `json:"max_response_bytes"`
	// IdleTimeoutSeconds is how long a connection may wait without a complete
	// request before the server closes it.
	IdleTimeoutSeconds int `json:"idle_timeout_seconds"`
	// MaxConnections is the connection cap.
	MaxConnections int `json:"max_connections"`
}

// HelloResult is the result of the hello operation.
type HelloResult struct {
	// Protocols are the protocol names the server speaks.
	Protocols []string `json:"protocols"`
	// Limits are the server's limits.
	Limits Limits `json:"limits"`
	// Operations are the operations offered.
	Operations []string `json:"operations"`
	// Mutations is always false in version 1: no operation changes anything.
	Mutations bool `json:"mutations"`
	// Version is the daemon's version string.
	Version string `json:"version"`
}

// DaemonStatus is the daemon block of the status result.
type DaemonStatus struct {
	// PID is the daemon's process ID.
	PID int `json:"pid"`
	// StartedAt is when the daemon started.
	StartedAt time.Time `json:"started_at"`
	// RegistryRevision is the registry's current revision.
	RegistryRevision uint64 `json:"registry_revision"`
	// BootID is the boot identity the daemon read.
	BootID string `json:"boot_id"`
	// LastReconcileCompleted tells whether the last reconcile ran to its end.
	LastReconcileCompleted bool `json:"last_reconcile_completed"`
	// LastReconcileAt is when the last reconcile ended or stopped; null when
	// none has run.
	LastReconcileAt *time.Time `json:"last_reconcile_at"`
}

// RequestedProfile is the profile a lead was asked to run with.
type RequestedProfile struct {
	// Provider is the provider name.
	Provider string `json:"provider"`
	// Version is the requested provider version.
	Version string `json:"version"`
	// Model is the requested model.
	Model string `json:"model"`
	// Effort is the requested effort.
	Effort string `json:"effort"`
	// PermissionMode is the requested permission mode.
	PermissionMode string `json:"permission_mode"`
	// AccessMode is the requested access mode.
	AccessMode string `json:"access_mode"`
}

// Observed is one observed value: either not observed, or observed with a
// value. On the wire it is {"observed":false} or {"observed":true,"value":"..."}.
type Observed struct {
	// Observed tells whether the value was seen.
	Observed bool
	// Value is the value seen; meaningful only when Observed is true.
	Value string
}

// Observe returns an observed value.
func Observe(value string) Observed { return Observed{Observed: true, Value: value} }

// MarshalJSON writes the wire form; an observed empty value still carries its
// "value" member, and an unobserved one never does.
func (o Observed) MarshalJSON() ([]byte, error) {
	if !o.Observed {
		return []byte(`{"observed":false}`), nil
	}
	v, err := json.Marshal(o.Value)
	if err != nil {
		return nil, err
	}
	return append(append([]byte(`{"observed":true,"value":`), v...), '}'), nil
}

// UnmarshalJSON reads the wire form.
func (o *Observed) UnmarshalJSON(b []byte) error {
	var w struct {
		Observed bool   `json:"observed"`
		Value    string `json:"value"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	o.Observed, o.Value = w.Observed, ""
	if w.Observed {
		o.Value = w.Value
	}
	return nil
}

// ObservedProfile is the profile the daemon saw the lead running with, each
// field observed or not.
type ObservedProfile struct {
	// Provider is the observed provider.
	Provider Observed `json:"provider"`
	// Version is the observed provider version.
	Version Observed `json:"version"`
	// Model is the observed model.
	Model Observed `json:"model"`
	// Effort is the observed effort.
	Effort Observed `json:"effort"`
	// PermissionMode is the observed permission mode.
	PermissionMode Observed `json:"permission_mode"`
	// AccessMode is the observed access mode.
	AccessMode Observed `json:"access_mode"`
}

// LaunchIntent is the lead's last journaled launch intent.
type LaunchIntent struct {
	// ID is the intent identifier.
	ID string `json:"id"`
	// Epoch is the epoch the intent opened.
	Epoch uint64 `json:"epoch"`
	// Unit is the unit name the intent named.
	Unit string `json:"unit"`
	// Outcome is the intent's outcome as the registry records it.
	Outcome string `json:"outcome"`
}

// LeadStatus is one lead in the status result.
type LeadStatus struct {
	// Role is the canonical role string, "<project>/lead" or "supervisor".
	Role string `json:"role"`
	// Kind is the role kind.
	Kind string `json:"kind"`
	// Project is the project, empty for the supervisor.
	Project string `json:"project"`
	// Manager is the manager of the lead's unit.
	Manager string `json:"manager"`
	// Epoch is the lead's current epoch.
	Epoch uint64 `json:"epoch"`
	// Desired is the desired state.
	Desired string `json:"desired"`
	// State is the shown state.
	State string `json:"state"`
	// ReasonCode is the machine-readable reason of the state.
	ReasonCode string `json:"reason_code"`
	// ReasonDetail is the reason's detail text.
	ReasonDetail string `json:"reason_detail"`
	// EvidenceKind is the kind of evidence behind the state.
	EvidenceKind string `json:"evidence_kind"`
	// Since is when the lead entered the state.
	Since time.Time `json:"since"`
	// Requested is the requested profile.
	Requested RequestedProfile `json:"requested"`
	// Observed is the observed profile.
	Observed ObservedProfile `json:"observed"`
	// LaunchIntent is the last launch intent; null when none was journaled.
	LaunchIntent *LaunchIntent `json:"launch_intent"`
}

// StatusResult is the result of the status operation.
type StatusResult struct {
	// Daemon is the daemon block.
	Daemon DaemonStatus `json:"daemon"`
	// Leads are the leads the registry holds.
	Leads []LeadStatus `json:"leads"`
}

// Handler supplies the values the server answers with. Both methods are
// read-only and may be called concurrently from several connections.
type Handler interface {
	// Hello returns the hello result. The server fills Protocols, Limits,
	// Operations and Mutations itself, as they describe the server; the
	// handler supplies Version.
	Hello(ctx context.Context) HelloResult
	// Status returns the status result. An error becomes an "internal"
	// response; its text is not sent.
	Status(ctx context.Context) (StatusResult, error)
}

// validRequestID reports whether id is 1 to 64 printable ASCII characters.
func validRequestID(id string) bool {
	if len(id) < 1 || len(id) > MaxRequestIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		if id[i] < 0x20 || id[i] > 0x7e {
			return false
		}
	}
	return true
}

// decodeRequest is the only gate to dispatch. It returns the request ID to echo
// (valid or empty), the operation (only OpHello or OpStatus) and, on failure,
// the error body to answer with. Fields are matched by exact name; unknown
// fields are ignored.
func decodeRequest(line []byte) (id, op string, fail *ErrorBody) {
	invalid := &ErrorBody{Code: CodeInvalidRequest, Message: msgInvalidRequest}
	if !utf8.Valid(line) {
		return "", "", invalid
	}
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return "", "", invalid
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil || fields == nil {
		return "", "", invalid
	}
	str := func(name string) (string, bool) {
		raw, ok := fields[name]
		if !ok || len(raw) == 0 || raw[0] != '"' {
			return "", false
		}
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return "", false
		}
		return s, true
	}
	rid, ok := str("request_id")
	if !ok || !validRequestID(rid) {
		return "", "", invalid
	}
	proto, ok := str("protocol")
	if !ok {
		return rid, "", invalid
	}
	if proto != Protocol {
		return rid, "", &ErrorBody{Code: CodeUnsupportedProtocol, Message: msgUnsupportedProtocol, SupportedProtocols: SupportedProtocols()}
	}
	op, ok = str("op")
	if !ok {
		return rid, "", invalid
	}
	if op != OpHello && op != OpStatus {
		return rid, "", &ErrorBody{Code: CodeUnsupportedOperation, Message: msgUnsupportedOperation}
	}
	return rid, op, nil
}
