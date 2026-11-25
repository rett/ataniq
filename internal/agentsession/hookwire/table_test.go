package hookwire

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"ataniq/internal/pack"
)

const rowJSON = `{"id":"alpha.start","event":"Start","fields":[{"name":"name","pointer":"/session/id"}],"digests":[{"name":"input","pointer":""}],"async":false,"timeout_ms":200,"output":"none"}`
const tableJSON = `{"schema":"ataniq.pack-declaration/1","builds":{"1":[` + rowJSON + `],"2":[]}}`

func TestDecodeTable(t *testing.T) {
	tables, err := DecodeTable([]byte(tableJSON))
	if err != nil {
		t.Fatal(err)
	}
	const manifest = `{"schema":"ataniq.pack-declaration/1","name":"alpha","class":"runtime","display":"Alpha","vendor":"Example","no_words_reason":"fixture","builds":[{"version":"1","transports":["terminal"],"source":"fixture","verified_on":""},{"version":"2","transports":["terminal"],"source":"fixture","verified_on":""}]}`
	d, err := pack.DecodeDeclaration(map[string][]byte{pack.ManifestPath: []byte(manifest), pack.HooksPath: []byte(tableJSON)}, pack.BindLayout{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tables["1"], Table(d.Hooks["1"])) || len(tables["2"]) != 0 {
		t.Fatalf("unexpected table: %+v", tables)
	}
	if req, err := tables["1"].DecodeRequest(packet(eventJSON)); req == nil || err != nil {
		t.Fatal(err)
	}
	fetch := packet(strings.Replace(eventJSON, "event", "fetch", 1))
	for _, data := range [][]byte{fetch, packet(strings.Replace(eventJSON, "alpha.start", "beta.end", 1)), packet(strings.Replace(eventJSON, `"name":`, `"other":`, 1)), packet(strings.Replace(eventJSON, `"digests":{}`, `"digests":{"name":"`+strings.Repeat("0", 64)+`"}`, 1))} {
		if req, err := tables["1"].DecodeRequest(data); req != nil || err == nil {
			t.Error("accepted undeclared request")
		}
	}
	tables, err = DecodeTable([]byte(strings.Replace(tableJSON, `"none"`, `"continuation"`, 1)))
	if err != nil {
		t.Fatal(err)
	}
	if req, err := tables["1"].DecodeRequest(fetch); req == nil || err != nil {
		t.Fatal(err)
	}
}

func TestDecodeTableFirstInvalidBuild(t *testing.T) {
	const want = `build "a": invalid hook row`
	for _, keys := range [][2]string{{"a", "b"}, {"b", "a"}} {
		data := []byte(fmt.Sprintf(`{"schema":"ataniq.pack-declaration/1","builds":{%q:[{"async":false}],%q:[{"async":false}]}}`, keys[0], keys[1]))
		for range 100 {
			tables, err := DecodeTable(data)
			if tables != nil || err == nil || err.Error() != want {
				t.Fatalf("builds %v: got %v, %v; want nil table and %q", keys, tables, err, want)
			}
		}
	}
}

func TestBadTables(t *testing.T) {
	for _, edit := range [][2]string{
		{`/1"`, `/2"`}, {`"2":[]`, `"1":[]`}, {`"2":[]`, `"2":null`}, {`"builds"`, `"unknown"`},
		{`"id":"alpha.start"`, `"id":"upperCase"`}, {`"async":false,`, ""}, {`"async":false`, `"async":null`},
		{`"timeout_ms":200`, `"timeout_ms":3600001`}, {`"output":"none"`, `"output":"allow"`},
		{`"event":"Start",`, ""}, {`"pointer":"/session/id"`, `"pointer":"/~2"`},
		{`,"pointer":""`, ""}, {`"fields":[`, `"fields":[{"name":"name","pointer":""},`},
		{`"digests":[`, `"digests":[{"name":"bad-name","pointer":""},`},
		{rowJSON, rowJSON + "," + rowJSON}, {`"async":false`, `"async":false,"turn_end":true`},
	} {
		old, replacement := edit[0], edit[1]
		data := strings.Replace(tableJSON, old, replacement, 1)
		if tab, err := DecodeTable([]byte(data)); tab != nil || err == nil {
			t.Errorf("accepted %s", data)
		}
	}
	for _, data := range [][]byte{[]byte(tableJSON + "\n"), bytes.Repeat([]byte(" "), pack.MaxDeclarationBytes+1)} {
		if tab, err := DecodeTable(data); tab != nil || err == nil {
			t.Fatal("accepted trailing or oversized table")
		}
	}
	for _, tc := range []struct {
		name string
		n    int
	}{{"fields", MaxFields}, {"digests", MaxDigests}} {
		items := make([]string, tc.n+1)
		for i := range items {
			items[i] = fmt.Sprintf(`{"name":"%c","pointer":""}`, 'a'+i)
		}
		for _, n := range []int{tc.n, tc.n + 1} {
			row := fmt.Sprintf(`{"id":"beta","event":"End","async":true,"timeout_ms":1,"output":"continuation","%s":[%s]}`, tc.name, strings.Join(items[:n], ","))
			tables, err := DecodeTable([]byte(strings.Replace(tableJSON, rowJSON, row, 1)))
			if (err == nil) != (n == tc.n) || (tables == nil) != (err != nil) {
				t.Errorf("%s count %d: %v", tc.name, n, err)
			}
		}
	}
}
