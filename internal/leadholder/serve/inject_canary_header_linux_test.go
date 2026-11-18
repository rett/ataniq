//go:build linux && ataniq_canary

package serve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/leadholder/eventlog"
	"ataniq/internal/leadholder/wire"
)

// The text's first line starts with the header of the message id: a matching
// one reaches the deliverer, and any other is bad-request before it, with no
// event and nothing of the text in the log.
func TestInjectHeaderMustNameTheMessageID(t *testing.T) {
	const body = "sample-body-9c71e2d4"
	d := &fakeDeliverer{}
	e := canaryEnv(t, d)
	for name, text := range map[string]string{
		"another id":          "[ataniq-msg id=msg-2 role=probe] " + body,
		"an id with a suffix": "[ataniq-msg id=msg-10 role=probe] " + body,
		"an id with a prefix": "[ataniq-msg id=xmsg-1 role=probe] " + body,
		"no space after id":   "[ataniq-msg id=msg-1] " + body,
		"no header":           body,
		"a header on line 2":  "first line\n[ataniq-msg id=msg-1 role=probe] " + body,
		"a space before":      " [ataniq-msg id=msg-1 role=probe] " + body,
		"another tag":         "[other-msg id=msg-1 role=probe] " + body,
	} {
		req := wire.Request{Op: wire.OpInject, Epoch: testEpoch, CommandID: "inj-head", MessageID: canaryID, Text: text}
		if r := e.srv.dispatch(req); r.OK || r.Code != wire.CodeBadRequest || r.Inject != nil {
			t.Errorf("%s: %+v", name, r)
		}
	}
	if d.calls() != 0 {
		t.Errorf("the deliverer was called %d times", d.calls())
	}
	for _, ev := range e.eventsNow() {
		if ev.Event == "inject" {
			t.Errorf("an inject event: %+v", ev)
		}
	}
	e.noTextInLog()
	raw, err := os.ReadFile(filepath.Join(e.logDir, eventlog.FileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{body, "msg-2", "msg-10", "xmsg-1", "other-msg"} {
		if strings.Contains(string(raw), needle) {
			t.Errorf("%q is in the event log", needle)
		}
	}

	// The matching header, on a first line that goes on, reaches the deliverer.
	for name, text := range map[string]string{
		"the header only":     "[ataniq-msg id=msg-1 ]",
		"a header and a body": canaryText,
		"a header and lines":  "[ataniq-msg id=msg-1 from=a kind=canary]\n" + body + "\nsecond line",
	} {
		req := wire.Request{Op: wire.OpInject, Epoch: testEpoch, CommandID: "inj-ok", MessageID: canaryID, Text: text}
		before := d.calls()
		if r := e.srv.dispatch(req); !r.OK || r.Inject == nil {
			t.Errorf("%s: %+v", name, r)
		}
		if d.calls() != before+1 {
			t.Errorf("%s: the deliverer was not called", name)
		}
	}
}
