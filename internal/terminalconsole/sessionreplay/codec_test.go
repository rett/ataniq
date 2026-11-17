package sessionreplay_test

import (
	"bytes"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"ataniq/internal/terminalconsole/sessionreplay"
)

const sampleText = `{"format":"ataniq-session-replay","version":1,"cols":100,"rows":30}
{"t":0,"k":"out","d":"YQ=="}
{"t":250,"k":"resize","cols":80,"rows":24}
{"t":250,"k":"out","d":"G1s="}
{"t":900,"k":"end","status":3}
`

func TestEncodeIsTheDocumentedText(t *testing.T) {
	r := sample()
	got, err := sessionreplay.Encode(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != sampleText {
		t.Fatalf("got\n%s\nwant\n%s", got, sampleText)
	}
	again, _ := sessionreplay.Encode(r)
	if !bytes.Equal(got, again) {
		t.Fatal("encoding is not repeatable")
	}
}

func TestEndWithoutStatusIsUnknown(t *testing.T) {
	const text = `{"format":"ataniq-session-replay","version":1,"cols":10,"rows":5}
{"t":1,"k":"out","d":"YQ=="}
{"t":5,"k":"end"}
`
	rec, err := sessionreplay.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	end := rec.Events[len(rec.Events)-1]
	if end.Kind != sessionreplay.KindEnd || end.StatusKnown || end.Status != 0 {
		t.Fatalf("end %+v", end)
	}
	enc, err := sessionreplay.Encode(rec)
	if err != nil || string(enc) != text {
		t.Fatalf("encoded %q (%v)", enc, err)
	}
	// A known status of zero is not the same as an unknown one.
	rec.Events[len(rec.Events)-1].StatusKnown = true
	enc, err = sessionreplay.Encode(rec)
	if err != nil || !strings.Contains(string(enc), `{"t":5,"k":"end","status":0}`) {
		t.Fatalf("encoded %q (%v)", enc, err)
	}
	back, err := sessionreplay.Parse(enc)
	if err != nil || !back.Events[len(back.Events)-1].StatusKnown {
		t.Fatalf("round trip: %+v (%v)", back, err)
	}
}

func TestParseRoundTrip(t *testing.T) {
	enc, err := sessionreplay.Encode(sample())
	if err != nil {
		t.Fatal(err)
	}
	got, err := sessionreplay.Parse(enc)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, sample()) {
		t.Fatalf("got %+v", got)
	}
}

func TestEncodeRejectsInvalid(t *testing.T) {
	r := sample()
	r.Events = r.Events[:3]
	if _, err := sessionreplay.Encode(r); err == nil {
		t.Fatal("accepted")
	}
}

func TestParseRejects(t *testing.T) {
	hdr := `{"format":"ataniq-session-replay","version":1,"cols":10,"rows":5}` + "\n"
	end := `{"t":5,"k":"end","status":0}` + "\n"
	cases := map[string]string{
		"empty":                "",
		"no final newline":     hdr + strings.TrimSuffix(end, "\n"),
		"blank line":           hdr + "\n" + end,
		"wrong format":         strings.Replace(hdr+end, "ataniq-session-replay", "other", 1),
		"wrong version":        strings.Replace(hdr+end, `"version":1`, `"version":2`, 1),
		"unknown header field": strings.Replace(hdr+end, `"rows":5`, `"rows":5,"title":"x"`, 1),
		"not json":             hdr + "end\n",
		"two objects":          hdr + end[:len(end)-1] + end,
		"trailing text":        hdr + `{"t":5,"k":"end","status":0} x` + "\n",
		"unknown kind":         hdr + `{"t":1,"k":"in","d":"YQ=="}` + "\n" + end,
		"unknown field":        hdr + `{"t":1,"k":"out","d":"YQ==","x":1}` + "\n" + end,
		"missing time":         hdr + `{"k":"end","status":0}` + "\n",
		"negative time":        hdr + `{"t":-1,"k":"end","status":0}` + "\n",
		"time over limit":      hdr + `{"t":86400001,"k":"end","status":0}` + "\n",
		"fractional time":      hdr + `{"t":1.5,"k":"end","status":0}` + "\n",
		"bad base64":           hdr + `{"t":1,"k":"out","d":"!!"}` + "\n" + end,
		"unpadded base64":      hdr + `{"t":1,"k":"out","d":"YQ"}` + "\n" + end,
		"out without data":     hdr + `{"t":1,"k":"out"}` + "\n" + end,
		"out with status":      hdr + `{"t":1,"k":"out","d":"YQ==","status":0}` + "\n" + end,
		"empty data":           hdr + `{"t":1,"k":"out","d":""}` + "\n" + end,
		"resize without rows":  hdr + `{"t":1,"k":"resize","cols":4}` + "\n" + end,
		"resize too large":     hdr + `{"t":1,"k":"resize","cols":501,"rows":4}` + "\n" + end,
		"end with size":        hdr + `{"t":1,"k":"end","cols":4,"rows":4}` + "\n",
		"end with data":        hdr + `{"t":1,"k":"end","status":0,"d":"YQ=="}` + "\n",
		"status too large":     hdr + `{"t":1,"k":"end","status":256}` + "\n",
		"no end":               hdr + `{"t":1,"k":"out","d":"YQ=="}` + "\n",
		"end twice":            hdr + end + end,
		"records out of order": hdr + `{"t":9,"k":"out","d":"YQ=="}` + "\n" + end,
		"header size too big":  strings.Replace(hdr+end, `"cols":10`, `"cols":501`, 1),
		"records only":         end,
	}
	// The text must be the one Encode writes: JSON that means the same
	// recording in another spelling is not accepted.
	canonical := map[string]string{
		"upper case key":       hdr + `{"T":5,"k":"end","status":0}` + "\n",
		"mixed case key":       hdr + `{"t":5,"K":"end","Status":0}` + "\n",
		"upper case header":    strings.Replace(hdr, `"cols"`, `"COLS"`, 1) + end,
		"upper case format":    strings.Replace(hdr, `"format"`, `"FORMAT"`, 1) + end,
		"repeated key":         hdr + `{"t":900,"t":5,"k":"end","status":0}` + "\n",
		"repeated header key":  strings.Replace(hdr, `"cols":10`, `"cols":9,"cols":10`, 1) + end,
		"CRLF":                 strings.ReplaceAll(hdr+end, "\n", "\r\n"),
		"leading spaces":       hdr + "  " + end,
		"space after colon":    hdr + `{"t": 5,"k":"end","status":0}` + "\n",
		"trailing space":       hdr + `{"t":5,"k":"end","status":0} ` + "\n",
		"null status":          hdr + `{"t":5,"k":"end","status":null}` + "\n",
		"key order":            hdr + `{"k":"end","t":5,"status":0}` + "\n",
		"escaped key":          hdr + `{"\u0074":5,"k":"end","status":0}` + "\n",
		"leading spaces first": "  " + hdr + end,
	}
	for name, text := range canonical {
		cases[name] = text
	}
	for name, text := range cases {
		if _, err := sessionreplay.Parse([]byte(text)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := sessionreplay.Parse([]byte(hdr + end)); err != nil {
		t.Fatal(err)
	}
}

func TestParseErrorNamesTheLine(t *testing.T) {
	text := `{"format":"ataniq-session-replay","version":1,"cols":10,"rows":5}` + "\n" +
		`{"t":1,"k":"nope"}` + "\n"
	_, err := sessionreplay.Parse([]byte(text))
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("got %v", err)
	}
}

func TestParseNamesTheLineThatIsNotCanonical(t *testing.T) {
	hdr := `{"format":"ataniq-session-replay","version":1,"cols":10,"rows":5}` + "\n"
	_, err := sessionreplay.Parse([]byte(hdr + `{"t":1,"k":"out","d":"YQ=="}` + "\n" + `{"T":5,"k":"end","status":0}` + "\n"))
	if err == nil || !strings.Contains(err.Error(), "line 3") {
		t.Fatalf("got %v", err)
	}
}

func TestParseRefusesNewlinesBeforeSplittingThem(t *testing.T) {
	data := bytes.Repeat([]byte{'\n'}, sessionreplay.MaxBytes)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := sessionreplay.Parse(data)
	runtime.ReadMemStats(&after)
	if err == nil {
		t.Fatal("accepted")
	}
	if used := after.TotalAlloc - before.TotalAlloc; used > 1<<20 {
		t.Fatalf("refusing %d newlines allocated %d bytes", len(data), used)
	}
}

func TestParseSizeLimit(t *testing.T) {
	if _, err := sessionreplay.Parse(make([]byte, sessionreplay.MaxBytes+1)); err == nil {
		t.Fatal("accepted")
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte(sampleText))
	f.Add([]byte(""))
	f.Add([]byte("{}\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := sessionreplay.Parse(data)
		if err != nil {
			return
		}
		enc, err := sessionreplay.Encode(r)
		if err != nil {
			t.Fatalf("parsed recording does not encode: %v", err)
		}
		back, err := sessionreplay.Parse(enc)
		if err != nil || !reflect.DeepEqual(back, r) {
			t.Fatalf("no round trip: %v", err)
		}
	})
}
