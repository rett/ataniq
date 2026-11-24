package leadwire

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// The chat operations are two, in their order, in a list of their own: none
// of them is one of the six, so a client's all-six check of lead control is
// the same with or without them.
func TestChatOperations(t *testing.T) {
	want := []string{"chat.list", "chat.set"}
	got := ChatOperations()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ChatOperations() = %v, want %v", got, want)
	}
	got[0] = "x"
	if ChatOperations()[0] != "chat.list" {
		t.Error("ChatOperations returned a shared slice")
	}
	for _, op := range want {
		before, after, ok := strings.Cut(op, ".")
		if !ok || before == "" || after == "" || strings.Contains(after, ".") || strings.ToLower(op) != op {
			t.Errorf("%q is not a two-part lower-case operation name", op)
		}
		if slices.Contains(Operations(), op) {
			t.Errorf("%q is one of the six lead operations", op)
		}
		if Mutating(op) {
			t.Errorf("Mutating(%q): a chat operation takes no command id and keeps no receipt", op)
		}
	}
	if len(Operations()) != 6 {
		t.Fatalf("the lead operations are %v", Operations())
	}
}

// The params and the row have exactly their keys, in their order; a set has
// no client and no command id.
func TestChatJSON(t *testing.T) {
	b, err := json.Marshal(ChatSetParams{Key: "alpha", Chat: ChatRead})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"key":"alpha","chat":"read"}` {
		t.Fatalf("params %s", b)
	}
	b, err = json.Marshal(ChatListResult{Leads: []ChatLead{{Key: "alpha", Chat: ChatOff, Seat: true, Dropped: map[string]int{}}}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"leads":[{"key":"alpha","chat":"off","seat":true,"suspended":false,"unavailable":false,"head":0,"entries":0,"dropped":{}}]}`
	if string(b) != want {
		t.Fatalf("list %s\nwant %s", b, want)
	}
	for _, c := range ChatErrorCodes() {
		for i := 0; i < len(c); i++ {
			if ch := c[i]; !(ch >= 'a' && ch <= 'z' || i > 0 && (ch == '-' || ch >= '0' && ch <= '9')) {
				t.Errorf("the code %q is not a word", c)
			}
		}
		if slices.Contains(ErrorCodes(), c) || slices.Contains(ReasonCodes(), c) {
			t.Errorf("the code %q repeats another list's", c)
		}
	}
	if ChatOff != "off" || ChatRead != "read" || ChatSend != "send" {
		t.Fatal("the switch words changed")
	}
}
