package qualification

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
)

func pointer[T any](v T) *T { return &v }
func request(c Case) Request {
	return Request{Version: Version1, RunID: "0123456789abcdef", RequestID: "fedcba9876543210", Case: c, MaxDurationMS: 100, MaxBytes: 4096}
}
func wire(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRequestCodec(t *testing.T) {
	r := request(CaseInventory)
	good := wire(t, r)
	if got, err := DecodeRequest(strings.NewReader(good)); err != nil || got.RunID != r.RunID {
		t.Fatalf("roundtrip: %+v %v", got, err)
	}
	cases := map[string]string{
		"duplicate":             strings.Replace(good, `"version":1`, `"version":1,"version":1`, 1),
		"ASCII alias":           strings.Replace(good, `"version":1`, `"version":1,"Version":1`, 1),
		"Unicode s alias":       strings.Replace(good, `"request_id"`, `"requeſt_id"`, 1),
		"escaped Unicode":       strings.Replace(good, `"request_id"`, `"reque\u017ft_id"`, 1),
		"unknown":               strings.Replace(good, `"version":1`, `"version":1,"extra":1`, 1),
		"trailing":              good + ` {}`,
		"junk":                  good + `no`,
		"null":                  strings.Replace(good, `"version":1`, `"version":null`, 1),
		"missing version":       strings.Replace(good, `"version":1,`, "", 1),
		"zero duration":         strings.Replace(good, `"max_duration_ms":100`, `"max_duration_ms":0`, 1),
		"missing bytes":         strings.Replace(good, `,"max_bytes":4096`, "", 1),
		"bad id":                strings.Replace(good, r.RunID, "nothex00", 1),
		"array":                 `[]`,
		"empty":                 "",
		"depth":                 `{"a":` + strings.Repeat(`{"a":`, 9) + `1` + strings.Repeat(`}`, 10),
		"valid prefix oversize": good + strings.Repeat(" ", MaxInputBytes),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeRequest(strings.NewReader(input)); err == nil {
				t.Fatal("accepted invalid request")
			}
		})
	}
	if _, err := DecodeRequest(strings.NewReader(good + strings.Repeat(" ", MaxInputBytes-len(good)))); err != nil {
		t.Fatal("exact cap:", err)
	}
	bad := []byte(good)
	bad = append(bad, 0xff)
	if _, err := DecodeRequest(bytes.NewReader(bad)); err == nil {
		t.Fatal("invalid UTF8 accepted")
	}
}

func TestCanonicalKeyAndDepthWalk(t *testing.T) {
	// Test the structural guard directly so unknown-field rejection cannot mask
	// a missing canonical-key or nesting check.
	for _, key := range []string{"State", "ſtate", `\u017ftate`, "Key"} {
		err := walk(json.NewDecoder(strings.NewReader(`{"`+key+`":1}`)), 1)
		if err == nil || err.Error() != "noncanonical key" {
			t.Fatal(key, err)
		}
	}
	input := strings.Repeat(`{"a":`, maxDepth) + `1` + strings.Repeat(`}`, maxDepth)
	err := walk(json.NewDecoder(strings.NewReader(input)), 1)
	if err == nil || err.Error() != "nesting too deep" {
		t.Fatal(err)
	}
}

func TestObservationDecoderRejectsInvalidWire(t *testing.T) {
	o := Observation{Version: Version1, RunID: "01234567", RequestID: "89abcdef", Case: CaseFakeUnix, Status: "observed", Facts: []Fact{{Name: "connected", State: "known", Value: pointer("true")}}}
	good := wire(t, o)
	for name, input := range map[string]string{
		"duplicate":        strings.Replace(good, `"version":1`, `"version":1,"version":1`, 1),
		"ASCII alias":      strings.Replace(good, `"status"`, `"Status"`, 1),
		"Unicode alias":    strings.Replace(good, `"status"`, `"\u017ftatus"`, 1),
		"nested alias":     strings.Replace(good, `"state"`, `"ſtate"`, 1),
		"nested duplicate": strings.Replace(good, `"state":"known"`, `"state":"known","state":"known"`, 1),
		"unknown":          strings.Replace(good, `"version":1`, `"version":1,"extra":1`, 1),
		"facts null":       strings.Replace(good, `[{"name":"connected","state":"known","value":"true"}]`, `null`, 1),
		"value null":       strings.Replace(good, `"value":"true"`, `"value":null`, 1),
		"value missing":    strings.Replace(good, `,"value":"true"`, ``, 1),
		"trailing":         good + ` {}`,
		"oversized prefix": good + strings.Repeat(" ", MaxOutputBytes),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeObservation(strings.NewReader(input)); err == nil {
				t.Fatal("invalid observation accepted")
			}
		})
	}
}

func TestPayloadAndFDValidation(t *testing.T) {
	for _, name := range []string{"x", "fixture-1.txt"} {
		r := request(CaseFakeFile)
		r.Payload = &Payload{File: &NamePayload{Name: name}}
		if _, err := DecodeRequest(strings.NewReader(wire(t, r))); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"", ".x", "..", "a..b", "a/b", "a\\b", "/x", "é", strings.Repeat("a", 65)} {
		r := request(CaseFakeFile)
		r.Payload = &Payload{File: &NamePayload{Name: name}}
		if r.Validate() == nil {
			t.Fatalf("bad flat name accepted: %q", name)
		}
	}
	r := request(CaseFakeFD)
	r.Payload = &Payload{FD: &FDPayload{Name: "canary", FD: pointer(512), Device: pointer(uint64(0)), Inode: pointer(uint64(0)), Regular: pointer(true), SHA256: strings.Repeat("a", 64)}}
	if r.Validate() != nil {
		t.Fatal("explicit zero identity rejected")
	}
	for _, fd := range []int{-1, 0, 1, 2, MaxFakeFD + 1} {
		r.Payload.FD.FD = pointer(fd)
		if r.Validate() == nil {
			t.Fatalf("FD %d accepted", fd)
		}
	}
	r.Payload.FD.FD = pointer(MaxFakeFD)
	if r.Validate() != nil {
		t.Fatal("upper bound rejected")
	}
	for _, key := range []string{"fd", "device", "inode", "regular", "sha256"} {
		var obj map[string]any
		if err := json.Unmarshal([]byte(wire(t, r)), &obj); err != nil {
			t.Fatal(err)
		}
		delete(obj["payload"].(map[string]any)["fd"].(map[string]any), key)
		if _, err := DecodeRequest(strings.NewReader(wire(t, obj))); err == nil {
			t.Fatalf("missing %s accepted", key)
		}
	}
	r.Payload.FD.Regular = pointer(false)
	if r.Validate() == nil {
		t.Fatal("nonregular accepted")
	}
	r = request(CaseFakeFile)
	r.Payload = &Payload{Unix: &UnixPayload{Name: "x"}}
	if r.Validate() == nil {
		t.Fatal("wrong payload accepted")
	}
	r.Payload = &Payload{File: &NamePayload{Name: "x"}, FD: &FDPayload{}}
	if r.Validate() == nil {
		t.Fatal("two payloads accepted")
	}
	r = request(CaseInventory)
	r.Payload = &Payload{}
	if r.Validate() == nil {
		t.Fatal("inventory payload accepted")
	}
	r = request(CaseFakeUnix)
	r.Payload = &Payload{Unix: &UnixPayload{Name: "x", Device: pointer(uint64(0)), Inode: pointer(uint64(0)), UID: pointer(uint32(0)), Nonce: "01234567"}}
	if r.Validate() != nil {
		t.Fatal("valid unix payload rejected")
	}
	for _, key := range []string{"device", "inode", "uid", "nonce"} {
		var obj map[string]any
		json.Unmarshal([]byte(wire(t, r)), &obj)
		delete(obj["payload"].(map[string]any)["unix"].(map[string]any), key)
		if _, err := DecodeRequest(strings.NewReader(wire(t, obj))); err == nil {
			t.Fatalf("missing %s accepted", key)
		}
	}
	input := wire(t, r)
	for _, entry := range []string{`"name":"x","name":"x"`, `"name":"x","Name":"x"`, `"name":"x","extra":"x"`} {
		if _, err := DecodeRequest(strings.NewReader(strings.Replace(input, `"name":"x"`, entry, 1))); err == nil {
			t.Fatal("nested invalid key accepted")
		}
	}
}

func TestUnixUIDPresenceAndProjectionValue(t *testing.T) {
	r := request(CaseFakeUnix)
	r.Payload = &Payload{Unix: &UnixPayload{Name: "socket", Device: pointer(uint64(1)), Inode: pointer(uint64(2)), UID: pointer(uint32(12345)), Nonce: "01234567"}}
	for _, uid := range []uint32{0, 12345} {
		r.Payload.Unix.UID = pointer(uid)
		got, err := DecodeRequest(strings.NewReader(wire(t, r)))
		if err != nil || got.Payload.Unix.UID == nil || *got.Payload.Unix.UID != uid {
			t.Fatal("explicit projected UID lost", got, err)
		}
	}
	input := wire(t, r)
	for _, replacement := range []string{`"uid":null`, `"uid":-1`, `"uid":4294967296`} {
		if _, err := DecodeRequest(strings.NewReader(strings.Replace(input, `"uid":12345`, replacement, 1))); err == nil {
			t.Fatal("invalid UID accepted", replacement)
		}
	}
}

type shortWriter struct{ calls int }

func (w *shortWriter) Write(p []byte) (int, error) { w.calls++; return len(p) - 1, nil }

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestObservationCodec(t *testing.T) {
	o := Observation{Version: Version1, RunID: "01234567", RequestID: "89abcdef", Case: CaseInventory, Status: "observed", Facts: []Fact{{Name: "present", State: "known", Value: pointer("false")}}}
	var b bytes.Buffer
	if err := EncodeObservation(&b, o); err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeObservation(&b)
	if err != nil || *decoded.Facts[0].Value != "false" {
		t.Fatal(decoded, err)
	}
	w := &shortWriter{}
	if err := EncodeObservation(w, o); !errors.Is(err, io.ErrShortWrite) || w.calls != 1 {
		t.Fatal("short write", err, w.calls)
	}
	if err := EncodeObservation(failingWriter{}, o); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
	for _, status := range []string{"pass", "fail", "", "supported"} {
		bad := o
		bad.Status = status
		if bad.Validate() == nil {
			t.Fatal("bad status", status)
		}
	}
	for _, f := range []Fact{{Name: "x", State: "known"}, {Name: "x", State: "missing", Value: pointer("0")}, {Name: "x", State: "unknown", Value: pointer("false")}, {Name: "x", State: "no"}} {
		bad := o
		bad.Facts = []Fact{f}
		if bad.Validate() == nil {
			t.Fatal("invalid presence", f)
		}
	}
	for _, state := range []string{"unknown", "missing"} {
		bad := o
		bad.Status = "inconclusive"
		bad.Reason = "missing data"
		bad.Facts = []Fact{{Name: "x", State: state}}
		if bad.Validate() != nil {
			t.Fatal("valid unknown rejected")
		}
		bad.Status = "observed"
		if bad.Validate() == nil {
			t.Fatal("unknown observed")
		}
	}
	for _, value := range []string{"", "0", "false", "bounded Unicode λ"} {
		good := o
		good.Facts = []Fact{{Name: "x", State: "known", Value: pointer(value)}}
		if good.Validate() != nil {
			t.Fatal("explicit known value rejected")
		}
	}
	bad := o
	bad.Facts = append([]Fact{}, o.Facts[0], o.Facts[0])
	if bad.Validate() == nil {
		t.Fatal("duplicate fact")
	}
	for _, errno := range []int{0, -1, 4096} {
		bad = o
		bad.Errno = &errno
		if bad.Validate() == nil {
			t.Fatal("invalid errno")
		}
	}
	bad = o
	bad.Facts = nil
	if bad.Validate() == nil {
		t.Fatal("missing facts")
	}
	bad = o
	bad.Status = "unsupported"
	if bad.Validate() == nil {
		t.Fatal("missing reason")
	}
	bad = o
	bad.Facts = []Fact{}
	for i := 0; i < MaxFacts; i++ {
		bad.Facts = append(bad.Facts, Fact{Name: "row." + strconv.Itoa(i), State: "known", Value: pointer(strings.Repeat("x", 1024))})
	}
	if err := bad.Validate(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := EncodeObservation(&output, bad); err == nil || output.Len() != 0 {
		t.Fatal("oversize output was emitted")
	}
}
