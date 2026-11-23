package seatnotice

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strings"
	"unicode/utf8"
)

// The operations.
const (
	OpInfo        = "info"
	OpNotify      = "notify"
	OpSubscribe   = "subscribe"
	OpUnsubscribe = "unsubscribe"
)

// The codes the notifier answers with. A code is [a-z0-9_]{1,40}; the client
// takes any code of that grammar, these are the ones the notifier uses.
const (
	CodeBadSubscription = "bad_subscription"
	CodeTooMany         = "too_many"
	CodeEndpointRefused = "endpoint_refused"
	CodeUnavailable     = "unavailable"
)

// The bounds of the wire's values.
const (
	// MaxN is the largest count a notify carries.
	MaxN = 999
	// MaxEndpoint is the longest endpoint, in bytes.
	MaxEndpoint = 2048
	// MaxCount bounds the counts of a response: subscriptions and sent.
	MaxCount = 999
	// P256DHLen and AuthLen are the decoded lengths of a subscription's keys.
	P256DHLen = 65
	AuthLen   = 16
)

// Subscription is a browser's push subscription: the endpoint of its push
// service and the two keys messages to it are encrypted with. It is what the
// seat's subscribe page relays; the notifier is the only part that keeps it.
type Subscription struct {
	Endpoint string `json:"endpoint"`
	P256DH   string `json:"p256dh"`
	Auth     string `json:"auth"`
}

// Info is the answer to the info op: the VAPID public key the browser needs
// to subscribe (base64url of the 65-byte point), and how many subscriptions
// the notifier holds.
type Info struct {
	VAPIDPublic   string
	Subscriptions int
}

// CodedError is an answer {"ok":false,"code":CODE}, and what a [Handler]
// returns to give one. Any other error a handler returns is answered with
// [CodeUnavailable].
type CodedError struct{ Code string }

// NewCodedError returns the error for code, or [CodeUnavailable]'s when code
// is not of the grammar.
func NewCodedError(code string) *CodedError {
	if !ValidCode(code) {
		code = CodeUnavailable
	}
	return &CodedError{Code: code}
}

func (e *CodedError) Error() string { return "seatnotice: refused: " + e.Code }

// ValidCode reports whether s is a code: [a-z0-9_]{1,40}.
func ValidCode(s string) bool {
	if len(s) == 0 || len(s) > 40 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

// ValidEndpointHash reports whether s is 64 lower-case hex digits.
func ValidEndpointHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// EndpointSHA256 returns the hash a subscription is named by in an
// unsubscribe: the SHA-256 of the endpoint's bytes, in lower-case hex.
func EndpointSHA256(endpoint string) string {
	sum := sha256.Sum256([]byte(endpoint))
	return hex.EncodeToString(sum[:])
}

var errBadSubscription = errors.New("seatnotice: not a valid subscription")

// ValidateSubscription checks s against the bounds of the wire: an endpoint
// that is an https URL of at most [MaxEndpoint] bytes, with a host, no user
// information, no fragment, and only visible ASCII; a p256dh that is the
// unpadded base64url of 65 bytes, the first 0x04 (an uncompressed point); an
// auth that is the unpadded base64url of 16 bytes. The error holds nothing of
// the subscription.
func ValidateSubscription(s Subscription) error {
	if !validEndpoint(s.Endpoint) {
		return errBadSubscription
	}
	p, err := decodeB64(s.P256DH, P256DHLen)
	if err != nil || p[0] != 0x04 {
		return errBadSubscription
	}
	if _, err := decodeB64(s.Auth, AuthLen); err != nil {
		return errBadSubscription
	}
	return nil
}

func validEndpoint(e string) bool {
	if len(e) == 0 || len(e) > MaxEndpoint || !strings.HasPrefix(e, "https://") {
		return false
	}
	for i := 0; i < len(e); i++ {
		if e[i] <= 0x20 || e[i] >= 0x7f {
			return false
		}
	}
	u, err := url.Parse(e)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	return !strings.Contains(e, "#")
}

// decodeB64 decodes s, the unpadded base64url of exactly n bytes, with its
// last digit canonical.
func decodeB64(s string, n int) ([]byte, error) {
	if len(s) != base64.RawURLEncoding.EncodedLen(n) {
		return nil, errBadSubscription
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(s)
	if err != nil || len(b) != n {
		return nil, errBadSubscription
	}
	return b, nil
}

// ValidVAPIDPublic reports whether s is the unpadded base64url of an
// uncompressed P-256 point's 65 bytes.
func ValidVAPIDPublic(s string) bool {
	b, err := decodeB64(s, P256DHLen)
	return err == nil && b[0] == 0x04
}

// request is a decoded request: Op says which of the other fields is set.
type request struct {
	Op             string
	N              int
	Sub            Subscription
	EndpointSHA256 string
}

// response is a decoded response. When OK is false only Code is set.
type response struct {
	OK   bool
	Code string
	Info Info
	Sent int
}

// checkObject holds data to the strict form of a frame: valid UTF-8 and
// valid JSON, one object with no byte before or after it, nested at most
// [maxDepth] levels, with no key repeated at any depth.
func checkObject(data []byte) error {
	if len(data) < 2 || data[0] != '{' || data[len(data)-1] != '}' {
		return violation("not a JSON object")
	}
	if !utf8.Valid(data) || !json.Valid(data) {
		return violation("not valid JSON")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := walk(dec, 1); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return violation("bytes after the object")
	}
	return nil
}

func walk(dec *json.Decoder, depth int) error {
	tok, err := dec.Token()
	if err != nil {
		return violation("not valid JSON")
	}
	d, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	if depth > maxDepth {
		return violation("nested too deeply")
	}
	switch d {
	case '{':
		seen := map[string]struct{}{}
		for dec.More() {
			k, err := dec.Token()
			if err != nil {
				return violation("not valid JSON")
			}
			key, _ := k.(string)
			if _, dup := seen[key]; dup {
				return violation("repeated key")
			}
			seen[key] = struct{}{}
			if err := walk(dec, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for dec.More() {
			if err := walk(dec, depth+1); err != nil {
				return err
			}
		}
	}
	if _, err := dec.Token(); err != nil {
		return violation("not valid JSON")
	}
	return nil
}

// fieldSpec names the keys an object may have; the value says it is required.
type fieldSpec map[string]bool

// splitObject checks data and returns the raw value of each key. A key not
// in spec is refused whatever its case (the match is exact, unlike the
// standard decoder's), and a required key that is absent is refused.
func splitObject(data []byte, spec fieldSpec) (map[string]json.RawMessage, error) {
	if err := checkObject(data); err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, violation("not valid JSON")
	}
	for k := range m {
		if _, ok := spec[k]; !ok {
			return nil, violation("unknown field")
		}
	}
	for k, required := range spec {
		if _, ok := m[k]; required && !ok {
			return nil, violation("missing field")
		}
	}
	return m, nil
}

func rawString(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || raw[0] != '"' {
		return "", violation("not a string")
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", violation("not a string")
	}
	return s, nil
}

// rawCount reads an integer 0 to max written as plain digits: no sign, no
// leading zero, no fraction and no exponent.
func rawCount(raw json.RawMessage, max int) (int, error) {
	if len(raw) == 0 || len(raw) > 3 || (raw[0] == '0' && len(raw) > 1) {
		return 0, violation("not a count")
	}
	n := 0
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0, violation("not a count")
		}
		n = n*10 + int(c-'0')
	}
	if n > max {
		return 0, violation("count out of range")
	}
	return n, nil
}

func rawBool(raw json.RawMessage) (bool, error) {
	switch string(raw) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	return false, violation("not a boolean")
}

var subSpec = fieldSpec{"endpoint": true, "p256dh": true, "auth": true}

// decodeRequest decodes and checks one request.
func decodeRequest(data []byte) (request, error) {
	head, err := splitObject(data, fieldSpec{"op": true, "n": false, "sub": false, "endpoint_sha256": false})
	if err != nil {
		return request{}, err
	}
	op, err := rawString(head["op"])
	if err != nil {
		return request{}, err
	}
	var want fieldSpec
	switch op {
	case OpInfo:
		want = fieldSpec{"op": true}
	case OpNotify:
		want = fieldSpec{"op": true, "n": true}
	case OpSubscribe:
		want = fieldSpec{"op": true, "sub": true}
	case OpUnsubscribe:
		want = fieldSpec{"op": true, "endpoint_sha256": true}
	default:
		return request{}, violation("unknown op")
	}
	for k := range head {
		if _, ok := want[k]; !ok {
			return request{}, violation("field the op does not have")
		}
	}
	for k, required := range want {
		if _, ok := head[k]; required && !ok {
			return request{}, violation("missing field")
		}
	}
	r := request{Op: op}
	switch op {
	case OpNotify:
		if r.N, err = rawCount(head["n"], MaxN); err != nil {
			return request{}, err
		}
	case OpSubscribe:
		sub, err := splitObject(head["sub"], subSpec)
		if err != nil {
			return request{}, err
		}
		if r.Sub.Endpoint, err = rawString(sub["endpoint"]); err != nil {
			return request{}, err
		}
		if r.Sub.P256DH, err = rawString(sub["p256dh"]); err != nil {
			return request{}, err
		}
		if r.Sub.Auth, err = rawString(sub["auth"]); err != nil {
			return request{}, err
		}
		// The bounds are the wire's: a subscription out of them is a
		// malformed frame, not a refusal the notifier answers.
		if err := ValidateSubscription(r.Sub); err != nil {
			return request{}, violation("subscription out of bounds")
		}
	case OpUnsubscribe:
		if r.EndpointSHA256, err = rawString(head["endpoint_sha256"]); err != nil {
			return request{}, err
		}
		if !ValidEndpointHash(r.EndpointSHA256) {
			return request{}, violation("not an endpoint hash")
		}
	}
	return r, nil
}

// encode returns the request's frame payload.
func (r request) encode() ([]byte, error) {
	switch r.Op {
	case OpInfo:
		return json.Marshal(struct {
			Op string `json:"op"`
		}{r.Op})
	case OpNotify:
		if r.N < 0 || r.N > MaxN {
			return nil, violation("count out of range")
		}
		return json.Marshal(struct {
			Op string `json:"op"`
			N  int    `json:"n"`
		}{r.Op, r.N})
	case OpSubscribe:
		if err := ValidateSubscription(r.Sub); err != nil {
			return nil, violation("subscription out of bounds")
		}
		return json.Marshal(struct {
			Op  string       `json:"op"`
			Sub Subscription `json:"sub"`
		}{r.Op, r.Sub})
	case OpUnsubscribe:
		if !ValidEndpointHash(r.EndpointSHA256) {
			return nil, violation("not an endpoint hash")
		}
		return json.Marshal(struct {
			Op             string `json:"op"`
			EndpointSHA256 string `json:"endpoint_sha256"`
		}{r.Op, r.EndpointSHA256})
	}
	return nil, violation("unknown op")
}

// decodeResponse decodes and checks the response to a request of op.
func decodeResponse(op string, data []byte) (response, error) {
	head, err := splitObject(data, fieldSpec{"ok": true, "code": false, "vapid_public": false, "subscriptions": false, "sent": false})
	if err != nil {
		return response{}, err
	}
	ok, err := rawBool(head["ok"])
	if err != nil {
		return response{}, err
	}
	if !ok {
		if len(head) != 2 || head["code"] == nil {
			return response{}, violation("a refusal has an ok and a code only")
		}
		code, err := rawString(head["code"])
		if err != nil {
			return response{}, err
		}
		if !ValidCode(code) {
			return response{}, violation("not a code")
		}
		return response{Code: code}, nil
	}
	var want fieldSpec
	switch op {
	case OpInfo:
		want = fieldSpec{"ok": true, "vapid_public": true, "subscriptions": true}
	case OpNotify:
		want = fieldSpec{"ok": true, "sent": true}
	case OpSubscribe, OpUnsubscribe:
		want = fieldSpec{"ok": true}
	default:
		return response{}, violation("unknown op")
	}
	for k := range head {
		if _, ok := want[k]; !ok {
			return response{}, violation("field the op does not have")
		}
	}
	for k := range want {
		if _, ok := head[k]; !ok {
			return response{}, violation("missing field")
		}
	}
	r := response{OK: true}
	switch op {
	case OpInfo:
		if r.Info.VAPIDPublic, err = rawString(head["vapid_public"]); err != nil {
			return response{}, err
		}
		if !ValidVAPIDPublic(r.Info.VAPIDPublic) {
			return response{}, violation("not a public key")
		}
		if r.Info.Subscriptions, err = rawCount(head["subscriptions"], MaxCount); err != nil {
			return response{}, err
		}
	case OpNotify:
		if r.Sent, err = rawCount(head["sent"], MaxCount); err != nil {
			return response{}, err
		}
	}
	return r, nil
}

// encodeResponse returns the frame payload that answers a request of op: the
// refusal for code, when code is not empty, else the success of that op.
func encodeResponse(op, code string, info Info, sent int) ([]byte, error) {
	if code != "" {
		if !ValidCode(code) {
			code = CodeUnavailable
		}
		return json.Marshal(struct {
			OK   bool   `json:"ok"`
			Code string `json:"code"`
		}{false, code})
	}
	switch op {
	case OpInfo:
		if !ValidVAPIDPublic(info.VAPIDPublic) || info.Subscriptions < 0 || info.Subscriptions > MaxCount {
			return nil, violation("info out of bounds")
		}
		return json.Marshal(struct {
			OK            bool   `json:"ok"`
			VAPIDPublic   string `json:"vapid_public"`
			Subscriptions int    `json:"subscriptions"`
		}{true, info.VAPIDPublic, info.Subscriptions})
	case OpNotify:
		if sent < 0 || sent > MaxCount {
			return nil, violation("sent out of bounds")
		}
		return json.Marshal(struct {
			OK   bool `json:"ok"`
			Sent int  `json:"sent"`
		}{true, sent})
	case OpSubscribe, OpUnsubscribe:
		return json.Marshal(struct {
			OK bool `json:"ok"`
		}{true})
	}
	return nil, violation("unknown op")
}
