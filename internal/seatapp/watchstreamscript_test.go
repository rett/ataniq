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
		`var STREAM_KIND_CHAT = "` + streamKindChat + `";`,
		"var STREAM_EVENT_MAX = " + strconv.Itoa(streamEventMax) + ";",
		"resp.body.getReader()", "new TextDecoder()", "decoder.decode(bytes.subarray(at, at + STREAM_SLICE), { stream: true })",
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
	// The stream carries the version the page shows, never one a hint
	// brought: the seat counts it as what the owner has seen.
	for _, want := range []string{"send(POLL_PATH, body, ctl)", "send(STREAM_PATH, onLeads ? { leads: renderedChat } : { version: rendered }, c.ctl)",
		"var rendered = version;"} {
		if !strings.Contains(js, want) {
			t.Errorf("watch.js lacks %q", want)
		}
	}
	// The stream's body is the version alone: the seat's struct has the one
	// key.
	if strings.Count(js, "send(STREAM_PATH") != 1 {
		t.Error("the stream is opened from more than one place")
	}
	if regexp.MustCompile(`\brendered\s*=[^=]`).FindAllStringIndex(js, -1) == nil ||
		len(regexp.MustCompile(`\brendered\s*=[^=]`).FindAllStringIndex(js, -1)) != 1 {
		t.Error("the rendered version is set other than once, at the page's load")
	}
}

// TestSeatWatchScriptStreamIsBounded: the body is decoded a bounded slice
// at a time, and a line (before it is cut out) and an event's data are
// checked against their bounds as each slice is taken; the opening
// heartbeat must come whole within a first deadline, below the rotation,
// and an open stream must not fall silent; only the one kind of event,
// through hinted, reaches the page, and a hint that cannot be read fails
// the stream; an unknown kind or field is skipped; a stream that ends
// inside an event, or before its opening heartbeat, fails.
func TestSeatWatchScriptStreamIsBounded(t *testing.T) {
	js := watchScript(t)
	read := funcBody(t, js, "readStream")
	for _, want := range []string{
		"for (var at = 0; at < bytes.length; at += STREAM_SLICE) {",
		"if (nl > STREAM_LINE_MAX) { throw { stream: false }; }\n            var line = rest.slice(0, nl);",
		"rest.length > STREAM_LINE_MAX", "evData.length > STREAM_EVENT_MAX",
		"silence = setTimeout(function () { c.ctl.abort(); }, ms);",
		"hush(STREAM_FIRST);", "if (opened) { hush(STREAM_SILENCE); }",
		"if (kind === streamKind && !(onLeads ? chatHinted(evData) : hinted(evData))) { throw { stream: false }; }",
		`if (line.charAt(0) === ":") { continue; }`,
		`if (field === "event")`, `} else if (field === "data") {`,
		"rest += decoder.decode();",
		`if (!opened || rest !== "" || kind !== "" || evData !== "") { throw { stream: false }; }`,
	} {
		if !strings.Contains(read, want) {
			t.Errorf("readStream lacks %q", want)
		}
	}
	// The whole chunk is never decoded at once.
	if strings.Contains(read, "decoder.decode(chunk.value") {
		t.Error("readStream decodes a whole chunk")
	}
	// The first deadline is below the rotation, so a proxy that holds the
	// response back to its end has the page on the poll within a rotation;
	// a slice is no larger than a line's bound.
	m := regexp.MustCompile(`STREAM_FIRST = (\d+);`).FindStringSubmatch(js)
	if m == nil {
		t.Fatal("watch.js has no STREAM_FIRST")
	}
	if n, _ := strconv.Atoi(m[1]); n <= 1000 || n >= int((streamLife-streamFinish).Milliseconds()) {
		t.Errorf("the first deadline is %d ms", n)
	}
	m = regexp.MustCompile(`STREAM_SLICE = (\d+);`).FindStringSubmatch(js)
	if m == nil {
		t.Fatal("watch.js has no STREAM_SLICE")
	}
	if n, _ := strconv.Atoi(m[1]); n <= 0 || n > 512 {
		t.Errorf("a slice is %d bytes", n)
	}
	m = regexp.MustCompile(`STREAM_LINE_MAX = (\d+);`).FindStringSubmatch(js)
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
	for _, want := range []string{
		"validVersion(hint.version)", "JSON.parse(text)", "catch (e) { return false; }",
		"Object.keys(hint).length !== 1", `hint.version === ""`,
	} {
		if !strings.Contains(h, want) {
			t.Errorf("hinted does not check the hint it takes: no %q", want)
		}
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
	for _, want := range []string{"closeStream();", "cancel();", "cancelReload();"} {
		if !strings.Contains(as, want) {
			t.Errorf("asleep lacks %q: %s", want, as)
		}
	}
	// A reload waiting its turn is dropped on hide, pagehide and a session's
	// end, and looks at the page again when its timer runs; the change is
	// kept pending for the page's return.
	if !strings.Contains(funcBody(t, js, "cancelReload"), "clearTimeout(reloadTimer); reloadTimer = null;") {
		t.Error("cancelReload does not drop the reload")
	}
	if !regexp.MustCompile(`(?s)case "ended":\s*stopped = true;\s*cancel\(\);\s*cancelReload\(\);`).MatchString(js) {
		t.Error("an ended session leaves a reload waiting")
	}
	wr := funcBody(t, js, "wantReload")
	for _, want := range []string{
		"pendingChange = true;",
		"if (reloadTimer !== null || stopped || document.hidden) { return; }",
		"reloadTimer = null;\n      if (stopped || document.hidden) { return; }\n      if (holding())",
	} {
		if !strings.Contains(wr, want) {
			t.Errorf("wantReload lacks %q", want)
		}
	}
	if strings.Count(js, "pendingChange = ") != 3 { // its declaration, wantReload and chatChanged
		t.Error("the pending change is set or cleared elsewhere")
	}
	if !strings.Contains(funcBody(t, js, "chatChanged"), "pendingChange = true;\n    if (stopped || document.hidden) { return; }") {
		t.Error("a Leads page acts on a change while hidden")
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
