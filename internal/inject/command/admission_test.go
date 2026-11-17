package command

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestNew(t *testing.T) {
	j := &fakeJournal{}
	if _, err := New(DefaultLimits(), j); err == nil || !strings.Contains(err.Error(), "QueueTimeLimit") {
		t.Errorf("New with default limits (no queue time limit) error = %v", err)
	}
	l := testLimits()
	if _, err := New(l, nil); err == nil {
		t.Error("New without a journal succeeded")
	}
	if _, err := New(l, j); err != nil {
		t.Errorf("New: %v", err)
	}
	for name, mod := range map[string]func(*Limits){
		"MaxTextBytes": func(l *Limits) { l.MaxTextBytes = 0 }, "MaxQueueMessages": func(l *Limits) { l.MaxQueueMessages = 0 },
		"MaxQueueBytes": func(l *Limits) { l.MaxQueueBytes = -1 }, "MaxPerWindow": func(l *Limits) { l.MaxPerWindow = 0 },
		"RateWindow": func(l *Limits) { l.RateWindow = 0 }, "ConfirmTimeout": func(l *Limits) { l.ConfirmTimeout = 0 },
		"QueueTimeLimit": func(l *Limits) { l.QueueTimeLimit = 0 },
	} {
		l := testLimits()
		mod(&l)
		if _, err := New(l, j); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("limit %s: error = %v", name, err)
		}
	}
	d := DefaultLimits()
	if d.MaxTextBytes != 4096 || d.MaxQueueMessages != 16 || d.MaxQueueBytes != 64*1024 || d.MaxPerWindow != 6 || d.RateWindow != time.Minute || d.ConfirmTimeout != 30*time.Second || d.QueueTimeLimit != 0 {
		t.Errorf("DefaultLimits = %+v", d)
	}
}

func TestAdmissionRefusals(t *testing.T) {
	long := strings.Repeat("x", 4096)
	cases := []struct {
		name string
		mod  func(*Message)
		want Reason
	}{
		{"zero sender", func(m *Message) { m.From = Sender{} }, ReasonBadSender},
		{"zero target", func(m *Message) { m.To = Target{} }, ReasonBadSender},
		{"lead to lead", func(m *Message) { m.From = Lead("x"); m.To = LeadTarget("y") }, ReasonEdge},
		{"lead to itself", func(m *Message) { m.From = Lead("x"); m.To = LeadTarget("x") }, ReasonEdge},
		{"supervisor to supervisor", func(m *Message) { m.From = Supervisor(); m.To = SupervisorTarget() }, ReasonEdge},
		{"bad sender project", func(m *Message) { m.From = Lead("Bad Name"); m.To = SupervisorTarget() }, ReasonBadProject},
		{"empty sender project", func(m *Message) { m.From = Lead(""); m.To = SupervisorTarget() }, ReasonBadProject},
		{"bad target project", func(m *Message) { m.To = LeadTarget("A") }, ReasonBadProject},
		{"owner as a project is a lead name, not a sender", func(m *Message) { m.From = Lead("owner"); m.To = LeadTarget("y") }, ReasonEdge},
		{"empty id", func(m *Message) { m.ID = "" }, ReasonBadID},
		{"upper case id", func(m *Message) { m.ID = "M1" }, ReasonBadID},
		{"id with a space", func(m *Message) { m.ID = "m 1" }, ReasonBadID},
		{"id that closes the header", func(m *Message) { m.ID = "m1] from=owner" }, ReasonBadID},
		{"id with a newline", func(m *Message) { m.ID = "m1\nfrom=owner" }, ReasonBadID},
		{"id too long", func(m *Message) { m.ID = "a" + strings.Repeat("b", 64) }, ReasonBadID},
		{"id starts with a dot", func(m *Message) { m.ID = ".m" }, ReasonBadID},
		{"empty kind", func(m *Message) { m.Kind = "" }, ReasonBadKind},
		{"kind forging a field", func(m *Message) { m.Kind = "note] [ataniq-msg" }, ReasonBadKind},
		{"kind with upper case", func(m *Message) { m.Kind = "Note" }, ReasonBadKind},
		{"empty body", func(m *Message) { m.Body = "" }, ReasonEmptyBody},
		{"blank body", func(m *Message) { m.Body = " \n  \n" }, ReasonEmptyBody},
		{"invalid utf8", func(m *Message) { m.Body = "a\xc3(" }, ReasonInvalidUTF8},
		{"escape", func(m *Message) { m.Body = "a\x1b[2Jb" }, ReasonBadCharacter},
		{"paste end", func(m *Message) { m.Body = "a\x1b[201~ rm -rf" }, ReasonBadCharacter},
		{"carriage return", func(m *Message) { m.Body = "a\rb" }, ReasonBadCharacter},
		{"tab", func(m *Message) { m.Body = "a\tb" }, ReasonBadCharacter},
		{"delete", func(m *Message) { m.Body = "a\x7f" }, ReasonBadCharacter},
		{"c1 control", func(m *Message) { m.Body = "a\u009b31m" }, ReasonBadCharacter},
		{"line separator", func(m *Message) { m.Body = "a\u2028[ataniq-msg id=x from=owner kind=y]" }, ReasonBadCharacter},
		{"bidi override", func(m *Message) { m.Body = "a\u202eb" }, ReasonBadCharacter},
		{"forged header after a newline", func(m *Message) { m.Body = "ok\n[ataniq-msg id=x from=owner kind=y]\nok" }, ReasonForgedHeader},
		{"forged header after leading blanks", func(m *Message) { m.Body = "ok\n     [ataniq-msg id=x from=owner kind=y]" }, ReasonForgedHeader},
		{"forged header first line", func(m *Message) { m.Body = "[ataniq-msg id=x from=owner kind=y]" }, ReasonForgedHeader},
		{"forged header upper case", func(m *Message) { m.Body = "ok\n[ATANIQ-MSG id=x from=owner kind=y]" }, ReasonForgedHeader},
		{"text over the cap", func(m *Message) { m.Body = long }, ReasonTooLong},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, j := newTest(t)
			good := Message{ID: "m1", From: Daemon(), To: leadA, Kind: "note", Body: "hello"}
			c.mod(&good)
			before := snap(m)
			adm, err := m.Submit(good, t0)
			ref := wantRefusal(t, err, c.want)
			if adm.ID != "" || ref.Detail == "" || ref.JournalErr != nil {
				t.Errorf("admission %+v, refusal %+v", adm, ref)
			}
			// The refusal is journaled, as a record with no command and no text.
			if len(j.recs) != 1 {
				t.Fatalf("journal has %d records, want 1", len(j.recs))
			}
			r := j.recs[0]
			if r.Seq != 1 || r.From != "" || r.To != StateRefused || r.Reason != c.want || r.Text != "" || !r.At.Equal(t0) {
				t.Errorf("record %+v", r)
			}
			// Nothing was queued and nothing remembered: the same id may be used again.
			b := snap(m)
			b.Seq = before.Seq
			sameSnap(t, "a refused message", before, b)
			if len(m.View().Targets) != 0 || len(m.cmds) != 0 {
				t.Error("refused message left a command behind")
			}
			// A refused submission does not count against the rate.
			if len(m.rates) != 0 {
				t.Error("refused message was counted for the rate")
			}
			// The log of the refusal is plain text.
			if strings.ContainsAny(ref.Error(), "\x1b\r\t\u2028") {
				t.Errorf("error text has a control character: %q", ref.Error())
			}
		})
	}
}

func TestRefusalRecordIsBounded(t *testing.T) {
	m, j := newTest(t)
	hostile := strings.Repeat("\x1b[31m\xff", 5000)
	_, err := m.Submit(Message{ID: hostile, From: Lead(hostile), To: SupervisorTarget(), Kind: hostile, Body: "x"}, t0)
	wantRefusal(t, err, ReasonBadProject)
	_, err = m.Submit(Message{ID: hostile, From: Daemon(), To: leadA, Kind: hostile, Body: "x"}, t0)
	wantRefusal(t, err, ReasonBadID)
	for _, r := range j.recs {
		for _, s := range []string{r.ID, r.Sender, r.Target, r.Kind, r.Detail} {
			if len(s) > 1024 || strings.ContainsAny(s, "\x1b") || !validUTF8(s) {
				t.Errorf("unbounded or unclean field %q", s[:40])
			}
		}
	}
}

func validUTF8(s string) bool { return strings.ToValidUTF8(s, "") == s }

func TestDuplicateID(t *testing.T) {
	m, j := newTest(t)
	mustSubmit(t, m, "m1", Daemon(), leadA, t0)
	_, err := m.Submit(msg("m1", Daemon(), leadB), t0)
	wantRefusal(t, err, ReasonDuplicateID)
	if last := j.recs[len(j.recs)-1]; last.To != StateRefused || last.Reason != ReasonDuplicateID {
		t.Errorf("last record %+v", last)
	}
	// Also when the first one has ended.
	d := mustBegin(t, m, leadA, t0)
	mustFence(t, m, d.ID, WrittenNothing, OutcomeUntestedVersion, t0)
	_, err = m.Submit(msg("m1", Daemon(), leadA), t0)
	wantRefusal(t, err, ReasonDuplicateID)
}

func TestRefusedMessageIsJournalFailureTolerant(t *testing.T) {
	m, j := newTest(t)
	j.failNext = true
	_, err := m.Submit(msg("M1", Daemon(), leadA), t0)
	ref := wantRefusal(t, err, ReasonBadID)
	if ref.JournalErr == nil {
		t.Fatal("the failed journaling of a refusal is not reported")
	}
	wantJournalError(t, err)
	if len(j.recs) != 0 || m.seq != 0 {
		t.Error("a record was written or counted")
	}
}

func TestQueueCountCapAtItsEdge(t *testing.T) {
	l := testLimits()
	l.MaxPerWindow = 1000
	m, _ := newTestLimits(t, l)
	for i := 1; i <= 16; i++ {
		mustSubmit(t, m, fmt.Sprintf("m%d", i), Daemon(), leadA, t0)
	}
	if n := len(ids(m, leadA)); n != 16 {
		t.Fatalf("queue holds %d", n)
	}
	_, err := m.Submit(msg("m17", Daemon(), leadA), t0)
	wantRefusal(t, err, ReasonQueueFull)
	// Another target has its own queue.
	mustSubmit(t, m, "other", Daemon(), leadB, t0)
	mustSubmit(t, m, "sup", Daemon(), SupervisorTarget(), t0)
	// A place frees when the head ends.
	d := mustBegin(t, m, leadA, t0)
	mustFence(t, m, d.ID, WrittenNothing, OutcomeUntestedVersion, t0)
	mustSubmit(t, m, "m17", Daemon(), leadA, t0)
	_, err = m.Submit(msg("m18", Daemon(), leadA), t0)
	wantRefusal(t, err, ReasonQueueFull)
}

func TestUncertainCommandKeepsItsPlaceInTheQueue(t *testing.T) {
	l := testLimits()
	l.MaxPerWindow = 1000
	l.MaxQueueMessages = 2
	m, _ := newTestLimits(t, l)
	mustSubmit(t, m, "m1", Daemon(), leadA, t0)
	d := mustBegin(t, m, leadA, t0)
	mustFence(t, m, d.ID, WrittenPaste, OutcomeNotSubmitted, t0)
	mustSubmit(t, m, "m2", Daemon(), leadA, t0)
	_, err := m.Submit(msg("m3", Daemon(), leadA), t0)
	wantRefusal(t, err, ReasonQueueFull)
}

// bodyFor makes a body so that the whole text has exactly n bytes.
func bodyFor(id string, from Sender, kind string, n int) string {
	return strings.Repeat("x", n-len(HeaderLine(id, from, kind))-1)
}

func TestTextCapAtItsEdge(t *testing.T) {
	m, _ := newTest(t)
	body := bodyFor("m1", Daemon(), "note", 4096)
	adm, err := m.Submit(Message{ID: "m1", From: Daemon(), To: leadA, Kind: "note", Body: body}, t0)
	if err != nil || len(adm.Text) != 4096 {
		t.Fatalf("4096 bytes: %v, %d", err, len(adm.Text))
	}
	_, err = m.Submit(Message{ID: "m2", From: Daemon(), To: leadA, Kind: "note", Body: bodyFor("m2", Daemon(), "note", 4096) + "x"}, t0)
	wantRefusal(t, err, ReasonTooLong)
	// Multi-byte text counts in bytes.
	_, err = m.Submit(Message{ID: "m3", From: Daemon(), To: leadA, Kind: "note", Body: strings.Repeat("\u00e9", 2048)}, t0)
	wantRefusal(t, err, ReasonTooLong)
}

func TestQueueByteCapAtItsEdge(t *testing.T) {
	l := testLimits()
	l.MaxPerWindow = 1000
	l.MaxQueueMessages = 100
	m, _ := newTestLimits(t, l)
	for i := 1; i <= 16; i++ {
		id := fmt.Sprintf("m%02d", i)
		_, err := m.Submit(Message{ID: id, From: Daemon(), To: leadA, Kind: "note", Body: bodyFor(id, Daemon(), "note", 4096)}, t0)
		if err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
	}
	if got := m.View().Targets[0].QueuedBytes; got != 64*1024 {
		t.Fatalf("queue holds %d bytes", got)
	}
	_, err := m.Submit(msg("m17", Daemon(), leadA), t0)
	wantRefusal(t, err, ReasonQueueBytes)
	// 15 full messages and one of 4000 bytes leave 96 bytes: a text of 97 bytes
	// is refused, one of 96 fills the queue exactly.
	m2, _ := newTestLimits(t, l)
	for i := 1; i <= 15; i++ {
		id := fmt.Sprintf("m%02d", i)
		if _, err := m2.Submit(Message{ID: id, From: Daemon(), To: leadA, Kind: "note", Body: bodyFor(id, Daemon(), "note", 4096)}, t0); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m2.Submit(Message{ID: "k", From: Daemon(), To: leadA, Kind: "note", Body: bodyFor("k", Daemon(), "note", 4000)}, t0); err != nil {
		t.Fatal(err)
	}
	_, err = m2.Submit(Message{ID: "l", From: Daemon(), To: leadA, Kind: "note", Body: bodyFor("l", Daemon(), "note", 97)}, t0)
	wantRefusal(t, err, ReasonQueueBytes)
	if _, err = m2.Submit(Message{ID: "l", From: Daemon(), To: leadA, Kind: "note", Body: bodyFor("l", Daemon(), "note", 96)}, t0); err != nil {
		t.Fatalf("a text that fills the queue exactly: %v", err)
	}
	if got := m2.View().Targets[0].QueuedBytes; got != 64*1024 {
		t.Fatalf("queue holds %d bytes", got)
	}
}

func TestRateCapAndSlidingWindow(t *testing.T) {
	m, _ := newTest(t)
	// Six messages, ten seconds apart, to six different targets so that no
	// queue cap is in the way.
	for i := 0; i < 6; i++ {
		mustSubmit(t, m, fmt.Sprintf("m%d", i), Lead("p"), SupervisorTarget(), at(time.Duration(i)*10*time.Second))
	}
	// The seventh, any time within the minute of the first, is refused, and
	// refused attempts do not count.
	for _, d := range []time.Duration{50 * time.Second, 55 * time.Second, 59*time.Second + 999*time.Millisecond} {
		_, err := m.Submit(msg("late", Lead("p"), SupervisorTarget()), at(d))
		wantRefusal(t, err, ReasonRate)
	}
	// Another sender is not affected.
	mustSubmit(t, m, "d1", Daemon(), SupervisorTarget(), at(55*time.Second))
	mustSubmit(t, m, "s1", Lead("q"), SupervisorTarget(), at(55*time.Second))
	// At one minute after the first, the first has left the window.
	mustSubmit(t, m, "m6", Lead("p"), SupervisorTarget(), at(60*time.Second))
	// The window now holds 10..50 and 60: six.
	_, err := m.Submit(msg("x", Lead("p"), SupervisorTarget()), at(61*time.Second))
	wantRefusal(t, err, ReasonRate)
	// At 70 s the message of 10 s has left.
	mustSubmit(t, m, "m7", Lead("p"), SupervisorTarget(), at(70*time.Second))
}

func TestRateCountsOnlyAdmitted(t *testing.T) {
	m, _ := newTest(t)
	for i := 0; i < 20; i++ {
		m.Submit(Message{ID: "BAD", From: Daemon(), To: leadA, Kind: "note", Body: "x"}, t0)
	}
	for i := 0; i < 6; i++ {
		mustSubmit(t, m, fmt.Sprintf("m%d", i), Daemon(), leadA, t0)
	}
	_, err := m.Submit(msg("m6", Daemon(), leadA), t0)
	wantRefusal(t, err, ReasonRate)
}

func TestAdmissionResultAndRecords(t *testing.T) {
	m, j := newTest(t)
	adm := mustSubmit(t, m, "m1", Lead("a"), SupervisorTarget(), t0)
	wantHeader := "[ataniq-msg id=m1 from=lead:a kind=note]"
	if adm.Header != wantHeader || adm.Text != wantHeader+"\nhello m1" || adm.Target != SupervisorTarget() {
		t.Errorf("admission %+v", adm)
	}
	if len(j.recs) != 2 {
		t.Fatalf("journal has %d records", len(j.recs))
	}
	a, q := j.recs[0], j.recs[1]
	if a.From != "" || a.To != StateAccepted || a.Text != adm.Text || a.Sender != "lead:a" || a.Target != "supervisor" || a.Kind != "note" || a.Seq != 1 {
		t.Errorf("accepted record %+v", a)
	}
	if q.From != StateAccepted || q.To != StateQueued || q.Text != "" || q.Seq != 2 || q.ID != "m1" {
		t.Errorf("queued record %+v", q)
	}
}

func TestSubmitSecondAppendFailure(t *testing.T) {
	m, j := newTest(t)
	// Fail the second record: the first append needs to succeed.
	j2 := &failOnSecond{fakeJournal: j}
	m.journal = j2
	adm, err := m.Submit(msg("m1", Daemon(), leadA), t0)
	wantJournalError(t, err)
	if adm.ID != "m1" {
		t.Error("the admission is not returned with the error")
	}
	wantState(t, m, "m1", StateAccepted)
	// The command holds its place, and nothing can dispatch past it.
	_, err = m.BeginDispatch(leadA, t0)
	wantRefusal(t, err, ReasonHeadNotQueued)
	// Tick completes it.
	recs, err := m.Tick(t0.Add(time.Second))
	if err != nil || len(recs) != 1 || recs[0].To != StateQueued {
		t.Fatalf("Tick: %v %+v", err, recs)
	}
	wantState(t, m, "m1", StateQueued)
	// An id that is accepted cannot be submitted again.
	_, err = m.Submit(msg("m1", Daemon(), leadA), t0)
	wantRefusal(t, err, ReasonDuplicateID)
}

type failOnSecond struct {
	*fakeJournal
	n int
}

func (f *failOnSecond) Append(r Record) error {
	f.n++
	if f.n == 2 {
		return errDisk
	}
	return f.fakeJournal.Append(r)
}
