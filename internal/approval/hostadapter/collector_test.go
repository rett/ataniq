//go:build linux && ataniq_ownerchannel

package hostadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/record"
)

// readReply decodes the reply file of a request id.
func (r *rig) readReply(id string) replyFile {
	r.t.Helper()
	b, err := os.ReadFile(filepath.Join(r.drop, id+replySuffix))
	if err != nil {
		r.t.Fatalf("no reply file for %s: %v", id, err)
	}
	var f replyFile
	if err := json.Unmarshal(b, &f); err != nil {
		r.t.Fatalf("reply for %s: %v: %s", id, err, b)
	}
	return f
}

// M5: the collector refuses a feature name outside the grammar and writes its
// own reply file beside the request; nothing is built from the name, nothing
// reaches the door, the suite or the mirror.
func TestTheCollectorRefusesAFeatureNameOutsideTheGrammar(t *testing.T) {
	r := newRig(t, execFeature)
	before := r.lab.refs()
	names := []string{"Bad_Name", "has space", "../x", "x/y", "UPPER", strings.Repeat("a", 49), "", "ünï", "a\nb", "a;b"}
	validID, valid := r.request("x", nil)
	for i, name := range names {
		// The name goes into the bytes of an otherwise valid request, as a
		// lead could write it.
		quoted, _ := json.Marshal(name)
		b := bytes.Replace(valid, []byte(`"argv":["x"]`), []byte(`"argv":[`+string(quoted)+`]`), 1)
		if bytes.Equal(b, valid) {
			t.Fatalf("the request has no argv to replace: %s", valid)
		}
		id := "rq-name-" + string(rune('a'+i))
		b = bytes.Replace(b, []byte(`"request_id":"`+validID+`"`), []byte(`"request_id":"`+id+`"`), 1)
		r.dropFile(id, b)
	}
	n, err := r.o.Collector.Scan(context.Background())
	if err != nil || n != len(names) {
		t.Fatalf("Scan took %d of %d: %v", n, len(names), err)
	}
	for i, name := range names {
		id := "rq-name-" + string(rune('a'+i))
		rp := r.readReply(id)
		code := record.CodeFeatureName
		if strings.ContainsAny(name, "\n") {
			// A control character is refused by the strict reader before the
			// grammar of the name is looked at.
			code = record.CodeRequestInvalid
		}
		if rp.Code != string(code) || rp.State != "" || rp.PendingID != "" || rp.RequestID != id || rp.Line == "" {
			t.Errorf("reply for the name %q: %+v", name, rp)
		}
		if strings.Contains(rp.Line, name) && name != "" {
			t.Errorf("the reply repeats the name %q: %s", name, rp.Line)
		}
		if _, ok := r.o.Service.Status(id); ok {
			t.Errorf("the service saw the request for %q", name)
		}
		if _, err := os.Stat(filepath.Join(r.drop, collectedDir, id+requestSuffix)); err != nil {
			t.Errorf("the request for %q was not moved out of the drop folder: %v", name, err)
		}
	}
	if r.suite.count() != 0 {
		t.Errorf("the suite ran for a refused request")
	}
	if got := r.lab.refs(); got != before {
		t.Errorf("a ref changed:\n%s\nwant\n%s", got, before)
	}
	if l, err := r.stand.List(context.Background()); err != nil || len(l) != 0 {
		t.Errorf("the door holds %d records: %v", len(l), err)
	}
}

// The other things a request file can get wrong are answered the same way,
// each with its own code, and none reaches the service.
func TestTheCollectorAnswersABadRequestFileWithAReplyFile(t *testing.T) {
	r := newRig(t, execFeature)
	write := func(name string, b []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(r.drop, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, valid := r.request("x", nil)
	// not JSON
	write("rq-junk-aa.request", []byte("not a request"))
	// a request whose id is not the file's name
	write("rq-other-aa.request", valid)
	// over the cap
	write("rq-big-aa.request", bytes.Repeat([]byte("x"), record.MaxRequestBytes+1))
	// a note over the cap
	idNote, bNote := r.request("x", nil)
	write(idNote+".request", bNote)
	write(idNote+".note", bytes.Repeat([]byte("n"), record.MaxNoteBytes+1))
	// an id taken before
	idDup, bDup := r.request("x", nil)
	if err := os.WriteFile(filepath.Join(r.drop, collectedDir, idDup+requestSuffix), bDup, 0o600); err != nil {
		t.Fatal(err)
	}
	write(idDup+".request", bDup)
	// a symbolic link and a folder with a request's name are not requests
	if err := os.Symlink("/etc/passwd", filepath.Join(r.drop, "rq-link-aa.request")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(r.drop, "rq-dir-aa.request"), 0o700); err != nil {
		t.Fatal(err)
	}
	// a name that is no request's name
	write("notes.txt", []byte("x"))
	write("RQ-UPPER.request", valid)

	n, err := r.o.Collector.Scan(context.Background())
	if err != nil || n != 5 {
		t.Fatalf("Scan took %d: %v", n, err)
	}
	want := map[string]record.Code{
		"rq-junk-aa":  record.CodeRequestInvalid,
		"rq-other-aa": record.CodeRequestInvalid,
		"rq-big-aa":   record.CodeRecordTooLarge,
		idNote:        record.CodeRecordTooLarge,
		idDup:         record.CodeRequestDuplicate,
	}
	for id, code := range want {
		if rp := r.readReply(id); rp.Code != string(code) || rp.State != "" || rp.Line == "" {
			t.Errorf("reply for %s: %+v, want %s", id, rp, code)
		}
		if _, ok := r.o.Service.Status(id); ok {
			t.Errorf("the service saw %s", id)
		}
	}
	for _, name := range []string{"rq-link-aa", "rq-dir-aa"} {
		if _, err := os.Stat(filepath.Join(r.drop, name+replySuffix)); err == nil {
			t.Errorf("a reply was written for %s, which is not a request file", name)
		}
	}
	if _, err := os.Stat(filepath.Join(r.drop, "RQ-UPPER"+replySuffix)); err == nil {
		t.Errorf("a reply was written for a file with a name that is not a request's")
	}
	if r.suite.count() != 0 {
		t.Errorf("the suite ran")
	}
	// A second scan takes nothing: what was answered stays answered.
	if n, err := r.o.Collector.Scan(context.Background()); err != nil || n != 0 {
		t.Errorf("a second Scan took %d: %v", n, err)
	}
}

// The reply file is written once: a second reply for the request id, as the
// service gives after a restart, leaves the first, and it holds neither a
// confirm code nor the plan token.
func TestTheReplyFileKeepsTheFirstReplyAndHoldsNoSecret(t *testing.T) {
	r := newRig(t, execFeature)
	m := r.o.Host.mailbox
	rep := ownerchannel.Reply{Project: "project-a", RequestID: "rq-reply-aa", State: record.StateApplied, Line: "first"}
	if err := m.Reply(context.Background(), rep); err != nil {
		t.Fatal(err)
	}
	rep.Line, rep.State = "second", record.StateNotApplied
	if err := m.Reply(context.Background(), rep); err != nil {
		t.Fatalf("a second reply is an error: %v", err)
	}
	rp := r.readReply("rq-reply-aa")
	if rp.Line != "first" || rp.State != "applied" || rp.Schema != ReplySchema {
		t.Errorf("reply %+v", rp)
	}
	if err := m.Reply(context.Background(), ownerchannel.Reply{RequestID: "../x", Line: "x"}); err == nil {
		t.Errorf("a reply for a request id that fails the grammar was written")
	}
	if left, _ := filepath.Glob(filepath.Join(r.drop, ".tmp-*")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
	// After a whole cycle, the plan token of the door and the files of the
	// state folder hold no secret the reply could carry.
	p := r.card()
	if _, err := r.decide(p, true); err != nil {
		t.Fatal(err)
	}
	reply := r.replyOf("rq-test-01")
	if reply == "" || strings.Contains(reply, "h-3fa9c2e1") || strings.Contains(reply, "plan_token") || strings.Contains(strings.ToLower(reply), "confirm") {
		t.Errorf("reply: %q", reply)
	}
}

// A mailbox or a collector is refused for a folder that is not the user's
// private one.
func TestTheDropFolderMustBePrivate(t *testing.T) {
	d := t.TempDir()
	if err := os.Chmod(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileMailbox(d); err == nil {
		t.Errorf("a folder the group and others can read was accepted")
	}
	if _, err := NewFileMailbox(filepath.Join(d, "missing")); err == nil {
		t.Errorf("a folder that is not there was accepted")
	}
	if _, err := NewFileMailbox("relative/dir"); err == nil {
		t.Errorf("a relative folder was accepted")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(privateDir(t), link); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileMailbox(link); err == nil {
		t.Errorf("a symbolic link was accepted")
	}
	if _, err := NewCollector(nil, "project-a", func(context.Context, ownerchannel.Collected) (ownerchannel.RequestStatus, error) {
		return ownerchannel.RequestStatus{}, nil
	}, nil, nil); err == nil {
		t.Errorf("a collector without a mailbox was made")
	}
}
