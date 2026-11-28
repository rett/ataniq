package appmodel

import (
	"encoding/json"
	"math"
	"os"
	"testing"
)

type messageIDVectors struct {
	Valid []struct {
		Name string
		ID   struct {
			Authority, Lead, Conversation string
			Generation, Seq               uint64
		}
		Text string
	}
	InvalidText []string `json:"invalid_text"`
}

func readMessageIDVectors(t *testing.T) messageIDVectors {
	t.Helper()
	data, err := os.ReadFile("testdata/message-ids-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var v messageIDVectors
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// TestMessageIDVectors holds the shared vectors: each tuple has exactly its
// canonical text, the text parses back to the same tuple, and no vector
// repeats another's text.
func TestMessageIDVectors(t *testing.T) {
	v := readMessageIDVectors(t)
	if len(v.Valid) < 5 || len(v.InvalidText) < 5 {
		t.Fatalf("vectors were not read: %d valid, %d invalid", len(v.Valid), len(v.InvalidText))
	}
	seen := map[string]string{}
	for _, c := range v.Valid {
		id := MessageID{c.ID.Authority, c.ID.Lead, c.ID.Conversation, c.ID.Generation, c.ID.Seq}
		if got := id.String(); got != c.Text {
			t.Errorf("%s: String() = %q, want %q", c.Name, got, c.Text)
		}
		back, err := ParseMessageID(c.Text)
		if err != nil || back != id {
			t.Errorf("%s: ParseMessageID(%q) = %+v, %v", c.Name, c.Text, back, err)
		}
		if other, dup := seen[c.Text]; dup {
			t.Errorf("%s repeats the text of %s", c.Name, other)
		}
		seen[c.Text] = c.Name
	}
	for _, s := range v.InvalidText {
		if id, err := ParseMessageID(s); err == nil {
			t.Errorf("ParseMessageID(%q) = %+v, want an error", s, id)
		} else {
			wantCode(t, err, InvalidInput)
		}
	}
}

func TestMessageIDValidate(t *testing.T) {
	ok := MessageID{"a", "l", "c", 1, 1}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, id := range map[string]MessageID{
		"zero value":      {},
		"authority":       {"", "l", "c", 1, 1},
		"lead slash":      {"a", "l/x", "c", 1, 1},
		"conversation":    {"a", "l", "c d", 1, 1},
		"zero generation": {"a", "l", "c", 0, 1},
		"zero seq":        {"a", "l", "c", 1, 0},
		"huge generation": {"a", "l", "c", math.MaxInt64 + 1, 1},
		"huge seq":        {"a", "l", "c", 1, math.MaxUint64},
	} {
		wantCode(t, id.Validate(), InvalidInput)
		if id.String() != "" {
			t.Errorf("%s: an invalid ID has the text %q", name, id.String())
		}
	}
}
