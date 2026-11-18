//go:build linux && ataniq_canary

package leaddaemon

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestCanaryUnspentMessageStaysUnspentAcrossARestart(t *testing.T) {
	c := newCanaryEnv(t)
	c.start()
	c.define()
	c.startRole()
	// No holder listens: nothing was sent, and the evidence says so.
	_, err := c.inject("msg-one", "body text\n")
	wantCode(t, err, CodeUnavailable)

	c.restart()
	c.holder = newFakeHolder(t, c.holderDir, submitted)
	got, err := c.inject("msg-one", "body text\n")
	if err != nil || got.Result.Outcome != wire.InjectOutcomeSubmitted {
		t.Fatalf("%+v %v", got, err)
	}
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
		"no role":                  {`{"event":"inject-sent","epoch":1}` + "\n", `"role" is not a string`},
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
			err := c.runToEnd(c.daemonConfig())
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
