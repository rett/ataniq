//go:build linux

package serve

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ataniq/internal/leadholder/eventlog"
	"ataniq/internal/leadholder/wire"
)

// A message that is easy to find in anything the holder writes.
const (
	gateText = "[ataniq-msg id=gate-1 role=probe] sample-body-9f3c1e7a"
	gateMark = "sample-body-9f3c1e7a"
	gateID   = "gate-1"
)

// Both builds: with no deliverer the control server answers an inject request
// with disabled, as it did before the wire carried a text. The request's text is
// never read, so the answer is the answer to a request without one, the only
// event is the session's disabled refusal, and nothing of the text, or of the
// message id, reaches the event log, a counter, the status or the terminal.
func TestInjectWithTextAnswersDisabledAndKeepsTheTextOut(t *testing.T) {
	e := newEnv(t, "raw; say READY; wait")
	waitScreen(t, e.sess, "READY")

	plain := e.mustControl(wire.Request{Op: wire.OpInject, Epoch: testEpoch, CommandID: "inj-plain"})
	got := e.mustControl(wire.Request{
		Op: wire.OpInject, Epoch: testEpoch, CommandID: "inj-text", MessageID: gateID, Text: gateText,
	})
	if got != plain {
		t.Fatalf("a request with a text: %+v, without one: %+v", got, plain)
	}
	if got.OK || got.Code != wire.CodeDisabled || got.Text != "injection is disabled" || got.Inject != nil || got.Status != nil {
		t.Fatalf("the answer is not today's: %+v", got)
	}
	// Events are written in the order they were queued: the command event of
	// the second request comes after everything it caused.
	e.waitEvent("the command of the request with a text", func(ev eventlog.Event) bool {
		return ev.Event == "state" && ev.Reason == "inject" && ev.CommandID == "inj-text" && ev.Outcome == "disabled"
	})
	injects := 0
	for _, ev := range e.eventsNow() {
		if ev.Event != "inject" {
			continue
		}
		injects++
		if ev.Reason != "disabled" || ev.Outcome != "refused" || ev.MessageID != "" || ev.Digest != "" ||
			ev.Sender != "" || ev.State != "" || ev.CommandID != "" {
			t.Errorf("the inject event is not today's: %+v", ev)
		}
	}
	if injects != 2 {
		t.Errorf("%d inject events, want 2", injects)
	}
	if n := e.count(cntControlOther); n != 0 {
		t.Errorf("a disabled refusal is not counted, counter %d", n)
	}

	// Nothing of the text, or the id, anywhere the holder writes.
	waitQuiet(t, e.sess)
	st := e.mustControl(wire.Request{Op: wire.OpStatus, Epoch: testEpoch})
	stJSON, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	logged, err := os.ReadFile(filepath.Join(e.logDir, eventlog.FileName))
	if err != nil {
		t.Fatal(err)
	}
	counters, _ := json.Marshal(e.sess.Counters())
	for what, where := range map[string]string{
		"the event log": string(logged), "the status": string(stJSON),
		"the counters": string(counters), "the terminal": screenText(e.sess),
	} {
		for _, needle := range []string{gateMark, gateID} {
			if strings.Contains(where, needle) {
				t.Errorf("%q is in %s", needle, what)
			}
		}
	}
}

// Both builds: the package imports nothing from the injection tree, in a file
// of either build or in a test. The deliverer is an interface that returns the
// wire's result.
func TestServeImportsNothingOfTheInjectionTree(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no Go files: %v", err)
	}
	for _, path := range files {
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if p == "ataniq/internal/inject" || strings.HasPrefix(p, "ataniq/internal/inject/") {
				t.Errorf("%s imports %s", path, p)
			}
		}
	}
}
