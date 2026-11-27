package hook_test

import (
	"bytes"
	"reflect"
	"testing"

	"ataniq/internal/agentsession/hookwire"
	"ataniq/packs/claude/hook"
)

// Exercise both sides of extraction. One target mutates the vendor document
// and its selected row. The other mutates the frame presented to the holder.
func FuzzExtractEvent(f *testing.F) {
	ex, table := documented(f)
	for _, raw := range ex.Inputs {
		f.Add(exampleRow(f, table, raw).ID, []byte(raw))
	}
	f.Add("claude.stop", []byte("{"))
	f.Add("claude.session-start", []byte("{}"))
	f.Fuzz(func(t *testing.T, id string, raw []byte) {
		row, ok := table.Lookup(id)
		if !ok {
			return
		}
		req, err := hookwire.Extract(row, bytes.NewReader(raw))
		if err != nil {
			return
		}
		frame, err := hookwire.EncodeRequest(*req)
		if err != nil {
			t.Fatal("extracted request cannot be encoded", err)
		}
		if !checkFrame(t, table, frame) {
			t.Fatal("declared row produced an inadmissible frame")
		}
	})
}

func FuzzDecodeFrame(f *testing.F) {
	ex, table := documented(f)
	for _, raw := range ex.Inputs {
		f.Add(packet(f, exampleRow(f, table, raw), raw))
	}
	f.Add([]byte{})
	f.Add([]byte{0, 0, 0, 2, '{', '}'})
	f.Fuzz(func(t *testing.T, frame []byte) { checkFrame(t, table, frame) })
}

func checkFrame(t *testing.T, table hookwire.Table, frame []byte) bool {
	t.Helper()
	r, err := table.DecodeRequest(frame)
	if err != nil {
		return false
	}
	row, ok := table.Lookup(r.Row)
	if !ok {
		t.Fatal("admitted unknown row")
	}
	event, err := hook.Decode(row, *r)
	if err != nil {
		if !reflect.DeepEqual(event, hook.Event{}) {
			t.Fatal("partial event on refusal", event)
		}
		return true
	}
	if r.Cut || r.Kind != hookwire.Event || !event.Report.Kind.Valid() ||
		event.Report.Session != r.Fields["session_id"] || r.Fields["hook_event_name"] != row.Event {
		t.Fatal("decoded incomplete or mismatched event")
	}
	checkDelivery(t, event)
	return true
}
