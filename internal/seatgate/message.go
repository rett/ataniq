package seatgate

import (
	"encoding/json"
	"strconv"
	"strings"
)

// The reserved operations of the transport. Every other operation belongs to
// the handler. The namespace "seat." is the transport's and the handler's
// both: the transport serves exactly these two and passes any other name to
// the handler, which owns its meaning (seat.challenge, seat.pair and the like).
const (
	OpHello = "seat.hello"
	OpPing  = "seat.ping"
)

// ProtoMajor is the version of the protocol this package speaks. A hello with
// another number is refused with [CodeProtoUnsupported].
const ProtoMajor = 1

// The codes the transport answers with. A code is of the grammar
// [a-z0-9-]{1,40} (see [ValidCode]) and is never a text for a person: a
// handler's own codes follow the same grammar.
const (
	CodeFailed           = "failed"            // any handler error that is not coded, and a handler that panicked
	CodeProtoUnsupported = "proto-unsupported" // a hello for another major version
	CodeBadHello         = "bad-hello"         // a hello that is not as specified
	CodeHelloRequired    = "hello-required"    // an operation before the hello; the connection is closed
	CodeHelloRepeated    = "hello-repeated"    // a second hello
	CodeRateLimited      = "rate-limited"      // more requests than the connection's rate
	CodeBusy             = "busy"              // more requests in flight than the limit
	CodeDeadline         = "deadline-exceeded" // the handler did not answer within the request deadline
	CodeTooLarge         = "too-large"         // a result that does not fit a frame
	CodeShuttingDown     = "shutting-down"     // the server is stopping
)

// ValidCode reports whether s is a code: 1 to 40 of a-z, 0-9 and hyphen.
func ValidCode(s string) bool {
	if len(s) < 1 || len(s) > 40 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// ValidOp reports whether s is an operation name: ^[a-z]+\.[a-z0-9-]{1,40}$,
// that is a namespace of letters, a period and a name of 1 to 40 of a-z, 0-9 and
// hyphen. The first period ends the namespace.
func ValidOp(s string) bool {
	ns, name, ok := strings.Cut(s, ".")
	if !ok || len(ns) == 0 || len(s) > 64 {
		return false
	}
	for i := 0; i < len(ns); i++ {
		if ns[i] < 'a' || ns[i] > 'z' {
			return false
		}
	}
	return ValidCode(name)
}

// validCap reports whether s can be a capability name: 1 to 40 of a-z, 0-9,
// period and hyphen.
func validCap(s string) bool {
	if len(s) < 1 || len(s) > 40 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '.' {
			return false
		}
	}
	return true
}

// maxCaps is the most capabilities a hello may list.
const maxCaps = 32

// checkCaps holds a capability list to its grammar, its size and uniqueness.
func checkCaps(caps []string) bool {
	if len(caps) > maxCaps {
		return false
	}
	seen := map[string]bool{}
	for _, c := range caps {
		if !validCap(c) || seen[c] {
			return false
		}
		seen[c] = true
	}
	return true
}

// request is a request frame: {"id": n, "op": "...", "params": {...}}, the
// params absent when the operation has none. The id is 1 or more and chosen by
// the client; the server answers it once.
type request struct {
	ID     uint64
	Op     string
	Params json.RawMessage
}

// response is a response frame: {"id": n, "result": {...}} or
// {"id": n, "error": {"code": "..."}}. Code is set for the second form.
type response struct {
	ID     uint64
	Result json.RawMessage
	Code   string
}

var (
	requestSpec  = fieldSpec{"id": true, "op": true, "params": false}
	responseSpec = fieldSpec{"id": true, "result": false, "error": false}
	errorSpec    = fieldSpec{"code": true}
	helloSpec    = fieldSpec{"proto": true, "caps": true, "role": true}
	helloOkSpec  = fieldSpec{"proto": true, "caps": true}
)

// isObject reports whether raw is a JSON object: it begins with a brace. (The
// whole frame has been checked for being well formed by then.)
func isObject(raw json.RawMessage) bool { return len(raw) > 0 && raw[0] == '{' }

// isArray reports whether raw is a JSON array (and so not null).
func isArray(raw json.RawMessage) bool { return len(raw) > 0 && raw[0] == '[' }

func decodeID(raw json.RawMessage) (uint64, error) {
	var id uint64
	if err := json.Unmarshal(raw, &id); err != nil || id == 0 {
		return 0, violation("bad id")
	}
	return id, nil
}

// decodeRequest reads a request frame's payload under the strict rules.
func decodeRequest(p []byte) (request, error) {
	m, err := splitObject(p, requestSpec)
	if err != nil {
		return request{}, err
	}
	var r request
	if r.ID, err = decodeID(m["id"]); err != nil {
		return request{}, err
	}
	if err := json.Unmarshal(m["op"], &r.Op); err != nil || !ValidOp(r.Op) {
		return request{}, violation("bad operation name")
	}
	if params, ok := m["params"]; ok {
		if !isObject(params) {
			return request{}, violation("params is not an object")
		}
		r.Params = params
	}
	return r, nil
}

// decodeResponse reads a response frame's payload under the strict rules.
func decodeResponse(p []byte) (response, error) {
	m, err := splitObject(p, responseSpec)
	if err != nil {
		return response{}, err
	}
	var r response
	if r.ID, err = decodeID(m["id"]); err != nil {
		return response{}, err
	}
	result, hasResult := m["result"]
	body, hasError := m["error"]
	if hasResult == hasError {
		return response{}, violation("a response has a result or an error, and not both")
	}
	if hasResult {
		if !isObject(result) {
			return response{}, violation("result is not an object")
		}
		r.Result = result
		return r, nil
	}
	if !isObject(body) {
		return response{}, violation("error is not an object")
	}
	e, err := splitObject(body, errorSpec)
	if err != nil {
		return response{}, err
	}
	if err := json.Unmarshal(e["code"], &r.Code); err != nil || !ValidCode(r.Code) {
		return response{}, violation("bad error code")
	}
	return r, nil
}

// encode writes the request as a frame payload. The params, if any, must be
// an object. The params are written byte for byte as given (the encoding/json
// package would re-escape them), the other fields being of a grammar that
// needs no escape.
func (r request) encode() ([]byte, error) {
	if r.ID == 0 || !ValidOp(r.Op) || (len(r.Params) > 0 && (!isObject(r.Params) || !json.Valid(r.Params))) {
		return nil, violation("bad request")
	}
	out := append([]byte(`{"id":`), strconv.FormatUint(r.ID, 10)...)
	out = append(out, `,"op":"`...)
	out = append(out, r.Op...)
	out = append(out, '"')
	if len(r.Params) > 0 {
		out = append(out, `,"params":`...)
		out = append(out, r.Params...)
	}
	return append(out, '}'), nil
}

// encode writes the response as a frame payload: an error if Code is set, a
// result otherwise ({} when there is none). A result is written byte for byte.
func (r response) encode() ([]byte, error) {
	if r.ID == 0 {
		return nil, violation("bad response")
	}
	out := append([]byte(`{"id":`), strconv.FormatUint(r.ID, 10)...)
	if r.Code != "" {
		if !ValidCode(r.Code) {
			return nil, violation("bad error code")
		}
		out = append(out, `,"error":{"code":"`...)
		out = append(out, r.Code...)
		return append(out, `"}}`...), nil
	}
	res := r.Result
	if len(res) == 0 {
		res = json.RawMessage("{}")
	}
	if !isObject(res) || !json.Valid(res) {
		return nil, violation("result is not an object")
	}
	out = append(out, `,"result":`...)
	out = append(out, res...)
	return append(out, '}'), nil
}

// hello is the params of seat.hello and, without Role, the result.
type hello struct {
	Proto int      `json:"proto"`
	Caps  []string `json:"caps"`
	Role  string   `json:"role,omitempty"`
}

// errBadHello is the refusal of a hello that is not as specified; the proto
// field alone being another number is [errProto].
var (
	errBadHello = violation("bad hello")
	errProto    = violation("unsupported protocol version")
)

// decodeHello reads the params of a seat.hello: proto, caps and role "seat".
func decodeHello(params json.RawMessage) (hello, error) {
	if len(params) == 0 {
		return hello{}, errBadHello
	}
	m, err := splitObject(params, helloSpec)
	if err != nil {
		return hello{}, errBadHello
	}
	var h hello
	if json.Unmarshal(m["proto"], &h.Proto) != nil || h.Proto < 1 {
		return hello{}, errBadHello
	}
	if json.Unmarshal(m["role"], &h.Role) != nil || h.Role != "seat" {
		return hello{}, errBadHello
	}
	if !isArray(m["caps"]) || json.Unmarshal(m["caps"], &h.Caps) != nil || !checkCaps(h.Caps) {
		return hello{}, errBadHello
	}
	if h.Proto != ProtoMajor {
		return hello{}, errProto
	}
	return h, nil
}

// decodeHelloResult reads the result of a seat.hello: proto and caps.
func decodeHelloResult(result json.RawMessage) (hello, error) {
	m, err := splitObject(result, helloOkSpec)
	if err != nil {
		return hello{}, errBadHello
	}
	var h hello
	if json.Unmarshal(m["proto"], &h.Proto) != nil || h.Proto != ProtoMajor {
		return hello{}, errProto
	}
	if !isArray(m["caps"]) || json.Unmarshal(m["caps"], &h.Caps) != nil || !checkCaps(h.Caps) {
		return hello{}, errBadHello
	}
	return h, nil
}

func (h hello) encode() json.RawMessage {
	if h.Caps == nil {
		h.Caps = []string{}
	}
	b, _ := json.Marshal(h)
	return b
}
