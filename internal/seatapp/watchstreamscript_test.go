package seatapp

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The tests of the stream's half of /static/watch.js. Like the poll's, they
// read the script's text, as the repository has no JavaScript engine: what
// it must hold, what it must not, and that it stays in step with
// watchstream.go.

// funcBody returns the text of the script's function name, from its
// declaration to the next function declaration at the same indent.
func funcBody(t *testing.T, js, name string) string {
	t.Helper()
	re := regexp.MustCompile(`(?s)\n  (?:async )?function ` + name + `\(.*?\n  \}\n`)
	m := re.FindString(js)
	if m == "" {
		t.Fatalf("watch.js has no function %s", name)
	}
	return m
}

// TestSeatWatchScriptStreamMatchesTheSeat: the script's address, kind of
// event and bound of an event are the seat's, and it reads the stream with
// fetch and a reader, never a browser's built-in stream client.
func TestSeatWatchScriptStreamMatchesTheSeat(t *testing.T) {
	js := watchScript(t)
	for _, want := range []string{
		`var STREAM_PATH = "` + streamPath + `";`,
		`var STREAM_KIND = "` + streamKindCards + `";`,
		"var STREAM_EVENT_MAX = " + strconv.Itoa(streamEventMax) + ";",
		"resp.body.getReader()", "new TextDecoder()", "decoder.decode(chunk.value, { stream: true })",
		`type !== "text/event-stream"`, "resp.status !== 200 || resp.redirected",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("watch.js lacks %q", want)
		}
	}
	// One fetch, shared by the poll and the stream, with the checks of the
	// app's own requests.
	if n := strings.Count(js, "fetch("); n != 1 {
		t.Errorf("watch.js calls fetch %d times", n)
	}
	send := funcBody(t, js, "send")
	for _, want := range []string{`method: "POST"`, `credentials: "same-origin"`, `redirect: "error"`, `cache: "no-store"`, "signal: ctl.signal"} {
		if !strings.Contains(send, want) {
			t.Errorf("send lacks %q", want)
		}
	}
	for _, want := range []string{"send(POLL_PATH, body, ctl)", "send(STREAM_PATH, { version: version }, c.ctl)"} {
		if !strings.Contains(js, want) {
			t.Errorf("watch.js lacks %q", want)
		}
	}
	// The stream's body is the version alone: the seat's struct has the one
	// key.
	if strings.Count(js, "send(STREAM_PATH") != 1 {
		t.Error("the stream is opened from more than one place")
	}
}

// TestSeatWatchScriptStreamIsBounded: a line and an event's data have their
// bounds, a silence ends the stream, and only the one kind of event, through
// hinted, reaches the page; an unknown kind or field is skipped.
func TestSeatWatchScriptStreamIsBounded(t *testing.T) {
	js := watchScript(t)
	read := funcBody(t, js, "readStream")
	for _, want := range []string{
		"line.length > STREAM_LINE_MAX", "rest.length > STREAM_LINE_MAX", "evData.length > STREAM_EVENT_MAX",
		"setTimeout(function () { c.ctl.abort(); }, STREAM_SILENCE)",
		"if (kind === STREAM_KIND) { hinted(evData); }",
		`if (line.charAt(0) === ":") { continue; }`,
		`if (field === "event")`, `} else if (field === "data") {`,
	} {
		if !strings.Contains(read, want) {
			t.Errorf("readStream lacks %q", want)
		}
	}
	m := regexp.MustCompile(`STREAM_LINE_MAX = (\d+);`).FindStringSubmatch(js)
	if m == nil {
		t.Fatal("watch.js has no STREAM_LINE_MAX")
	}
	if n, _ := strconv.Atoi(m[1]); n < streamEventMax || n > 4096 {
		t.Errorf("a line may be %d characters", n)
	}
	// The seat writes a heartbeat every streamBeat; the silence that fails a
	// stream is longer, and shorter than the poll's cut.
	m = regexp.MustCompile(`STREAM_SILENCE = (\d+);`).FindStringSubmatch(js)
	if m == nil {
		t.Fatal("watch.js has no STREAM_SILENCE")
	}
	if n, _ := strconv.Atoi(m[1]); n <= int(streamBeat.Milliseconds()) || n > 30000 {
		t.Errorf("the silence is %d ms", n)
	}
	// hinted reads the version of the hint and nothing else, and puts
	// nothing of it on the page.
	h := funcBody(t, js, "hinted")
	for _, mm := range regexp.MustCompile(`\bhint\.([A-Za-z_]+)`).FindAllStringSubmatch(h, -1) {
		if mm[1] != "version" {
			t.Errorf("hinted reads hint.%s", mm[1])
		}
	}
	for _, bad := range []string{"textContent", "say(", "createTextNode", "location.reload", "dataset"} {
		if strings.Contains(h, bad) {
			t.Errorf("hinted holds %q", bad)
		}
	}
	if !strings.Contains(h, "validVersion(hint.version)") || !strings.Contains(h, "JSON.parse(text)") {
		t.Error("hinted does not check the version it takes")
	}
	if strings.Count(js, "hinted(") != 2 { // its definition and its one call
		t.Errorf("hinted appears %d times", strings.Count(js, "hinted("))
	}
}

// TestSeatWatchScriptStreamFallsBack: only the card list streams; a browser
// that cannot read a stream, and any failure of one, takes the poll, which
// is unchanged; the poll's answer is dropped once the page is back on the
// stream.
func TestSeatWatchScriptStreamFallsBack(t *testing.T) {
	js := watchScript(t)
	for _, want := range []string{
		"var streamable = live && !onCard && !still && canStream();",
		"var useStream = streamable;",
		`typeof window.ReadableStream === "function"`, `typeof window.TextDecoder === "function"`,
		"if (useStream) { stream(); } else { poll(); }",
		"if (stopped || useStream) { return; }",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("watch.js lacks %q", want)
		}
	}
	st := funcBody(t, js, "stream")
	for _, want := range []string{"catch (e) { failed = true; }", "fallBack();", "schedule(reopenWait());", "shortEnds >= 2", "if (c.closed || stopped) { return; }"} {
		if !strings.Contains(st, want) {
			t.Errorf("stream lacks %q", want)
		}
	}
	fb := funcBody(t, js, "fallBack")
	if !strings.Contains(fb, "useStream = false;") || !strings.Contains(fb, "poll();") {
		t.Errorf("fallBack does not turn to the poll: %s", fb)
	}
	// The reopen wait doubles with the failures, is bounded and is spread.
	rw := funcBody(t, js, "reopenWait")
	for _, want := range []string{"REOPEN_FIRST * Math.pow(2", "REOPEN_LAST", "Math.random() * REOPEN_JITTER"} {
		if !strings.Contains(rw, want) {
			t.Errorf("reopenWait lacks %q", want)
		}
	}
	// A stream that ends is reopened no sooner than a second.
	if !strings.Contains(js, "var REOPEN_FIRST = 1000;") || !strings.Contains(js, "var REOPEN_LAST = 30000;") {
		t.Error("the reopen waits moved")
	}
}

// TestSeatWatchScriptStreamFollowsThePage: a hidden or left page closes its
// stream, a page shown again, restored from history or back online opens
// one after a random wait, and one connection is kept per page.
func TestSeatWatchScriptStreamFollowsThePage(t *testing.T) {
	js := watchScript(t)
	for _, want := range []string{
		`document.addEventListener("visibilitychange"`,
		`window.addEventListener("pagehide", function () { asleep(); });`,
		`window.addEventListener("pageshow", function (ev) { if (ev.persisted) { awake(); } });`,
		`window.addEventListener("online", function () { awake(); });`,
		"if (document.hidden) { asleep(); } else { awake(); }",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("watch.js lacks %q", want)
		}
	}
	as := funcBody(t, js, "asleep")
	if !strings.Contains(as, "closeStream();") || !strings.Contains(as, "cancel();") {
		t.Errorf("asleep does not close the stream: %s", as)
	}
	aw := funcBody(t, js, "awake")
	for _, want := range []string{"if (stopped || document.hidden) { return; }", "pollCtl.abort()", "if (conn === null) { schedule(reopenWait()); }"} {
		if !strings.Contains(aw, want) {
			t.Errorf("awake lacks %q", want)
		}
	}
	// One stream at a time.
	if !strings.Contains(funcBody(t, js, "stream"), "if (stopped || !useStream || conn !== null || document.hidden) { return; }") {
		t.Error("stream may open a second connection")
	}
}
