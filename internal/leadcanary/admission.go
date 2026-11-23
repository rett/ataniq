//go:build linux && ataniq_canary

package leadcanary

import (
	"ataniq/internal/inject/command"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/leadrole"
)

// This file is the only file of the package that imports from the injection
// tree, and what it imports is the command package's header and rules, nothing
// below it. A test holds both.

const (
	// MaxBodyBytes is the cap on the body of the canary message, in bytes.
	MaxBodyBytes = 2048
	// MessageKind is the kind in the header of the canary message.
	MessageKind = "canary"
)

// AdmitCode is the fixed reason code of a refused message. It is also what the
// evidence's "code" field and the daemon's "refused" answer carry.
type AdmitCode string

const (
	// CodeBadID: the message id does not follow the wire's grammar
	// ([a-z0-9][a-z0-9._-]{0,63}).
	CodeBadID AdmitCode = "bad-id"
	// CodeBadTarget: the header's names or the edge are refused by the
	// command package (the project is not a valid name, or the daemon may not
	// send to this target). It cannot happen for a valid canary role.
	CodeBadTarget AdmitCode = "bad-target"
	// CodeBodyTooLong: the body is over MaxBodyBytes.
	CodeBodyTooLong AdmitCode = "body-too-long"
	// CodeInvalidUTF8, CodeBadCharacter, CodeEmptyBody and CodeForgedHeader are
	// the body rules of the command package (CheckBody), with its spellings.
	CodeInvalidUTF8  AdmitCode = "invalid-utf8"
	CodeBadCharacter AdmitCode = "bad-character"
	CodeEmptyBody    AdmitCode = "empty-body"
	CodeForgedHeader AdmitCode = "forged-header"
	// CodeBadBody: the body is refused by a rule of the command package that
	// this list does not name.
	CodeBadBody AdmitCode = "bad-body"
	// CodeBadText: the whole text is refused by the wire's rule for the text of
	// an inject request (length or characters).
	CodeBadText AdmitCode = "bad-text"
)

// AdmitError is the error of a refused message. Its text is the code and a
// fixed sentence; it never repeats the id or the body.
type AdmitError struct {
	Code AdmitCode
}

// Error returns "leadcanary: message refused (<code>)".
func (e *AdmitError) Error() string { return "leadcanary: message refused (" + string(e.Code) + ")" }

// Admission is the admission of the one message of a canary run. It builds the
// text of a message from the daemon to the lead of the canary project, which
// the daemon sends to the holder.
type Admission struct {
	project string
	target  command.Target
}

// NewAdmission returns the admission for the role, which must be the lead of a
// throwaway project ([Names] refuses any other). The target of the message is
// that lead. It is [NewAdmissionFor] for leadrole.ReservedThrowaway.
func NewAdmission(role leadrole.Role) (*Admission, error) {
	return NewAdmissionFor(leadrole.ReservedThrowaway, role)
}

// NewAdmissionFor is [NewAdmission] for the lead of a project that carries the
// reserved prefix of kind k ([NamesFor] refuses any other). The text of the
// message is the same for both kinds.
func NewAdmissionFor(k leadrole.Reserved, role leadrole.Role) (*Admission, error) {
	if _, _, err := NamesFor(k, role); err != nil {
		return nil, err
	}
	target := command.LeadTarget(role.Project())
	if !target.Valid() || !command.EdgeAllowed(command.Daemon(), target) {
		return nil, &AdmitError{Code: CodeBadTarget}
	}
	return &Admission{project: role.Project(), target: target}, nil
}

// Admit checks a message and returns the text to send. The checks, in order:
//
//   - the message id follows the wire's grammar (wire.CheckMessageID), which is
//     also the command package's name grammar;
//   - the header's names and the edge from the daemon to the lead are valid
//     (the command package's ValidName and EdgeAllowed);
//   - the body is at most MaxBodyBytes (2048) bytes;
//   - the body passes the command package's CheckBody (valid UTF-8, printable
//     or a newline, some text, no header marker);
//   - the whole text passes the wire's rule for an inject request
//     (wire.CheckInjectText).
//
// The text is the command package's header, a newline and the body:
//
//	[ataniq-msg id=<id> from=ataniq kind=canary]
//	<body>
//
// A refusal is an *[AdmitError] with a fixed code. The id and the body are
// never in the error. A caller that writes a refusal to the evidence must not
// pass an id that failed CodeBadID: the evidence refuses an id that is not
// well formed.
func (a *Admission) Admit(messageID, body string) (text string, err error) {
	if wire.CheckMessageID(messageID) != nil {
		return "", &AdmitError{Code: CodeBadID}
	}
	if !command.ValidName(messageID) || !command.ValidName(MessageKind) ||
		!a.target.Valid() || !command.EdgeAllowed(command.Daemon(), a.target) {
		return "", &AdmitError{Code: CodeBadTarget}
	}
	if len(body) > MaxBodyBytes {
		return "", &AdmitError{Code: CodeBodyTooLong}
	}
	if reason, _ := command.CheckBody(body); reason != "" {
		return "", &AdmitError{Code: bodyCode(reason)}
	}
	text = command.RenderText(messageID, command.Daemon(), MessageKind, body)
	if wire.CheckInjectText(text) != nil {
		return "", &AdmitError{Code: CodeBadText}
	}
	return text, nil
}

func bodyCode(r command.Reason) AdmitCode {
	switch r {
	case command.ReasonInvalidUTF8:
		return CodeInvalidUTF8
	case command.ReasonBadCharacter:
		return CodeBadCharacter
	case command.ReasonEmptyBody:
		return CodeEmptyBody
	case command.ReasonForgedHeader:
		return CodeForgedHeader
	}
	return CodeBadBody
}
