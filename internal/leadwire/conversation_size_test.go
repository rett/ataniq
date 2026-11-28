package leadwire

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"ataniq/internal/appcontract"
	"ataniq/internal/appmodel"
)

// worstPage reads, through the real page reader, the fullest page the wire can
// be asked to carry: 200 receipts, every identifier, revision and cursor at
// its cap, every cursor made of characters JSON escapes, and text of such
// characters sized so that the reader's own bound is just met. It returns the
// page in the wire's shape.
func worstPage(t *testing.T, entries int, text string) ConversationReadResult {
	t.Helper()
	word := func(c string) string { return strings.Repeat(c, MaxConversationWord) }
	instance := appcontract.Instance{Authority: word("a"), StoreID: [16]byte{1}, IncarnationID: [16]byte{2}}
	conv := appmodel.ConversationRef{Authority: instance.Authority, ID: word("c")}
	project := appmodel.ProjectRef{Authority: instance.Authority, ID: word("p")}
	lead := word("l")
	clock := time.Date(2026, 11, 1, 12, 0, 0, 999999999, time.UTC)
	src := appcontract.NewMemorySource(lead, conv, func() time.Time { return clock }, appcontract.Retention{})
	src.Grant("viewer", nil)
	for i := 0; i < entries; i++ {
		key := appmodel.CommandKey{Authority: instance.Authority, EnrolledClientID: word("e"), CommandID: appmodel.CommandID(word("k"))}
		src.Append(appcontract.Entry{Kind: appcontract.KindReceipt, Text: text, Command: key, Receipt: &appmodel.AdmissionReceipt{
			Command: key, Digest: [32]byte{0xff, 0xee}, AdmittedRevision: appmodel.Revision(word("r")), Cursor: strings.Repeat("<", MaxCursorBytes)}})
		src.SetStatus(appmodel.CurrentStatus{Command: key, Revision: appmodel.Revision(word("s")), State: appmodel.Cancelled})
	}
	reader, err := appcontract.NewPageReader(instance, [32]byte{7}, src)
	if err != nil {
		t.Fatal(err)
	}
	page, err := reader.Read(appcontract.PageRequest{Viewer: "viewer", Project: project, Conversation: conv, Lead: lead,
		Direction: appcontract.Backward, Limit: appcontract.MaxPageEntries})
	if err != nil {
		t.Fatal(err)
	}
	out := ConversationReadResult{Key: "alpha", State: string(page.State), Entries: []ConversationEntry{}, Older: page.Older, Newer: page.Newer}
	for _, e := range page.Entries {
		we := ConversationEntry{
			ID:   ConversationMessageID{Authority: e.ID.Authority, Lead: e.ID.Lead, Conversation: e.ID.Conversation, Generation: e.ID.Generation, Seq: e.ID.Seq},
			Kind: string(e.Kind), Text: e.Text, At: e.At.Format(time.RFC3339Nano),
			Command: &ConversationCommand{Authority: e.Command.Authority, Client: e.Command.EnrolledClientID, Command: string(e.Command.CommandID)},
			Receipt: &ConversationReceipt{Digest: hex.EncodeToString(e.Receipt.Digest[:]), AdmittedRevision: string(e.Receipt.AdmittedRevision), Cursor: e.Receipt.Cursor},
		}
		if e.Status != nil {
			we.Status = &ConversationStatus{Revision: string(e.Status.Revision), State: string(e.Status.State)}
		}
		out.Entries = append(out.Entries, we)
	}
	return out
}

// TestWorstCasePageFitsTheResultCap holds that a page the reader builds at its
// own bounds is never refused by the wire for size: the reader counts text as
// JSON writes it, and the largest fixed fields of 200 entries fit beside it.
func TestWorstCasePageFitsTheResultCap(t *testing.T) {
	// 200 entries share the reader's byte bound; each entry's own overhead is
	// counted there, and a character that JSON escapes costs six bytes.
	each := (appcontract.MaxPageBytes/appcontract.MaxPageEntries - appcontract.EntryOverhead) / 6
	t.Run("full page", func(t *testing.T) {
		res := worstPage(t, appcontract.MaxPageEntries, strings.Repeat("<", each))
		if len(res.Entries) != appcontract.MaxPageEntries {
			t.Fatalf("%d entries, want a full page", len(res.Entries))
		}
		data, err := json.Marshal(res)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("worst full page: %d bytes of %d", len(data), MaxConversationResultBytes)
		if len(data) > MaxConversationResultBytes*85/100 {
			t.Errorf("the worst full page encodes to %d bytes, over 85%% of the %d cap", len(data), MaxConversationResultBytes)
		}
		if _, err := DecodeConversationReadResult(data); err != nil {
			t.Errorf("the wire refuses the reader's own page of %d bytes: %v", len(data), err)
		}
	})
	t.Run("large text", func(t *testing.T) {
		// Fewer, larger entries: the text bound is what stops the page.
		res := worstPage(t, 40, strings.Repeat("<", MaxConversationText/4))
		data, err := json.Marshal(res)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Entries) == 0 || len(res.Entries) == 40 {
			t.Fatalf("%d entries: the byte bound did not cut the page", len(res.Entries))
		}
		if _, err := DecodeConversationReadResult(data); err != nil {
			t.Errorf("the wire refuses the reader's own page of %d bytes: %v", len(data), err)
		}
	})
	t.Run("one entry of the largest text", func(t *testing.T) {
		res := worstPage(t, 2, strings.Repeat("<", MaxConversationText))
		data, err := json.Marshal(res)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Entries) != 1 {
			t.Fatalf("%d entries, want the one that always fits", len(res.Entries))
		}
		if _, err := DecodeConversationReadResult(data); err != nil {
			t.Errorf("the wire refuses a page of %d bytes: %v", len(data), err)
		}
	})
}
