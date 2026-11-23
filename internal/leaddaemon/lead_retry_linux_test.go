//go:build linux && ataniq_canary

package leaddaemon

import (
	"context"
	"net"
	"os"
	"strings"
	"sync"
	"testing"

	"ataniq/internal/leadholder/wire"
	"ataniq/internal/leadwire"
)

// The tests of the exceptions to a spent message in scratch mode: a message
// that was not delivered (the holder answered not-now or refused with nothing
// written, or refused the request itself as disabled, wrong-epoch, busy or
// bad-request) leaves the epoch's message free, and anything that may have
// reached the lead leaves it spent, in this daemon and in the next.

// injectAnswer is the holder's answer to one inject request.
type injectAnswer func(conn net.Conn)

func okResult(r wire.InjectResult) injectAnswer {
	return func(conn net.Conn) { writeResponse(conn, wire.Response{OK: true, Inject: &r}) }
}

var (
	answerNotNow   = okResult(wire.InjectResult{Outcome: wire.InjectOutcomeNotNow, Reason: wire.InjectReasonNotReady, Written: wire.InjectWrittenNothing})
	answerBlocked  = okResult(wire.InjectResult{Outcome: wire.InjectOutcomeNotNow, Reason: wire.InjectReasonBlocked, Written: wire.InjectWrittenNothing})
	answerUntested = okResult(wire.InjectResult{Outcome: wire.InjectOutcomeRefused, Reason: wire.InjectReasonUntested, Written: wire.InjectWrittenNothing})
	answerText     = okResult(wire.InjectResult{Outcome: wire.InjectOutcomeRefused, Reason: wire.InjectReasonText, Written: wire.InjectWrittenNothing})
	answerSubmit   = okResult(wire.InjectResult{Outcome: wire.InjectOutcomeSubmitted, Written: wire.InjectWrittenWhole})
)

// answersInOrder answers the inject requests with the answers in turn, the last
// one for every request after them, and every other request as submitted does.
func answersInOrder(answers ...injectAnswer) holderAnswer {
	var mu sync.Mutex
	n := 0
	return func(req wire.Request, conn net.Conn) {
		if req.Op != wire.OpInject {
			submitted(req, conn)
			return
		}
		mu.Lock()
		i := min(n, len(answers)-1)
		n++
		mu.Unlock()
		answers[i](conn)
	}
}

func injectFrames(h *fakeHolder) int { return len(h.requests(wire.OpInject)) }

// A message that was not delivered does not spend the epoch's message: another
// is admitted and sent, with the same id or another, as often as the holder
// answers not-now or refused. The message that is delivered spends it.
func TestScratchLeadMayMessageAgainAfterANotDeliveredMessage(t *testing.T) {
	l := newLeadEnv(t)
	l.start()
	sr := l.up("alpha", answersInOrder(answerNotNow, answerBlocked, answerUntested, answerText, answerText, answerSubmit))
	h := l.holders["alpha"]

	for i, c := range []struct {
		id    string
		words leadwire.MessageResult
	}{
		{"msg-one", leadwire.MessageResult{Outcome: "not-now", Reason: "not-ready", Written: "nothing"}},
		{"msg-two", leadwire.MessageResult{Outcome: "not-now", Reason: "blocked", Written: "nothing"}},
		{"msg-three", leadwire.MessageResult{Outcome: "refused", Reason: "untested", Written: "nothing"}},
		{"msg-four", leadwire.MessageResult{Outcome: "refused", Reason: "text", Written: "nothing"}},
		// The id of an earlier message is no reason for a refusal.
		{"msg-four", leadwire.MessageResult{Outcome: "refused", Reason: "text", Written: "nothing"}},
	} {
		want(t, l.inject("alpha", sr.Epoch, c.id, "body text"), leadwire.StatusDone, "")
		row := l.row("alpha")
		if row.MessageSpent || row.MessageResult != c.words {
			t.Fatalf("message %d: row %+v", i, row)
		}
		if n := injectFrames(h); n != i+1 {
			t.Fatalf("message %d: %d frames reached the holder", i, n)
		}
	}
	want(t, l.inject("alpha", sr.Epoch, "msg-six", "body text"), leadwire.StatusDone, "")
	if row := l.row("alpha"); !row.MessageSpent || row.MessageResult != (leadwire.MessageResult{Outcome: "submitted", Written: "whole"}) {
		t.Fatalf("after the delivered message: %+v", row)
	}
	want(t, l.inject("alpha", sr.Epoch, "msg-seven", "body text"), leadwire.StatusRefused, leadwire.CodeSecondMessage)
	if n := injectFrames(h); n != 6 {
		t.Fatalf("%d frames reached the holder", n)
	}
	// Every message left a sent line and then a result line.
	var kinds []string
	for _, ev := range l.evidenceOf("alpha") {
		if ev == "inject-sent" || ev == "inject-result" {
			kinds = append(kinds, ev)
		}
	}
	if got := strings.Join(kinds, " "); got != strings.TrimSpace(strings.Repeat("inject-sent inject-result ", 6)) {
		t.Fatalf("evidence: %s", got)
	}
}

// Whatever may have reached the lead keeps the epoch's message spent, and the
// next message is refused as the second one without reaching the holder.
func TestScratchLeadMessageStaysSpentWhenItMayHaveBeenDelivered(t *testing.T) {
	garbage := func(conn net.Conn) { _, _ = conn.Write([]byte("not a frame")) }
	for name, a := range map[string]injectAnswer{
		"submitted": answerSubmit,
		"uncertain, not submitted": okResult(wire.InjectResult{
			Outcome: wire.InjectOutcomeUncertain, Reason: wire.InjectReasonNotSubmitted, Written: wire.InjectWrittenPartial}),
		"uncertain, died, nothing written": okResult(wire.InjectResult{
			Outcome: wire.InjectOutcomeUncertain, Reason: wire.InjectReasonDied, Written: wire.InjectWrittenNothing}),
		"uncertain, died, unknown": okResult(wire.InjectResult{
			Outcome: wire.InjectOutcomeUncertain, Reason: wire.InjectReasonDied, Written: wire.InjectWrittenUnknown}),
		"an ok answer without a result":           func(conn net.Conn) { writeResponse(conn, wire.Response{OK: true}) },
		"a refusal code the daemon does not know": func(conn net.Conn) { writeResponse(conn, wire.Response{OK: false, Code: "no-such-code"}) },
		"an answer that does not decode":          garbage,
		"no answer":                               func(conn net.Conn) { silent(wire.Request{}, conn) },
		"a closed connection":                     func(conn net.Conn) { conn.Close() },
	} {
		t.Run(name, func(t *testing.T) {
			l := newLeadEnv(t)
			l.start()
			var first injectAnswer = a
			sr := l.up("alpha", answersInOrder(first, answerSubmit))
			h := l.holders["alpha"]
			r := l.inject("alpha", sr.Epoch, "msg-one", "body text")
			if r.Status == leadwire.StatusRefused {
				t.Fatalf("the message was refused: %+v", r)
			}
			if row := l.row("alpha"); !row.MessageSpent {
				t.Fatalf("the message is not shown spent: %+v", row)
			}
			want(t, l.inject("alpha", sr.Epoch, "msg-two", "body text"), leadwire.StatusRefused, leadwire.CodeSecondMessage)
			if n := injectFrames(h); n != 1 {
				t.Fatalf("%d frames reached the holder", n)
			}
			// A restart does not free it.
			l.restart()
			if row := l.row("alpha"); !row.MessageSpent {
				t.Fatalf("after the restart: %+v", row)
			}
			want(t, l.inject("alpha", sr.Epoch, "msg-three", "body text"), leadwire.StatusRefused, leadwire.CodeSecondMessage)
			if n := injectFrames(h); n != 1 {
				t.Fatalf("%d frames reached the holder after the restart", n)
			}
		})
	}
}

// A not-delivered message is free in the next daemon too, because the evidence
// has its result line; a second message is admitted after the restart and the
// epoch is spent once that one is delivered, for the daemon after that.
func TestScratchLeadNotDeliveredMessageIsFreeAfterARestart(t *testing.T) {
	l := newLeadEnv(t)
	l.start()
	sr := l.up("alpha", answersInOrder(answerNotNow, answerSubmit))
	h := l.holders["alpha"]
	want(t, l.inject("alpha", sr.Epoch, "msg-one", "body text"), leadwire.StatusDone, "")

	l.restart()
	if row := l.row("alpha"); row.MessageSpent || row.Epoch != sr.Epoch {
		t.Fatalf("after the restart: %+v", row)
	}
	if !strings.Contains(l.log.String(), "was not delivered") {
		t.Fatalf("the log does not say the message was not delivered:\n%s", l.log)
	}
	want(t, l.inject("alpha", sr.Epoch, "msg-two", "body text"), leadwire.StatusDone, "")
	if n := injectFrames(h); n != 2 {
		t.Fatalf("%d frames reached the holder", n)
	}
	l.restart()
	if row := l.row("alpha"); !row.MessageSpent {
		t.Fatalf("after the second restart: %+v", row)
	}
	want(t, l.inject("alpha", sr.Epoch, "msg-three", "body text"), leadwire.StatusRefused, leadwire.CodeSecondMessage)
	if n := injectFrames(h); n != 2 {
		t.Fatalf("%d frames reached the holder", n)
	}
}

// appendEvidence adds lines to the evidence of the lead while the daemon is
// stopped, as a daemon that was killed at that point would have left them.
func appendEvidence(t *testing.T, l *leadEnv, key string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(l.cfgOf(key).EvidencePath(), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, line := range lines {
		if _, err := f.WriteString(line + "\n"); err != nil {
			t.Fatal(err)
		}
	}
}

// What the evidence shows to the next daemon decides: a sent line that the kill
// left without its result line keeps the epoch spent, and so does every
// disorder of the lines (the result of no message, a result twice, a result that
// is no release), while a complete not-delivered message leaves it free.
func TestScratchLeadEvidenceDecidesWhatARestartAdmits(t *testing.T) {
	role := roleOf("alpha")
	sent := func(id string) string {
		return evidenceLineOf("inject-sent", role, `,"epoch":1,"message_id":"`+id+`"`)
	}
	result := func(id, outcome, reason, written string) string {
		more := `,"epoch":1,"message_id":"` + id + `","outcome":"` + outcome + `"`
		if reason != "" {
			more += `,"reason":"` + reason + `"`
		}
		return evidenceLineOf("inject-result", role, more+`,"written":"`+written+`"`)
	}
	notNow := func(id string) string { return result(id, "not-now", "not-ready", "nothing") }
	for name, tc := range map[string]struct {
		lines []string
		spent bool
	}{
		"sent and a not-now result":             {[]string{sent("m-1"), notNow("m-1")}, false},
		"sent, not-now, sent again, not-now":    {[]string{sent("m-1"), notNow("m-1"), sent("m-2"), notNow("m-2")}, false},
		"killed after the sent line":            {[]string{sent("m-1")}, true},
		"not-now, then killed after the sent":   {[]string{sent("m-1"), notNow("m-1"), sent("m-2")}, true},
		"a result of no sent message":           {[]string{notNow("m-1")}, true},
		"a result of another message":           {[]string{sent("m-1"), notNow("m-2")}, true},
		"a result twice":                        {[]string{sent("m-1"), notNow("m-1"), notNow("m-1")}, true},
		"the sent line twice":                   {[]string{sent("m-1"), sent("m-1"), notNow("m-1")}, true},
		"a submitted result":                    {[]string{sent("m-1"), result("m-1", "submitted", "", "whole")}, true},
		"not-now, then a submitted message":     {[]string{sent("m-1"), notNow("m-1"), sent("m-2"), result("m-2", "submitted", "", "whole")}, true},
		"a died result with nothing written":    {[]string{sent("m-1"), result("m-1", "uncertain", "died", "nothing")}, true},
		"a result that the wire does not carry": {[]string{sent("m-1"), result("m-1", "not-now", "not-ready", "whole")}, true},
	} {
		t.Run(name, func(t *testing.T) {
			l := newLeadEnv(t)
			l.start()
			sr := l.up("alpha", answersInOrder(answerSubmit))
			h := l.holders["alpha"]
			if sr.Epoch != 1 {
				t.Fatalf("epoch %d", sr.Epoch)
			}
			if err := l.running.stop(); err != nil {
				t.Fatal(err)
			}
			appendEvidence(t, l, "alpha", tc.lines...)
			l.start()
			if row := l.row("alpha"); row.MessageSpent != tc.spent {
				t.Fatalf("spent %v, want %v: %+v", row.MessageSpent, tc.spent, row)
			}
			if tc.spent {
				want(t, l.inject("alpha", sr.Epoch, "msg-new", "body text"), leadwire.StatusRefused, leadwire.CodeSecondMessage)
				if n := injectFrames(h); n != 0 {
					t.Fatalf("%d frames reached the holder", n)
				}
				return
			}
			want(t, l.inject("alpha", sr.Epoch, "msg-new", "body text"), leadwire.StatusDone, "")
			if n := injectFrames(h); n != 1 {
				t.Fatalf("%d frames reached the holder", n)
			}
		})
	}
}

// A line of the lead's evidence that cannot be read refuses the start of the
// daemon in scratch mode as it does in canary mode.
func TestScratchLeadDamagedEvidenceRefusesTheStart(t *testing.T) {
	l := newLeadEnv(t)
	l.start()
	l.up("alpha", answersInOrder(answerSubmit))
	if err := l.running.stop(); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(l.cfgOf("alpha").EvidencePath(), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"event":"inject-resu`); err != nil {
		t.Fatal(err)
	}
	f.Close()
	cfg := l.daemonConfig()
	err = Run(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "does not end with a newline") {
		t.Fatalf("the daemon did not refuse the start: %v", err)
	}
}

// The canary keeps its rule: the not-now answer spends the epoch's message, in
// the instance and in the daemon that reads the evidence back, and the same
// answer to an instance of scratch mode (the flag the router sets for a lead)
// does not.
func TestCanaryNotNowStillSpendsTheMessageAndScratchDoesNot(t *testing.T) {
	ctx := context.Background()
	for _, scratch := range []bool{false, true} {
		f := newExtFixture(t, answersInOrder(answerNotNow, answerSubmit), "")
		f.x.scratch = scratch
		res, fl := f.x.injectFor(ctx, CanaryInjectParams{MessageID: "msg-one", Body: "body text"}, f.epoch)
		if fl != nil || res.Result.Outcome != wire.InjectOutcomeNotNow {
			t.Fatalf("scratch %v: %+v %+v", scratch, res, fl)
		}
		if got := f.x.messageSpent(f.epoch); got == scratch {
			t.Fatalf("scratch %v: spent %v", scratch, got)
		}
		_, fl = f.x.injectFor(ctx, CanaryInjectParams{MessageID: "msg-two", Body: "body text"}, f.epoch)
		if scratch {
			if fl != nil {
				t.Fatalf("scratch: the second message was refused: %+v", fl)
			}
			if !f.x.messageSpent(f.epoch) {
				t.Fatal("scratch: the delivered message did not spend the epoch")
			}
		} else if fl == nil || fl.code != "second-message" {
			t.Fatalf("canary: the second message: %+v", fl)
		}
		want := 2
		if !scratch {
			want = 1
		}
		if n := len(f.holder.requests(wire.OpInject)); n != want {
			t.Fatalf("scratch %v: %d inject frames, want %d", scratch, n, want)
		}
	}
}

// A result line that could not be written leaves the message spent, whatever
// the answer was: the evidence cannot show the message as free, and the daemon
// does not show it free either.
func TestScratchNotDeliveredMessageStaysSpentWhenItsResultCannotBeWritten(t *testing.T) {
	f := newExtFixture(t, answersInOrder(answerNotNow), "")
	f.x.scratch = true
	// An evidence that is closed takes no line, the sent line and the result
	// line alike; the message is on its way all the same.
	if err := f.x.ev.Close(); err != nil {
		t.Fatal(err)
	}
	res, fl := f.x.injectFor(context.Background(), CanaryInjectParams{MessageID: "msg-one", Body: "body text"}, f.epoch)
	if fl != nil || res.Result.Outcome != wire.InjectOutcomeNotNow {
		t.Fatalf("%+v %+v", res, fl)
	}
	if !f.x.messageSpent(f.epoch) {
		t.Fatal("a message whose result line is not written is shown free")
	}
}

// The canary daemon reads its evidence with the canary's reader: a not-now
// answer spends the epoch's message across a restart, and the second message
// never reaches the holder.
func TestCanaryNotNowMessageStaysSpentAcrossARestart(t *testing.T) {
	c := newCanaryEnv(t)
	started := c.up(notNowHolder)
	got, err := c.inject("msg-one", "body text\n")
	if err != nil || got.Result.Outcome != wire.InjectOutcomeNotNow {
		t.Fatalf("%+v %v", got, err)
	}
	_, err = c.inject("msg-two", "body text\n")
	if ce := wantCode(t, err, CodeRefused); ce.Message != msgSecond {
		t.Fatalf("message %q", ce.Message)
	}
	c.restart()
	if _, _, epoch, found := c.shown(); !found || epoch != started.Epoch {
		t.Fatalf("after the restart: epoch %d (found %v)", epoch, found)
	}
	_, err = c.inject("msg-three", "body text\n")
	if ce := wantCode(t, err, CodeRefused); ce.Message != msgSecond {
		t.Fatalf("message %q", ce.Message)
	}
	if n := len(c.holder.requests(wire.OpInject)); n != 1 {
		t.Fatalf("%d inject requests", n)
	}
}

// holderRefusals are the holder's own refusals of an inject request, the ones
// that mean nothing was written to the lead for the message (holderRefusal);
// freeingRefusals are those of them that give the epoch's message back to a
// lead of scratch mode (holderFrees), all but busy.
var (
	holderRefusals  = []string{wire.CodeDisabled, wire.CodeWrongEpoch, wire.CodeBusy, wire.CodeBadRequest}
	freeingRefusals = []string{wire.CodeDisabled, wire.CodeWrongEpoch, wire.CodeBadRequest}
)

// A message the holder refuses itself (disabled, wrong-epoch or bad-request)
// was not delivered and does not spend the epoch's message: the row says free
// and not delivered, whatever words an earlier message left, the lead may send
// another, and a restart of the daemon reads the epoch as free too, from the
// inject-refused line. The answer to the lead is the refusal's.
func TestScratchLeadHolderRefusalLeavesTheEpochsMessageFree(t *testing.T) {
	for _, code := range freeingRefusals {
		t.Run(code, func(t *testing.T) {
			l := newLeadEnv(t)
			l.start()
			refuse := func(conn net.Conn) { writeResponse(conn, wire.Response{OK: false, Code: code}) }
			// The first message is not delivered with words of its own, which
			// the refusal of the second replaces.
			sr := l.up("alpha", answersInOrder(answerNotNow, refuse, refuse, answerSubmit))
			h := l.holders["alpha"]
			want(t, l.inject("alpha", sr.Epoch, "msg-one", "body text"), leadwire.StatusDone, "")
			if row := l.row("alpha"); row.MessageSpent || row.MessageResult.Reason != "not-ready" {
				t.Fatalf("after the not-now message: %+v", row)
			}
			wantCode, wantStatus := "holder-"+code, leadwire.StatusUnavailable
			if code == wire.CodeWrongEpoch {
				wantCode, wantStatus = codeStaleEpoch, leadwire.StatusRefused
			}
			want(t, l.inject("alpha", sr.Epoch, "msg-two", "body text"), wantStatus, wantCode)
			notDelivered := leadwire.MessageResult{Outcome: "refused"}
			if row := l.row("alpha"); row.MessageSpent || row.MessageResult != notDelivered {
				t.Fatalf("after the refusal: %+v", row)
			}
			// Free in the next daemon too: the evidence has the refused line.
			l.restart()
			if row := l.row("alpha"); row.MessageSpent {
				t.Fatalf("after the restart: %+v", row)
			}
			want(t, l.inject("alpha", sr.Epoch, "msg-two", "body text"), wantStatus, wantCode)
			want(t, l.inject("alpha", sr.Epoch, "msg-three", "body text"), leadwire.StatusDone, "")
			if row := l.row("alpha"); !row.MessageSpent || row.MessageResult.Outcome != "submitted" {
				t.Fatalf("after the delivered message: %+v", row)
			}
			want(t, l.inject("alpha", sr.Epoch, "msg-four", "body text"), leadwire.StatusRefused, leadwire.CodeSecondMessage)
			if n := injectFrames(h); n != 4 {
				t.Fatalf("%d frames reached the holder, want 4", n)
			}
			if !strings.Contains(l.log.String(), "was refused by the holder") {
				t.Fatalf("the log does not say the message was refused:\n%s", l.log)
			}
			l.restart()
			if row := l.row("alpha"); !row.MessageSpent {
				t.Fatalf("after the second restart: %+v", row)
			}
		})
	}
}

// What the evidence shows to the next daemon about a holder's refusal: a
// refusal that closes the open message of its id frees the epoch, busy
// excepted, which closes it and leaves it spent; every disorder spends it (the
// refusal of no message, of another message, twice, a message left open by a
// kill after it, or a code that is not one of the holder's four), and the lines
// of a message refused before its frame was written change nothing.
func TestScratchLeadEvidenceOfAHolderRefusalDecidesWhatARestartAdmits(t *testing.T) {
	role := roleOf("alpha")
	sent := func(id string) string {
		return evidenceLineOf("inject-sent", role, `,"epoch":1,"message_id":"`+id+`"`)
	}
	refused := func(id, code string) string {
		return evidenceLineOf("inject-refused", role, `,"epoch":1,"message_id":"`+id+`","code":"`+code+`"`)
	}
	noID := evidenceLineOf("inject-refused", role, `,"epoch":1,"code":"holder-busy"`)
	result := evidenceLineOf("inject-result", role, `,"epoch":1,"message_id":"m-1","outcome":"submitted","written":"whole"`)
	notNow := evidenceLineOf("inject-result", role, `,"epoch":1,"message_id":"m-2","outcome":"not-now","reason":"not-ready","written":"nothing"`)
	for name, tc := range map[string]struct {
		lines []string
		spent bool
	}{
		"sent and holder-busy":                  {[]string{sent("m-1"), refused("m-1", "holder-busy")}, true},
		"sent and holder-disabled":              {[]string{sent("m-1"), refused("m-1", "holder-disabled")}, false},
		"sent and holder-wrong-epoch":           {[]string{sent("m-1"), refused("m-1", "holder-wrong-epoch")}, false},
		"sent and holder-bad-request":           {[]string{sent("m-1"), refused("m-1", "holder-bad-request")}, false},
		"refused twice, each after its sent":    {[]string{sent("m-1"), refused("m-1", "holder-disabled"), sent("m-2"), refused("m-2", "holder-bad-request")}, false},
		"freed, then busy":                      {[]string{sent("m-1"), refused("m-1", "holder-disabled"), sent("m-2"), refused("m-2", "holder-busy")}, true},
		"busy, then freed":                      {[]string{sent("m-1"), refused("m-1", "holder-busy"), sent("m-2"), refused("m-2", "holder-disabled")}, true},
		"busy, then a not-now result":           {[]string{sent("m-1"), refused("m-1", "holder-busy"), sent("m-2"), notNow}, true},
		"refusal, then a delivered message":     {[]string{sent("m-1"), refused("m-1", "holder-busy"), sent("m-2"), result}, true},
		"a refusal of no sent message":          {[]string{refused("m-1", "holder-busy")}, true},
		"a refusal of another message":          {[]string{sent("m-1"), refused("m-2", "holder-busy")}, true},
		"a refusal with no message id":          {[]string{sent("m-1"), noID}, true},
		"a refusal twice":                       {[]string{sent("m-1"), refused("m-1", "holder-busy"), refused("m-1", "holder-busy")}, true},
		"a refusal, then killed after the sent": {[]string{sent("m-1"), refused("m-1", "holder-busy"), sent("m-2")}, true},
		"killed after the sent line":            {[]string{sent("m-1")}, true},
		"a refusal and a result of one message": {[]string{sent("m-1"), refused("m-1", "holder-busy"), result}, true},
		// Not one of the holder's four: the open message stays open.
		"sent and holder-unreachable":           {[]string{sent("m-1"), refused("m-1", "holder-unreachable")}, true},
		"sent and an unknown holder code":       {[]string{sent("m-1"), refused("m-1", "holder-no-such-code")}, true},
		"sent and second-message":               {[]string{sent("m-1"), refused("m-1", "second-message")}, true},
		"sent and the holder's code unprefixed": {[]string{sent("m-1"), refused("m-1", "busy")}, true},
		// A line of a message that never reached the holder, between others.
		"a refusal before any frame":         {[]string{refused("m-1", "inject-off")}, false},
		"a refusal in flight, of another id": {[]string{sent("m-1"), refused("m-2", "message-in-flight"), refused("m-1", "holder-disabled")}, false},
	} {
		t.Run(name, func(t *testing.T) {
			l := newLeadEnv(t)
			l.start()
			sr := l.up("alpha", answersInOrder(answerSubmit))
			h := l.holders["alpha"]
			if err := l.running.stop(); err != nil {
				t.Fatal(err)
			}
			appendEvidence(t, l, "alpha", tc.lines...)
			l.start()
			if row := l.row("alpha"); row.MessageSpent != tc.spent {
				t.Fatalf("spent %v, want %v: %+v", row.MessageSpent, tc.spent, row)
			}
			if tc.spent {
				want(t, l.inject("alpha", sr.Epoch, "msg-new", "body text"), leadwire.StatusRefused, leadwire.CodeSecondMessage)
				if n := injectFrames(h); n != 0 {
					t.Fatalf("%d frames reached the holder", n)
				}
				return
			}
			want(t, l.inject("alpha", sr.Epoch, "msg-new", "body text"), leadwire.StatusDone, "")
			if n := injectFrames(h); n != 1 {
				t.Fatalf("%d frames reached the holder", n)
			}
		})
	}
}

// A holder that answers busy is delivering something the daemon has no record
// of, so the epoch's message stays spent: live, where a second message is
// refused second-message and never reaches the holder, and in the next daemon,
// which reads the evidence's inject-refused line of busy as a close that does
// not free. The row shows it spent with no words of the message (the console
// reads that as sent-unsure), also when an earlier message of the epoch left
// words of a message that was not delivered.
func TestScratchLeadHolderBusyLeavesTheEpochsMessageSpent(t *testing.T) {
	l := newLeadEnv(t)
	l.start()
	busy := func(conn net.Conn) { writeResponse(conn, wire.Response{OK: false, Code: wire.CodeBusy}) }
	sr := l.up("alpha", answersInOrder(answerNotNow, busy, answerSubmit))
	h := l.holders["alpha"]
	want(t, l.inject("alpha", sr.Epoch, "msg-one", "body text"), leadwire.StatusDone, "")
	if row := l.row("alpha"); row.MessageSpent || row.MessageResult.Reason != "not-ready" {
		t.Fatalf("after the not-now message: %+v", row)
	}
	want(t, l.inject("alpha", sr.Epoch, "msg-two", "body text"), leadwire.StatusUnavailable, "holder-busy")
	if row := l.row("alpha"); !row.MessageSpent || row.MessageResult != (leadwire.MessageResult{}) {
		t.Fatalf("after the busy refusal: %+v", row)
	}
	want(t, l.inject("alpha", sr.Epoch, "msg-three", "body text"), leadwire.StatusRefused, leadwire.CodeSecondMessage)
	if n := injectFrames(h); n != 2 {
		t.Fatalf("%d frames reached the holder, want 2", n)
	}
	l.restart()
	if row := l.row("alpha"); !row.MessageSpent {
		t.Fatalf("after the restart: %+v", row)
	}
	want(t, l.inject("alpha", sr.Epoch, "msg-three", "body text"), leadwire.StatusRefused, leadwire.CodeSecondMessage)
	if n := injectFrames(h); n != 2 {
		t.Fatalf("%d frames reached the holder after the restart, want 2", n)
	}
}

// The canary keeps its rule: a refusal of the holder spends the epoch's
// message in the instance and in the daemon that reads the evidence back,
// where an instance of scratch mode (the flag the router sets for a lead)
// takes it back, busy excepted: that one stays spent in both. The
// inject-refused line is the same in all of them.
func TestCanaryHolderRefusalStillSpendsTheMessageAndScratchDoesNot(t *testing.T) {
	ctx := context.Background()
	for _, code := range holderRefusals {
		for _, scratch := range []bool{false, true} {
			f := newExtFixture(t, answersInOrder(
				func(conn net.Conn) { writeResponse(conn, wire.Response{OK: false, Code: code}) }, answerSubmit), "")
			f.x.scratch = scratch
			_, fl := f.x.injectFor(ctx, CanaryInjectParams{MessageID: "msg-one", Body: "body text"}, f.epoch)
			if fl == nil || fl.code != "holder-"+code {
				t.Fatalf("%s, scratch %v: %+v", code, scratch, fl)
			}
			freed := scratch && holderFrees(code)
			if got := f.x.messageSpent(f.epoch); got == freed {
				t.Fatalf("%s, scratch %v: spent %v", code, scratch, got)
			}
			_, fl = f.x.injectFor(ctx, CanaryInjectParams{MessageID: "msg-two", Body: "body text"}, f.epoch)
			if freed && fl != nil || !freed && (fl == nil || fl.code != "second-message") {
				t.Fatalf("%s, scratch %v: the second message: %+v", code, scratch, fl)
			}
		}
	}
}

// The canary daemon reads its evidence with the canary's reader: the inject-refused
// line of a holder's refusal does not free the epoch's message across a restart.
func TestCanaryHolderRefusalMessageStaysSpentAcrossARestart(t *testing.T) {
	c := newCanaryEnv(t)
	started := c.up(respondWith(wire.Response{OK: false, Code: wire.CodeBusy}))
	if _, err := c.inject("msg-one", "body text\n"); err == nil {
		t.Fatal("the message was not refused")
	}
	c.restart()
	if _, _, epoch, found := c.shown(); !found || epoch != started.Epoch {
		t.Fatalf("after the restart: epoch %d (found %v)", epoch, found)
	}
	_, err := c.inject("msg-two", "body text\n")
	if ce := wantCode(t, err, CodeRefused); ce.Message != msgSecond {
		t.Fatalf("message %q", ce.Message)
	}
	if n := len(c.holder.requests(wire.OpInject)); n != 1 {
		t.Fatalf("%d inject requests", n)
	}
}

// A refusal line that could not be written leaves the message spent, as a
// result line does: the evidence cannot show the message as free, and the
// daemon does not show it free either.
func TestScratchHolderRefusalStaysSpentWhenItsLineCannotBeWritten(t *testing.T) {
	f := newExtFixture(t, respondWith(wire.Response{OK: false, Code: wire.CodeDisabled}), "")
	f.x.scratch = true
	if err := f.x.ev.Close(); err != nil {
		t.Fatal(err)
	}
	if _, fl := f.x.injectFor(context.Background(), CanaryInjectParams{MessageID: "msg-one", Body: "body text"}, f.epoch); fl == nil || fl.code != "holder-disabled" {
		t.Fatalf("%+v", fl)
	}
	if !f.x.messageSpent(f.epoch) {
		t.Fatal("a refusal whose line is not written is shown free")
	}
}
