package record

import (
	"strings"
	"testing"
)

// strictSample is a payload type of the kind another package reads with the
// exported API: a string, a Text, an int, bytes, a list and an optional field.
type strictSample struct {
	Name  string   `json:"name"`
	Body  Text     `json:"body"`
	N     int      `json:"n"`
	Data  []byte   `json:"data"`
	List  []string `json:"list"`
	Extra string   `json:"extra,omitempty"`
}

func TestReaderReadsAndMarshalsThePayloadTypes(t *testing.T) {
	r := Reader{MaxDepth: 4, Noun: "payload"}
	in := `{"body":{"b64":"AQI="},"data":"YWJj","extra":"x","list":["a"],"n":7,"name":"p"}`
	var got strictSample
	if err := r.Unmarshal([]byte(in), "sample", &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "p" || got.Body != "\x01\x02" || got.N != 7 || string(got.Data) != "abc" || got.Extra != "x" {
		t.Errorf("read %+v", got)
	}
	out, err := Marshal(&got, "sample")
	if err != nil || string(out) != in {
		t.Errorf("Marshal = %s, %v, want the canonical form %s", out, err, in)
	}
	pre, err := AppendMarshal([]byte("x"), &got, "sample")
	if err != nil || string(pre) != "x"+in {
		t.Errorf("AppendMarshal = %s, %v", pre, err)
	}
}

func TestReaderRefusesWhatARecordRefuses(t *testing.T) {
	r := Reader{MaxDepth: 3, Noun: "payload"}
	base := `{"body":"b","data":"YWJj","list":[],"n":1,"name":"p"}`
	cases := map[string]string{
		"a repeated key":                  `{"body":"b","body":"b","data":"YWJj","list":[],"n":1,"name":"p"}`,
		"a key in another case":           `{"Body":"b","data":"YWJj","list":[],"n":1,"name":"p"}`,
		"an unknown field":                `{"body":"b","data":"YWJj","list":[],"n":1,"name":"p","more":1}`,
		"a missing field":                 `{"body":"b","data":"YWJj","list":[],"n":1}`,
		"a float":                         `{"body":"b","data":"YWJj","list":[],"n":1.0,"name":"p"}`,
		"a null":                          `{"body":"b","data":"YWJj","list":[],"n":null,"name":"p"}`,
		"a control character":             `{"body":"b","data":"YWJj","list":[],"n":1,"name":"p\u0007"}`,
		"raw invalid UTF-8":               "{\"body\":\"b\",\"data\":\"YWJj\",\"list\":[],\"n\":1,\"name\":\"p\xff\"}",
		"loose base64 bits":               `{"body":"b","data":"YR==","list":[],"n":1,"name":"p"}`,
		"base64 that is not a string":     `{"body":"b","data":1,"list":[],"n":1,"name":"p"}`,
		"a b64 that could be a string":    `{"body":{"b64":"eA=="},"data":"YWJj","list":[],"n":1,"name":"p"}`,
		"an empty optional field":         `{"body":"b","data":"YWJj","extra":"","list":[],"n":1,"name":"p"}`,
		"an int over the int range":       `{"body":"b","data":"YWJj","list":[],"n":99999999999999999999,"name":"p"}`,
		"data after the value":            base + `{}`,
		"nesting over the depth":          `{"body":"b","data":"YWJj","list":[[[["a"]]]],"n":1,"name":"p"}`,
		"a b64 with a second key":         `{"body":{"b64":"AQI=","x":1},"data":"YWJj","list":[],"n":1,"name":"p"}`,
		"a number that is not an integer": `{"body":"b","data":"YWJj","list":[],"n":1e0,"name":"p"}`,
	}
	if err := r.Unmarshal([]byte(base), "sample", new(strictSample)); err != nil {
		t.Fatalf("the base case is refused: %v", err)
	}
	for name, in := range cases {
		err := r.Unmarshal([]byte(in), "sample", new(strictSample))
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if !Text(err.Error()).Plain() || strings.Contains(err.Error(), "\xff") {
			t.Errorf("%s: the refusal holds bytes of the data: %q", name, err)
		}
	}
	// The depth is the reader's own, and the noun is in its texts.
	deep := `{"body":"b","data":"","list":[],"n":1,"name":"p"}`
	if err := (Reader{MaxDepth: 1, Noun: "frame"}).Unmarshal([]byte(deep), "sample", new(strictSample)); err == nil || !strings.Contains(err.Error(), "the frame nests too deep") {
		t.Errorf("depth 1: %v, want the frame nests too deep", err)
	}
	if err := (Reader{MaxDepth: 4, Noun: "frame"}).Unmarshal([]byte("\xff"), "sample", new(strictSample)); err == nil || !strings.HasPrefix(err.Error(), "sample: the frame is not valid UTF-8") {
		t.Errorf("invalid UTF-8: %v", err)
	}
}

func TestMarshalRefusesAPlainStringItCannotCarry(t *testing.T) {
	if _, err := Marshal(&strictSample{Name: "a\nb"}, "sample"); err == nil {
		t.Error("a plain string with a line feed was written")
	}
	if _, err := Marshal(&strictSample{Name: "a\xffb"}, "sample"); err == nil {
		t.Error("a plain string with invalid UTF-8 was written")
	}
	if _, err := Marshal(&strictSample{Body: "a\nb"}, "sample"); err != nil {
		t.Errorf("a Text with a line feed: %v", err)
	}
}

func TestTextLenIsTheLengthOfItsJSON(t *testing.T) {
	for _, s := range []string{"", "a", `a"b\c`, "tab\there", "caf\xe9", "\xff\xfe", "日本語", strings.Repeat("x", 1000)} {
		b, err := Text(s).MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		if got := TextLen(Text(s)); got != len(b) {
			t.Errorf("TextLen(%q) = %d, the JSON is %d bytes", s, got, len(b))
		}
	}
	if got, want := B64FrameBytes, len(`{"b64":""}`); got != want {
		t.Errorf("B64FrameBytes = %d, want %d", got, want)
	}
}

// The validators the owner connection shares are the record's own.
func TestSharedValidators(t *testing.T) {
	for _, k := range []string{FlagExecConfig, FlagSymlink, FlagSubmodule, FlagAliased, FlagHistoryOnly, FlagOddPath} {
		if !ValidFlagKind(k) {
			t.Errorf("ValidFlagKind(%q) is false", k)
		}
	}
	for _, k := range []string{"", "Symlink", "symlinks", "grant"} {
		if ValidFlagKind(k) {
			t.Errorf("ValidFlagKind(%q) is true", k)
		}
	}
	for s, want := range map[string]bool{"100644": true, "000000": true, "160000": true, "10064": false, "1006444": false, "100648": false, "10064a": false} {
		if ValidMode(s) != want {
			t.Errorf("ValidMode(%q) = %v", s, !want)
		}
	}
	for _, c := range []struct {
		s    string
		n    int
		want bool
	}{{"0f", 2, true}, {"0F", 2, false}, {"0g", 2, false}, {"0f", 3, false}, {"", 0, true}} {
		if IsLowerHex(c.s, c.n) != c.want {
			t.Errorf("IsLowerHex(%q, %d) = %v", c.s, c.n, !c.want)
		}
	}
	for r, want := range map[rune]bool{0: true, 0x1f: true, 0x7f: true, 0x80: true, 0x9f: true, 0xa0: false, ' ': false, 'a': false, 0x2028: false} {
		if IsControl(r) != want {
			t.Errorf("IsControl(%U) = %v", r, !want)
		}
	}
}
