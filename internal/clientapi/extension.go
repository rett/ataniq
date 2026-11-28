package clientapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"
)

// Extension is a set of operations that the caller of [Serve] adds to the two
// base operations. The server owns the wire, the peer check, the caps, the
// timeouts and the counters; an extension supplies only the operations and
// what they answer. No release build registers one.
type Extension interface {
	// Operations returns the operation names the extension serves, at least
	// one: lower-case letters, digits and dashes around exactly one dot, for
	// example "canary.start". The list is read once, before the server
	// accepts, and is fixed for the server's life.
	Operations() []string
	// Mutating tells whether any of the operations changes anything. It is
	// read once, before the server accepts. The hello result says
	// "mutations": true when any registered extension returns true.
	Mutating() bool
	// Handle serves op, one of Operations, with the request's params: nil when
	// the request carried none, otherwise one JSON object that already passed
	// the line cap and a check for a repeated top-level key. The result is
	// marshalled into the response; a non-nil fail is sent as the error
	// instead. Handle is called concurrently from several connections, in the
	// connection's goroutine, with a context that ends with the server's, and
	// a panic in it, or in the marshalling of its result, is answered
	// "internal". A fail's message must be a fixed text that echoes nothing
	// the peer sent.
	Handle(ctx context.Context, op string, params json.RawMessage) (result any, fail *ErrorBody)
}

// maxExtensionMessage is the most of a fail's message the server sends.
const maxExtensionMessage = 256

// validOperationName reports whether name is lower-case letters, digits and
// dashes, in two non-empty parts around exactly one dot.
func validOperationName(name string) bool {
	dots := 0
	partLen := 0
	for i := 0; i < len(name); i++ {
		switch c := name[i]; {
		case c == '.':
			dots++
			if partLen == 0 || dots > 1 {
				return false
			}
			partLen = 0
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-':
			partLen++
		default:
			return false
		}
	}
	return dots == 1 && partLen > 0
}

// validExtensionCode reports whether code is well-formed: 1 to 64 lower-case
// letters, digits and dashes, starting with a letter. It must not be one of
// the codes that the protocol gives a connection-level meaning (the client
// closes the connection after them), so an extension cannot make the client
// drop its connection or forge a refusal.
func validExtensionCode(code ErrorCode) bool {
	if len(code) < 1 || len(code) > MaxRequestIDLen {
		return false
	}
	for i := 0; i < len(code); i++ {
		switch c := code[i]; {
		case c >= 'a' && c <= 'z':
		case i > 0 && (c >= '0' && c <= '9' || c == '-'):
		default:
			return false
		}
	}
	switch code {
	case CodeUnauthorized, CodeBusy, CodeTooLarge, CodeUnsupportedProtocol:
		return false
	}
	return true
}

// validExtensionMessage reports whether msg is a short printable text.
func validExtensionMessage(msg string) bool {
	if len(msg) > maxExtensionMessage || !utf8.ValidString(msg) {
		return false
	}
	for _, r := range msg {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return false
		}
	}
	return true
}

// extensionSet is the validated, frozen view of the registered extensions.
type extensionSet struct {
	ops       []string             // every extension operation, in order
	byOp      map[string]Extension // operation to its extension
	mutations bool
}

// newExtensionSet validates the extensions and reads their operations and
// mutation flags once. It returns an error for a nil extension, one with no
// operations, an operation name that is not well-formed, or one that repeats
// another extension's. A name that is well-formed has a dot, so it cannot be
// "hello" or "status". A panic in Operations or Mutating is an error.
func newExtensionSet(exts []Extension) (set *extensionSet, err error) {
	defer func() {
		if recover() != nil {
			set, err = nil, errors.New("clientapi: an extension panicked while it was read")
		}
	}()
	set = &extensionSet{byOp: map[string]Extension{}}
	for i, ext := range exts {
		if ext == nil {
			return nil, fmt.Errorf("clientapi: extension %d is nil", i)
		}
		ops := append([]string(nil), ext.Operations()...)
		if len(ops) == 0 {
			// Hello would otherwise say "mutations" for an extension that
			// offers nothing to mutate.
			return nil, fmt.Errorf("clientapi: extension %d has no operations", i)
		}
		for _, op := range ops {
			if !validOperationName(op) {
				return nil, fmt.Errorf("clientapi: extension %d has an operation name that is not well-formed", i)
			}
			if _, dup := set.byOp[op]; dup {
				return nil, fmt.Errorf("clientapi: extension %d repeats operation %q", i, op)
			}
			set.byOp[op] = ext
			set.ops = append(set.ops, op)
		}
		if ext.Mutating() {
			set.mutations = true
		}
	}
	return set, nil
}

// has reports whether op is an extension operation. A nil set has none.
func (s *extensionSet) has(op string) bool {
	if s == nil {
		return false
	}
	_, ok := s.byOp[op]
	return ok
}

// operations returns the operations hello lists: the base ones, then the
// extensions', in order.
func (s *extensionSet) operations() []string {
	ops := Operations()
	if s != nil {
		ops = append(ops, s.ops...)
	}
	return ops
}

// mutating tells whether hello says "mutations": true.
func (s *extensionSet) mutating() bool { return s != nil && s.mutations }

// Principal identifies a verified managed client in its issuer's namespace.
// Both fields must be set. A principal is supplied by the server's trusted
// connection verifier, never by the request or by the same-UID check.
type Principal struct {
	Issuer   string
	ClientID string
}

type principalKey struct{}

// PrincipalFromContext returns the principal bound to a managed handler call.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// ManagedHandler serves only protocol v2. Its methods may run concurrently.
// Capabilities returns a stable snapshot for the principal in ctx at dispatch
// and again at delivery. Managed and extension operations follow the same
// naming rules.
// HandleManaged receives only a target and operation from that snapshot.
// It must recheck current authorization when committing an effect. Its result
// and error follow the same wire rules as Extension.Handle.
type ManagedHandler interface {
	Capabilities(ctx context.Context) (TargetCapabilities, error)
	HandleManaged(ctx context.Context, target, op string, params json.RawMessage) (any, *ErrorBody)
}
