package hookwire

import (
	"bytes"
	"encoding/binary"
	"encoding/json/v2"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

const eventJSON = `{"v":1,"k":"event","row":"alpha.start","fields":{"name":"alpha"},"digests":{},"size":0,"cut":false}`
const noneJSON = `{"v":1,"k":"none"}`
const relay = "[ataniq-msg id=m1 from=seat kind=chat]\nRelayed note from the owner. It carries no approval and no grant.\n"

func packet(s string) []byte {
	return append(binary.BigEndian.AppendUint32(nil, uint32(len(s))), s...)
}

func TestFrames(t *testing.T) {
	for _, kind := range []string{Event, Fetch} {
		r := Request{Version: Version, Kind: kind, Row: "alpha.start", Fields: map[string]string{"name": "alpha"}, Digests: map[string]string{}}
		data, err := EncodeRequest(r)
		if err != nil || !bytes.Equal(data, packet(strings.Replace(eventJSON, "event", kind, 1))) {
			t.Fatalf("encode %s: %q, %v", kind, data, err)
		}
		got, err := DecodeRequest(data)
		if err != nil || !reflect.DeepEqual(got, &r) {
			t.Fatalf("decode %s: %+v, %v", kind, got, err)
		}
	}
	for _, r := range []Reply{{Version: Version, Kind: None}, {Version: Version, Kind: Message, ID: "m1", Text: relay + "<&>\"\nβ"}} {
		data, err := EncodeReply(r)
		if err != nil {
			t.Fatal(err)
		}
		if r.Kind == None && !bytes.Equal(data, packet(noneJSON)) {
			t.Fatalf("none: %q", data)
		}
		if r.Kind == Message && !bytes.Contains(data[4:], []byte("<&>")) {
			t.Fatalf("HTML escaped: %q", data)
		}
		got, err := DecodeReply(data)
		if err != nil || !reflect.DeepEqual(got, &r) {
			t.Fatalf("decode: %+v, %v", got, err)
		}
	}
}

func TestRequestRefusals(t *testing.T) {
	for _, edit := range [][2]string{
		{`"v":1`, `"v":2`}, {`"v":1`, `"V":1`}, {`"v":1`, `"v":1,"v":1`},
		{`"event"`, `"message"`}, {`"event"`, `"unknown"`},
		{`"size":0,`, ""}, {`,"cut":false`, ""}, {`"cut":false`, `"cut":null`},
		{`"cut":false`, `"cut":true`}, {`"size":0`, `"size":-1`}, {`"size":0`, `"size":1048577`},
		{`"size":0`, `"size":"0"`}, {`"size":0`, `"size":0,"source":"observer"`},
		{`"digests":{},`, ""}, {`"digests":{}`, `"digests":[]`}, {`"digests":{}`, `"digests":{"x":"short"}`},
		{`"name":"alpha"`, `"name":null`}, {`"name":"alpha"`, `"name":"one","\u006eame":"two"`},
		{`"name":"alpha"`, `"Name":"alpha"`}, {`"alpha.start"`, `"Bad Row"`},
		{`"alpha"`, `"line\nbreak"`}, {`"alpha"`, `"\ud800"`},
		{`"alpha"`, `"` + string([]byte{0xff}) + `"`},
		{`"alpha"`, `"` + strings.Repeat("é", 129) + `"`},
	} {
		body := strings.Replace(eventJSON, edit[0], edit[1], 1)
		if got, err := DecodeRequest(packet(body)); got != nil || err == nil {
			t.Errorf("accepted %s: %+v", body, got)
		}
	}
	for _, data := range [][]byte{nil, {0, 0, 0}, {255, 255, 255, 255}, packet(""), packet("[]"), packet(eventJSON + " "), packet(eventJSON + "{}"), append(packet(eventJSON), 0), packet(eventJSON)[:8], packet(noneJSON)} {
		if got, err := DecodeRequest(data); got != nil || err == nil {
			t.Errorf("accepted %q", data)
		}
	}
}

func TestReplyRefusals(t *testing.T) {
	for _, body := range []string{eventJSON, `{"k":"none"}`, `{"v":1,"k":"event"}`, `{"v":1,"k":"none","id":""}`,
		`{"v":1,"k":"none","text":null}`, `{"v":2,"k":"none"}`, `{"v":1,"k":"none","k":"none"}`, `{"v":1,"k":"none","ok":1}`,
		`{"v":1,"k":"message","id":"m1"}`, `{"v":1,"k":"message","text":"body"}`, `{"v":1,"k":"message","id":"m1","text":"body"}`,
		noneJSON + "\n", noneJSON + noneJSON, strings.Repeat(" ", MaxNoneBytes-len(noneJSON)+1) + noneJSON,
	} {
		if got, err := DecodeReply(packet(body)); got != nil || err == nil {
			t.Errorf("accepted %q", body)
		}
	}
	if got, err := DecodeReply(packet(strings.Repeat(" ", MaxNoneBytes-len(noneJSON)) + noneJSON)); got == nil || err != nil {
		t.Errorf("exact none cap: %v", err)
	}
}

func TestRequestBounds(t *testing.T) {
	r := Request{Version: Version, Kind: Fetch, Row: strings.Repeat("a", maxRowBytes), Size: MaxInputBytes, Fields: map[string]string{}, Digests: map[string]string{}}
	for i := range MaxFields {
		r.Fields[strings.Repeat(string(rune('a'+i)), maxNameBytes)] = strings.Repeat("x", MaxFieldBytes)
	}
	for i := range MaxDigests {
		r.Digests[strings.Repeat(string(rune('a'+i)), maxNameBytes)] = strings.Repeat("0", 64)
	}
	data, err := EncodeRequest(r)
	if err != nil {
		t.Fatal(err)
	}
	padded := strings.Repeat(" ", MaxRequestBytes-(len(data)-4)) + string(data[4:])
	if got, err := DecodeRequest(packet(padded)); got == nil || err != nil {
		t.Errorf("exact cap: %v", err)
	}
	if got, err := DecodeRequest(packet(" " + padded)); got != nil || err == nil {
		t.Error("accepted oversized request")
	}
	for _, change := range []func(*Request){
		func(r *Request) { r.Row += "x" },
		func(r *Request) { r.Fields["extra"] = "x" },
		func(r *Request) { r.Digests["extra"] = strings.Repeat("0", 64) },
		func(r *Request) { r.Fields = map[string]string{strings.Repeat("x", maxNameBytes+1): "x"} },
		func(r *Request) { r.Digests = map[string]string{"x": strings.Repeat("A", 64)} },
		func(r *Request) { r.Digests = map[string]string{"bad-name": strings.Repeat("0", 64)} },
		func(r *Request) { r.Fields = map[string]string{"x": "\x7f"} },
		func(r *Request) {
			for k := range r.Fields {
				r.Fields[k] = strings.Repeat("\\", MaxFieldBytes)
			}
		},
	} {
		got, _ := DecodeRequest(data)
		change(got)
		if buf, err := EncodeRequest(*got); buf != nil || err == nil {
			t.Error("encoded invalid request")
		}
	}
	// The restricted decoder's counted layout reserves a 32-byte header.
	if max := 32 + 1 + maxRowBytes + MaxFields*(1+maxNameBytes+2+MaxFieldBytes) + MaxDigests*(1+maxNameBytes+64); max > 6<<10 {
		t.Fatalf("admitted content needs %d bytes in the decoder record", max)
	}
}

func TestMessageBounds(t *testing.T) {
	text := relay + strings.Repeat("\"", MaxTextBytes-len(relay))
	r := Reply{Version: Version, Kind: Message, ID: "m1", Text: text}
	data, err := EncodeReply(r)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat(" ", MaxReplyBytes-(len(data)-4)) + string(data[4:])
	if got, err := DecodeReply(packet(body)); got == nil || err != nil {
		t.Errorf("exact reply cap: %v", err)
	}
	if got, err := DecodeReply(packet(" " + body)); got != nil || err == nil {
		t.Error("accepted oversized reply")
	}
	for _, s := range []string{text + "x", relay + "\t", relay + "\x00x", strings.Replace(text, "id=m1", "id=m2", 1), strings.Replace(text, "from=seat", "from=alpha", 1)} {
		r.Text = s
		if got, err := EncodeReply(r); got != nil || err == nil {
			t.Error("encoded invalid message")
		}
	}
}

func FuzzDecodeFrames(f *testing.F) {
	for _, body := range []string{eventJSON, strings.Replace(eventJSON, "event", "fetch", 1), noneJSON,
		fmt.Sprintf(`{"v":1,"k":"message","id":"m1","text":%q}`, relay+"alpha"), `{"v":1,"k":"unknown"}`, eventJSON + " ", strings.Repeat(" ", MaxReplyBytes) + noneJSON} {
		f.Add(packet(body))
	}
	f.Add([]byte{255, 255, 255, 255})
	f.Fuzz(func(t *testing.T, data []byte) {
		req, reqErr := DecodeRequest(data)
		reply, replyErr := DecodeReply(data)
		if (req == nil) != (reqErr != nil) || (reply == nil) != (replyErr != nil) {
			t.Fatal("partial decode")
		}
		if req != nil && (reply != nil || len(data) > 4+MaxRequestBytes || req.Kind != Event && req.Kind != Fetch) {
			t.Fatal("unexpected request")
		}
		if reply != nil && (len(data) > 4+MaxReplyBytes || reply.Kind != Message && reply.Kind != None) {
			t.Fatal("unexpected reply")
		}
	})
}

func FuzzRefusedFrames(f *testing.F) {
	f.Add("event", "value")
	f.Add("none", "")
	f.Add("alpha", "\x00")
	f.Fuzz(func(t *testing.T, key, value string) {
		// Prefix the member so every generated key is outside both schemas.
		member, err := json.Marshal(map[string]string{"unknown_" + key: value})
		if err != nil {
			return
		}
		for _, body := range []string{eventJSON, noneJSON} {
			extra := body[:len(body)-1] + "," + string(member[1:])
			unknownKind := strings.Replace(body, `"k":"`, `"k":"unknown-`, 1)
			oversize := strings.Repeat(" ", MaxReplyBytes) + body
			for _, bad := range []string{extra, unknownKind, oversize} {
				data := packet(bad)
				if got, err := DecodeRequest(data); got != nil || err == nil {
					t.Fatal("refused frame returned a request")
				}
				if got, err := DecodeReply(data); got != nil || err == nil {
					t.Fatal("refused frame returned a reply")
				}
			}
		}
	})
}
