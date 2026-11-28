package canon

import (
	"bytes"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestParseRefuses(t *testing.T) {
	cases := map[string]string{
		"float":            `{"a":1.5}`,
		"exponent":         `{"a":1e3}`,
		"null":             `{"a":null}`,
		"repeated key":     `{"a":1,"a":2}`,
		"repeated escaped": `{"a":1,"\u0061":2}`,
		"control raw":      "{\"a\":\"x\ty\"}",
		"control escaped":  `{"a":"\u0007"}`,
		"control c1":       `{"a":"` + "\u0085" + `"}`,
		"trailing data":    `{} {}`,
		"leading zero":     `{"a":01}`,
		"minus zero":       `{"a":-0}`,
		"too large":        `{"a":99999999999999999999}`,
		"lone surrogate":   `{"a":"\ud800"}`,
		"bad escape":       `{"a":"\x"}`,
		"unterminated":     `{"a":"x`,
		"empty":            ``,
		"not utf8":         "\"\xff\"",
		"too deep":         `[[[[[[[[[1]]]]]]]]]`,
		"key not string":   `{1:2}`,
		"no colon":         `{"a" 1}`,
		"bad list":         `[1 2]`,
	}
	for name, in := range cases {
		if _, err := Parse([]byte(in), 8); err == nil {
			t.Errorf("%s: parsed", name)
		} else if strings.Contains(err.Error(), "\"") {
			t.Errorf("%s: the error text %q quotes data", name, err)
		}
	}
}

func TestEncodeIsCanonical(t *testing.T) {
	in := "{ \"b\" : [1, true, \"<&>\\u2028\"], \"a\": {\"z\":\"q\\\"\\\\\", \"y\":-3}, \"\\u00e9\": \"\\/\" }"
	v, err := Parse([]byte(in), 8)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Encode(v)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"a\":{\"y\":-3,\"z\":\"q\\\"\\\\\"},\"b\":[1,true,\"<&>\u2028\"],\"é\":\"/\"}"
	if string(out) != want {
		t.Fatalf("got  %s\nwant %s", out, want)
	}
	back, err := Parse(out, 8)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Encode(back)
	if err != nil || !bytes.Equal(again, out) {
		t.Fatalf("the canonical form is not stable: %s (%v)", again, err)
	}
}

func TestEncodeRefuses(t *testing.T) {
	for name, v := range map[string]Value{
		"control":   Str("a\nb"),
		"not utf8":  Str("\xff"),
		"key twice": Obj(M("a", Num(1)), M("a", Num(2))),
		"bad key":   Obj(M("\x01", Num(1))),
		"no kind":   List(Value{}),
	} {
		if _, err := Encode(v); err == nil {
			t.Errorf("%s: encoded", name)
		}
	}
	if out, err := Encode(List()); err != nil || string(out) != "[]" {
		t.Fatalf("an empty list: %s (%v)", out, err)
	}
	if out, err := Encode(Obj()); err != nil || string(out) != "{}" {
		t.Fatalf("an empty object: %s (%v)", out, err)
	}
}

func TestFields(t *testing.T) {
	v, err := Parse([]byte(`{"a":1,"b":"x"}`), 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Fields(v, "f", []string{"a"}, []string{"b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Fields(v, "f", []string{"a"}, nil); err == nil {
		t.Fatal("an unknown field was accepted")
	}
	if _, err := Fields(v, "f", []string{"a", "b", "c"}, nil); err == nil {
		t.Fatal("a missing field was accepted")
	}
	if _, err := Fields(Str("x"), "f", nil, nil); err == nil {
		t.Fatal("a string was taken for an object")
	}
	if _, err := AsInt(Num(7), "n", 0, 6); err == nil {
		t.Fatal("an integer out of range was accepted")
	}
	if _, err := AsStrings(Strs([]string{"a", "b", "c"}), "l", 2); err == nil {
		t.Fatal("a list over its cap was accepted")
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{`{"a":[1,"x",true,{}]}`, `[]`, `"\u00e9"`, `{"a":1,"a":2}`, `1.5`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		v, err := Parse(data, 8)
		if err != nil {
			return
		}
		out, err := Encode(v)
		if err != nil {
			t.Fatalf("a parsed value does not encode: %v", err)
		}
		if len(out) > len(data) {
			t.Fatalf("%d bytes encode to %d", len(data), len(out))
		}
		back, err := Parse(out, 8)
		if err != nil {
			t.Fatalf("the canonical form does not parse: %v", err)
		}
		again, err := Encode(back)
		if err != nil || !bytes.Equal(again, out) {
			t.Fatalf("the canonical form is not stable (%v)", err)
		}
		if reflect.DeepEqual(v, Value{}) {
			t.Fatal("a parsed value has no kind")
		}
	})
}

// The package imports the standard library only.
func TestImportsTheStandardLibraryOnly(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), e.Name(), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			first, _, _ := strings.Cut(path, "/")
			if strings.Contains(first, ".") || first == "ataniq" {
				t.Errorf("%s imports %s", e.Name(), path)
			}
		}
	}
}
