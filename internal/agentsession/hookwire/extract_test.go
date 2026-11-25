package hookwire

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"

	"ataniq/internal/pack"
)

func selection(name, path string) pack.HookField {
	p := jsontext.Pointer(path)
	return pack.HookField{Name: name, Pointer: &p}
}

func endRow() pack.HookRow {
	return pack.HookRow{
		ID: "beta.end", Event: "End", Output: "continuation", TimeoutMS: 250,
		Fields: []pack.HookField{
			selection("session", "/a~1b/~0/1"), selection("flag", "/active"), selection("number", "/n"), selection("empty", "/"),
			selection("missing", "/absent"), selection("obj", "/input"), selection("null", "/nil"), selection("past", "/a~1b/~0/2"),
			selection("leading", "/a~1b/~0/01"), selection("dash", "/a~1b/~0/-"), selection("inside_scalar", "/n/0"),
		},
		Digests: []pack.HookField{selection("input", "/input"), selection("whole", ""), selection("string", "/a~1b/~0/1"), selection("null", "/nil")},
	}
}

func TestExtractSelections(t *testing.T) {
	const obj = `{ "b" : 2, "a":1 }`
	const str = `"\u03b2\u0007-end"`
	const value = `{"": "", "a/b":{"~":[{},` + str + `]}, "active":false, "n":-1.00e+12,"nil":null,"input":` + obj + `}`
	input := "\t " + value + "\n"
	row := endRow()
	got, err := Extract(row, strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	wantFields := map[string]string{"session": "β-end", "flag": "false", "number": "-1.00e+12", "empty": ""}
	wantDigests := map[string]string{}
	for name, bytes := range map[string]string{"input": obj, "whole": value, "string": str, "null": "null"} {
		wantDigests[name] = fmt.Sprintf("%x", sha256.Sum256([]byte(bytes)))
	}
	if got.Kind != Fetch || got.Cut || got.Size != len(input) || !reflect.DeepEqual(got.Fields, wantFields) || !reflect.DeepEqual(got.Digests, wantDigests) {
		t.Fatalf("extracted %+v", got)
	}
	packet, err := EncodeRequest(*got)
	if err != nil {
		t.Fatal(err)
	}
	again, err := (Table{row}).DecodeRequest(packet)
	if err != nil || !reflect.DeepEqual(got, again) {
		t.Fatalf("row round trip: %+v, %v", again, err)
	}
}

func TestKeptText(t *testing.T) {
	row := endRow()
	row.Output = "deny-from-table"
	row.Fields = []pack.HookField{selection("s", "")}
	row.Digests = nil
	prefix := strings.Repeat("x", MaxFieldBytes-2)
	for _, tc := range []struct{ input, want string }{
		{"\u0000", ""}, {"a\u202eb\u007fc", "abc"}, {prefix + "\néz", prefix + "é"}, {prefix + "世x", prefix},
	} {
		raw, err := jsontext.AppendQuote(nil, []byte(tc.input))
		if err != nil {
			t.Fatal(err)
		}
		got, err := Extract(row, bytes.NewReader(raw))
		if err != nil || got.Cut || got.Kind != Event || got.Fields["s"] != tc.want {
			t.Fatalf("kept text %+v, %v", got, err)
		}
	}
}

func TestInputBounds(t *testing.T) {
	row := endRow()
	sized := strings.Repeat(" ", MaxInputBytes-2) + "{}"
	for _, suffix := range []string{"", "x", "more to come"} {
		input := strings.NewReader(sized + suffix)
		got, err := Extract(row, input)
		if err != nil || got.Size != MaxInputBytes || got.Cut != (suffix != "") || input.Len() != max(len(suffix)-1, 0) {
			t.Fatalf("bound: %+v, unread %d, %v", got, input.Len(), err)
		}
		if suffix == "" && len(got.Digests) != 1 {
			t.Fatalf("missing root digest at exact limit: %+v", got)
		}
		if suffix != "" && (len(got.Fields) != 0 || len(got.Digests) != 0) {
			t.Fatal("cut input yielded selections")
		}
	}
	for _, input := range []string{"", `{"active":true}`, sized} {
		got, err := Extract(row, io.MultiReader(strings.NewReader(input), iotest.ErrReader(context.DeadlineExceeded)))
		if err != nil || !got.Cut || got.Size != len(input) || len(got.Fields) != 0 || len(got.Digests) != 0 {
			t.Fatalf("input timeout: %+v, %v", got, err)
		}
	}
}

func TestExtractFailures(t *testing.T) {
	row := endRow()
	for _, input := range []string{"", `{"active":true,"unfinished":`, `{"active":true}{}`, `{"active":true,"unselected":{"x":1,"x":2}}`, "\xff"} {
		if r, err := Extract(row, strings.NewReader(input)); r != nil || err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
	bad := errors.New("read failed")
	if r, err := Extract(row, io.MultiReader(strings.NewReader("{}"), iotest.ErrReader(bad))); r != nil || !errors.Is(err, bad) {
		t.Fatalf("read failure: %+v, %v", r, err)
	}
	row.Fields[0].Pointer = nil
	if r, err := Extract(row, strings.NewReader("{}")); r != nil || err == nil {
		t.Fatal("extracted from invalid row")
	}
}

func FuzzExtract(f *testing.F) {
	f.Add(`{"a":[{"x":1,"y":2},false]}`, "/a/1")
	f.Add(`{"a/b":{"~":"β"}}`, "/a~1b/~0")
	f.Add(`{"a":1,"a":2}`, "")
	f.Add(`[null]`, "/18446744073709551616")
	f.Add(`"\udfff"`, "/~3")
	f.Fuzz(func(t *testing.T, input, path string) {
		row := endRow()
		row.Fields = []pack.HookField{selection("field", path)}
		row.Digests = []pack.HookField{selection("digest", path)}
		r, err := Extract(row, strings.NewReader(input))
		if err != nil {
			if r != nil {
				t.Fatal("partial extraction")
			}
			return
		}
		packet, err := EncodeRequest(*r)
		if err != nil {
			t.Fatal(err)
		}
		again, err := (Table{row}).DecodeRequest(packet)
		if err != nil || !reflect.DeepEqual(r, again) {
			t.Fatalf("invalid extraction: %+v, %v", again, err)
		}
	})
}
