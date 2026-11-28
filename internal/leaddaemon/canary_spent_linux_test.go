//go:build linux && ataniq_canary

package leaddaemon

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadholder/wire"
)

// restart stops the daemon and starts it again on the same roots, with the
// same unit manager: the lead keeps running, in its epoch.
func (c *canaryEnv) restart() {
	c.t.Helper()
	if err := c.running.stop(); err != nil {
		c.t.Fatalf("the daemon did not stop cleanly: %v", err)
	}
	c.start()
}

func TestCanaryMessageStaysSpentAcrossARestart(t *testing.T) {
	c := newCanaryEnv(t)
	started := c.up(submitted)
	got, err := c.inject("msg-one", "body text\n")
	if err != nil || got.Result.Outcome != wire.InjectOutcomeSubmitted {
		t.Fatalf("%+v %v", got, err)
	}

	c.restart()
	// The role is still running in the same epoch: the refusal below is the
	// second message's, not a role that is down.
	if state, _, epoch, found := c.shown(); !found || epoch != started.Epoch || state != "starting" {
		t.Fatalf("after the restart the role is %q in epoch %d (found %v)", state, epoch, found)
	}
	_, err = c.inject("msg-two", "body text\n")
	ce := wantCode(t, err, CodeRefused)
	if ce.Message != msgSecond {
		t.Fatalf("message %q, want %q", ce.Message, msgSecond)
	}
	if l := c.lastEvidence(); l["event"] != "inject-refused" || l["code"] != "second-message" ||
		l["message_id"] != "msg-two" || l["epoch"] != float64(started.Epoch) {
		t.Fatalf("line %v", l)
	}
	if reqs := c.holder.requests(wire.OpInject); len(reqs) != 1 || reqs[0].MessageID != "msg-one" {
		t.Fatalf("inject requests %+v: the second message reached the holder", reqs)
	}
	if !strings.Contains(c.log.String(), "the evidence shows the message of epoch 1 as sent") {
		t.Fatalf("the log does not say what was read back:\n%s", c.log)
	}

	// A second restart changes nothing, and neither does the message's own id.
	c.restart()
	_, err = c.inject("msg-one", "body text\n")
	wantCode(t, err, CodeRefused)
	if n := len(c.holder.requests(wire.OpInject)); n != 1 {
		t.Fatalf("%d inject requests", n)
	}
}

// The canary keeps its stricter rule: a message whose inject-sent line is
// written and whose socket could not be dialled is spent, the inject-refused
// line with the code unsent not freeing it, in this daemon and in the one that
// starts next.
func TestCanaryUnsentMessageStaysSpentAcrossARestart(t *testing.T) {
	c := newCanaryEnv(t)
	c.start()
	c.define()
	c.startRole()
	// No holder listens: nothing was sent, and the evidence says so.
	_, err := c.inject("msg-one", "body text\n")
	wantCode(t, err, CodeUnavailable)
	_, err = c.inject("msg-two", "body text\n")
	if ce := wantCode(t, err, CodeRefused); ce.Message != msgSecond {
		t.Fatalf("message %q, want %q", ce.Message, msgSecond)
	}

	c.restart()
	c.holder = newFakeHolder(t, c.holderDir, submitted)
	_, err = c.inject("msg-three", "body text\n")
	if ce := wantCode(t, err, CodeRefused); ce.Message != msgSecond {
		t.Fatalf("message %q, want %q", ce.Message, msgSecond)
	}
	if n := len(c.holder.requests(wire.OpInject)); n != 0 {
		t.Fatalf("%d inject requests reached the holder", n)
	}
}

// runToRefusal runs the daemon in canary mode, which must refuse to start, and
// returns its error. A daemon that starts instead fails the test as soon as it
// listens, and is stopped: the test does not wait for a daemon that will not
// end by itself.
func (c *canaryEnv) runToRefusal() error {
	c.t.Helper()
	cfg := c.daemonConfig()
	listening := make(chan string, 1)
	cfg.Ready = func(s string) { listening <- s }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg) }()
	stopped := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
		}
	}
	select {
	case err := <-done:
		return err
	case <-listening:
		stopped()
		c.t.Fatal("the daemon started and listens; it was to refuse the start")
	case <-time.After(20 * time.Second):
		stopped()
		c.t.Fatal("the daemon neither refused nor listened in 20 s")
	}
	return nil
}

// evidenceLineOf is a line of the evidence as the writer makes it, without the
// newline.
func evidenceLineOf(event, role string, more string) string {
	return `{"at":"2001-02-03T04:05:06Z","event":"` + event + `","role":"` + role + `"` + more + `,"agent":"stand-in"}`
}

func TestCanaryDamagedEvidenceRefusesTheStart(t *testing.T) {
	good := evidenceLineOf("daemon-start", canaryRoleText, "") + "\n"
	for name, tc := range map[string]struct {
		content, want string
	}{
		"a line cut short":         {good + `{"at":"2001-02-03T04:05:06Z","event":"inject-se`, "does not end with a newline"},
		"a part of a line":         {good + `{"at":"2001-02-03T04:05:06Z","eve` + "\n" + good, "line 2 of the evidence file is damaged"},
		"an empty line":            {good + "\n" + good, "line 2 of the evidence file is damaged"},
		"not an object":            {"[1,2]\n", "not a JSON object"},
		"null":                     {"null\n", "not a JSON object"},
		"two objects on a line":    {strings.TrimSuffix(good, "\n") + " " + good, "not a JSON object"},
		"no event":                 {`{"role":"` + canaryRoleText + `","epoch":1}` + "\n", `"event" is not a string`},
		"an event that is no text": {`{"event":7,"role":"` + canaryRoleText + `"}` + "\n", `"event" is not a string`},
		"an empty event":           {`{"event":"","role":"` + canaryRoleText + `"}` + "\n", `"event" is not a string`},
		"no role":                  {`{"event":"inject-sent","epoch":1}` + "\n", `"role" is not a string`},
		"an empty role":            {`{"event":"inject-sent","role":"","epoch":1}` + "\n", `"role" is not a string`},
		"an epoch in quotes":       {evidenceLineOf("inject-sent", canaryRoleText, `,"epoch":"1"`) + "\n", `"epoch" is not a whole number`},
		"a negative epoch":         {evidenceLineOf("inject-sent", canaryRoleText, `,"epoch":-1`) + "\n", `"epoch" is not a whole number`},
		"an epoch with a fraction": {evidenceLineOf("inject-sent", canaryRoleText, `,"epoch":1.5`) + "\n", `"epoch" is not a whole number`},
		"an epoch too large":       {evidenceLineOf("inject-sent", canaryRoleText, `,"epoch":18446744073709551616`) + "\n", `"epoch" is not a whole number`},
		"sent without an epoch":    {evidenceLineOf("inject-sent", canaryRoleText, "") + "\n", "an inject-sent line without an epoch"},
		"sent in epoch zero":       {evidenceLineOf("inject-sent", canaryRoleText, `,"epoch":0`) + "\n", "an inject-sent line without an epoch"},
		"too large":                {strings.Repeat(good, maxEvidenceRead/len(good)+1), "larger than"},
	} {
		t.Run(name, func(t *testing.T) {
			c := newCanaryEnv(t)
			path := c.cfg.EvidencePath()
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			err := c.runToRefusal()
			se := mustFail(t, err, StepState, ExitState)
			if !strings.Contains(se.Error(), tc.want) || !strings.Contains(se.Error(), "no message is admitted") {
				t.Fatalf("the error is %q, want %q in it", se.Error(), tc.want)
			}
			// The refusal says when the file may be set aside, and why.
			if !strings.Contains(se.Error(), "set the file aside only once the role's unit is stopped, since a new file spends no epoch's message") {
				t.Fatalf("the error %q does not say when the file may be set aside", se.Error())
			}
			// Nothing was added to the file, nothing listened and no unit was
			// touched.
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(after, []byte(tc.content)) {
				t.Fatalf("the evidence file changed (%v)", err)
			}
			if len(c.fake.Calls()) != 0 {
				t.Fatal("the manager was called")
			}
			c.noSocketMade()
			c.lockFree()
		})
	}
}

func TestSpentEpochsReadsTheSentLinesOfTheRole(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "evidence.jsonl")
	if got, err := spentEpochs(path, canaryRoleText); err != nil || len(got) != 0 {
		t.Fatalf("no file: %v %v", got, err)
	}
	lines := []string{
		evidenceLineOf("daemon-start", canaryRoleText, ""),
		evidenceLineOf("start", canaryRoleText, `,"epoch":1`),
		evidenceLineOf("inject-refused", canaryRoleText, `,"epoch":1,"message_id":"m-0","code":"holder-unreachable"`),
		evidenceLineOf("inject-sent", canaryRoleText, `,"epoch":1,"message_id":"m-1"`),
		evidenceLineOf("inject-result", canaryRoleText, `,"epoch":1,"message_id":"m-1","outcome":"submitted","written":"whole"`),
		evidenceLineOf("start", canaryRoleText, `,"epoch":2`),
		evidenceLineOf("inject-refused", canaryRoleText, `,"epoch":2,"code":"not-running"`),
		// Another role's message spends nothing of this role's.
		evidenceLineOf("inject-sent", "throwaway-two/lead", `,"epoch":3,"message_id":"m-3"`),
		evidenceLineOf("inject-sent", canaryRoleText, `,"epoch":4,"message_id":"m-4"`),
		// A member the reader does not know is not its business.
		evidenceLineOf("stop", canaryRoleText, `,"epoch":4,"sample_member":{"a":[1,2]}`),
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := spentEpochs(path, canaryRoleText)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[1] || !got[4] {
		t.Fatalf("spent epochs %v, want 1 and 4", got)
	}

	// An empty file has no line.
	empty := filepath.Join(dir, "empty.jsonl")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := spentEpochs(empty, canaryRoleText); err != nil || len(got) != 0 {
		t.Fatalf("empty file: %v %v", got, err)
	}
}

func TestSpentEpochsReadsOnlyARegularFileItself(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.jsonl")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.jsonl")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := spentEpochs(link, canaryRoleText); err == nil {
		t.Fatal("a symbolic link was followed")
	}
	fifo := filepath.Join(dir, "fifo.jsonl")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	// A FIFO without a writer: the open does not block, and it is refused.
	if _, err := spentEpochs(fifo, canaryRoleText); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("a FIFO: %v", err)
	}
	if _, err := spentEpochs(dir, canaryRoleText); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("a folder: %v", err)
	}
}

// sentOf and resultOf are lines of the evidence, as the writer makes them.
func sentOf(epoch int, id string) string {
	return evidenceLineOf("inject-sent", canaryRoleText, `,"epoch":`+strconv.Itoa(epoch)+`,"message_id":"`+id+`"`)
}

func resultOf(epoch int, id, outcome, reason, written string) string {
	more := `,"epoch":` + strconv.Itoa(epoch) + `,"message_id":"` + id + `","outcome":"` + outcome + `"`
	if reason != "" {
		more += `,"reason":"` + reason + `"`
	}
	return evidenceLineOf("inject-result", canaryRoleText, more+`,"written":"`+written+`"`)
}

func writeEvidence(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "evidence.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The reader of scratch mode frees an epoch only for a sent line that its own
// result line follows with nothing written; the canary's reader never does.
func TestScratchSpentEpochsFreesOnlyANotDeliveredMessage(t *testing.T) {
	other := evidenceLineOf("inject-sent", "scratch-two/lead", `,"epoch":1,"message_id":"m-9"`)
	otherResult := evidenceLineOf("inject-result", "scratch-two/lead", `,"epoch":1,"message_id":"m-9","outcome":"submitted","written":"whole"`)
	for name, tc := range map[string]struct {
		lines []string
		want  []uint64 // the spent epochs of scratch mode
	}{
		"not-now with nothing written":   {[]string{sentOf(1, "m-1"), resultOf(1, "m-1", "not-now", "not-ready", "nothing")}, nil},
		"not-now blocked":                {[]string{sentOf(1, "m-1"), resultOf(1, "m-1", "not-now", "blocked", "nothing")}, nil},
		"refused untested":               {[]string{sentOf(1, "m-1"), resultOf(1, "m-1", "refused", "untested", "nothing")}, nil},
		"refused text":                   {[]string{sentOf(1, "m-1"), resultOf(1, "m-1", "refused", "text", "nothing")}, nil},
		"submitted":                      {[]string{sentOf(1, "m-1"), resultOf(1, "m-1", "submitted", "", "whole")}, []uint64{1}},
		"not submitted":                  {[]string{sentOf(1, "m-1"), resultOf(1, "m-1", "uncertain", "not-submitted", "partial")}, []uint64{1}},
		"died with nothing written":      {[]string{sentOf(1, "m-1"), resultOf(1, "m-1", "uncertain", "died", "nothing")}, []uint64{1}},
		"died, unknown":                  {[]string{sentOf(1, "m-1"), resultOf(1, "m-1", "uncertain", "died", "unknown")}, []uint64{1}},
		"the sent line only (a kill)":    {[]string{sentOf(1, "m-1")}, []uint64{1}},
		"a combination the wire refuses": {[]string{sentOf(1, "m-1"), resultOf(1, "m-1", "not-now", "died", "nothing")}, []uint64{1}},
		"a result of nothing written, but not of not-delivered": {[]string{sentOf(1, "m-1"), resultOf(1, "m-1", "submitted", "", "nothing")}, []uint64{1}},
		"not-now, then a second message that is submitted": {[]string{
			sentOf(1, "m-1"), resultOf(1, "m-1", "not-now", "not-ready", "nothing"),
			sentOf(1, "m-2"), resultOf(1, "m-2", "submitted", "", "whole")}, []uint64{1}},
		"not-now, then a second one killed": {[]string{
			sentOf(1, "m-1"), resultOf(1, "m-1", "not-now", "not-ready", "nothing"), sentOf(1, "m-2")}, []uint64{1}},
		"not-now twice": {[]string{
			sentOf(1, "m-1"), resultOf(1, "m-1", "not-now", "not-ready", "nothing"),
			sentOf(1, "m-2"), resultOf(1, "m-2", "refused", "text", "nothing")}, nil},
		"the same id used again after a release": {[]string{
			sentOf(1, "m-1"), resultOf(1, "m-1", "not-now", "not-ready", "nothing"),
			sentOf(1, "m-1"), resultOf(1, "m-1", "not-now", "blocked", "nothing")}, nil},
		"the same id used again, and submitted": {[]string{
			sentOf(1, "m-1"), resultOf(1, "m-1", "not-now", "not-ready", "nothing"),
			sentOf(1, "m-1"), resultOf(1, "m-1", "submitted", "", "whole")}, []uint64{1}},
		// Fail closed.
		"a result twice": {[]string{
			sentOf(1, "m-1"), resultOf(1, "m-1", "not-now", "not-ready", "nothing"),
			resultOf(1, "m-1", "not-now", "not-ready", "nothing")}, []uint64{1}},
		"a result with no sent line":     {[]string{resultOf(1, "m-1", "not-now", "not-ready", "nothing")}, []uint64{1}},
		"a result of another message id": {[]string{sentOf(1, "m-1"), resultOf(1, "m-2", "not-now", "not-ready", "nothing")}, []uint64{1}},
		"a result of another epoch":      {[]string{sentOf(1, "m-1"), resultOf(2, "m-1", "not-now", "not-ready", "nothing")}, []uint64{1, 2}},
		"two sent lines of one message":  {[]string{sentOf(1, "m-1"), sentOf(1, "m-1"), resultOf(1, "m-1", "not-now", "not-ready", "nothing")}, []uint64{1}},
		"a sent line of a message in between": {[]string{
			sentOf(1, "m-1"), sentOf(1, "m-2"), resultOf(1, "m-2", "not-now", "not-ready", "nothing"),
			resultOf(1, "m-1", "not-now", "not-ready", "nothing")}, []uint64{1}},
		"a refusal line in between changes nothing": {[]string{
			sentOf(1, "m-1"),
			evidenceLineOf("inject-refused", canaryRoleText, `,"epoch":1,"message_id":"m-2","code":"message-in-flight"`),
			resultOf(1, "m-1", "not-now", "not-ready", "nothing")}, nil},
		"epochs are apart": {[]string{
			sentOf(1, "m-1"), resultOf(1, "m-1", "submitted", "", "whole"),
			sentOf(2, "m-2"), resultOf(2, "m-2", "refused", "text", "nothing")}, []uint64{1}},
		"another role's lines are not the role's": {[]string{other, otherResult,
			sentOf(1, "m-1"), resultOf(1, "m-1", "not-now", "not-ready", "nothing")}, nil},
		"another role's sent line spends nothing": {[]string{other}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			path := writeEvidence(t, tc.lines...)
			got, err := scratchSpentEpochs(path, canaryRoleText)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("spent epochs %v, want %v", got, tc.want)
			}
			for _, e := range tc.want {
				if !got[e] {
					t.Fatalf("spent epochs %v, want %v", got, tc.want)
				}
			}
			// The canary's reader reads the same file and has every epoch with
			// a sent line spent, whatever the result was.
			canary, err := spentEpochs(path, canaryRoleText)
			if err != nil {
				t.Fatal(err)
			}
			for _, l := range tc.lines {
				var ev struct {
					Event string `json:"event"`
					Role  string `json:"role"`
					Epoch int    `json:"epoch"`
				}
				if err := json.Unmarshal([]byte(l), &ev); err != nil {
					t.Fatal(err)
				}
				if ev.Event == "inject-sent" && ev.Role == canaryRoleText && !canary[uint64(ev.Epoch)] {
					t.Fatalf("the canary's reader has epoch %d free", ev.Epoch)
				}
			}
		})
	}
}

// The reader of scratch mode is as strict as the canary's: a line that is
// damaged or partial refuses the start, and so does a line of the role that
// lacks a member it needs.
func TestScratchSpentEpochsRefusesADamagedFile(t *testing.T) {
	good := sentOf(1, "m-1") + "\n"
	res := resultOf(1, "m-1", "not-now", "not-ready", "nothing")
	for name, tc := range map[string]struct {
		content, want string
	}{
		"a line cut short":                     {good + res[:40], "does not end with a newline"},
		"a part of a line":                     {good + res[:40] + "\n", "line 2 of the evidence file is damaged"},
		"an empty line":                        {good + "\n" + good, "line 2 of the evidence file is damaged"},
		"not an object":                        {"[1,2]\n", "not a JSON object"},
		"an epoch in quotes":                   {evidenceLineOf("inject-result", canaryRoleText, `,"epoch":"1"`) + "\n", `"epoch" is not a whole number`},
		"sent without an epoch":                {evidenceLineOf("inject-sent", canaryRoleText, `,"message_id":"m-1"`) + "\n", "an inject-sent line without an epoch"},
		"result without epoch":                 {evidenceLineOf("inject-result", canaryRoleText, `,"message_id":"m-1","outcome":"not-now","written":"nothing"`) + "\n", "an inject-result line without an epoch"},
		"sent without an id":                   {evidenceLineOf("inject-sent", canaryRoleText, `,"epoch":1`) + "\n", `"message_id" is not a string`},
		"result without an id":                 {evidenceLineOf("inject-result", canaryRoleText, `,"epoch":1,"outcome":"not-now","written":"nothing"`) + "\n", `"message_id" is not a string`},
		"result without outcome":               {evidenceLineOf("inject-result", canaryRoleText, `,"epoch":1,"message_id":"m-1","written":"nothing"`) + "\n", `"outcome" is not a string`},
		"result without written":               {evidenceLineOf("inject-result", canaryRoleText, `,"epoch":1,"message_id":"m-1","outcome":"not-now"`) + "\n", `"written" is not a string`},
		"result with a reason that is no text": {evidenceLineOf("inject-result", canaryRoleText, `,"epoch":1,"message_id":"m-1","outcome":"not-now","reason":7,"written":"nothing"`) + "\n", `"reason" is not a string`},
		"too large":                            {strings.Repeat(good, maxEvidenceRead/len(good)+1), "larger than"},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "evidence.jsonl")
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := scratchSpentEpochs(path, canaryRoleText)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v, want %q in it", err, tc.want)
			}
		})
	}
	// A line of another role is not read for the members the role's lines need.
	path := writeEvidence(t, evidenceLineOf("inject-result", "scratch-two/lead", `,"epoch":1`))
	if got, err := scratchSpentEpochs(path, canaryRoleText); err != nil || len(got) != 0 {
		t.Fatalf("%v %v", got, err)
	}
	// The same file shape as for the canary's reader: a link, a FIFO and a
	// folder are refused.
	dir := t.TempDir()
	if _, err := scratchSpentEpochs(dir, canaryRoleText); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("a folder: %v", err)
	}
	if got, err := scratchSpentEpochs(filepath.Join(dir, "none"), canaryRoleText); err != nil || len(got) != 0 {
		t.Fatalf("no file: %v %v", got, err)
	}
}
