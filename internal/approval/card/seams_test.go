package card

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"ataniq/internal/approval/limits"
	"ataniq/internal/approval/record"
)

// The names that other packages build against, held here so that a change
// of one is a change of this test as well. The surfaces, the sample backend
// and the service were built apart from this package and share these.
var (
	_ func(record.Pending, Inputs) (Card, error)                       = New
	_ func(Card, Reading, time.Time) (bool, string)                    = Card.MayApprove
	_ func(Card) string                                                = Card.ShownDigest
	_ func(LeadText, int) (string, bool)                               = LeadText.Line
	_ func(record.Summary, bool) limits.CountInput                     = CountInput
	_ func(record.Pending, record.Pending, time.Time) (Differs, error) = DiffersFrom
)

// fields writes the fields of a struct type as "Name type", joined by "; ".
func fields(v any) string {
	typ := reflect.TypeOf(v)
	var out []string
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		out = append(out, f.Name+" "+f.Type.String())
	}
	return strings.Join(out, "; ")
}

func TestTheSharedTypesHaveTheirFixedFields(t *testing.T) {
	for name, tc := range map[string]struct{ got, want string }{
		"Decision": {fields(Decision{}),
			"ID string; BindingSHA256 string; Approve bool; SignedOff []record.SignOff; Reason string"},
		"Outcome": {fields(Outcome{}),
			"State record.State; Line string; Held bool"},
		"Listing": {fields(Listing{}),
			"Pending []card.Item; Recent []card.Item; Rules []card.Rule; Count card.Count; Sample bool; LookedAt time.Time"},
		"Page": {fields(Page{}),
			"Kind card.PageKind; N int; Pages int; ReviewDigest string; Paths []record.PathChange; Patch []string; Cont []bool"},
		"VerdictBlock": {fields(VerdictBlock{}), "Runs [2]card.Run; Refused bool"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s has the fields %q, want %q", name, tc.got, tc.want)
		}
	}
	item := "; " + fields(Item{}) + ";"
	for _, want := range []string{"ID string", "Project string", "Held bool", "Verdicts *card.VerdictBlock"} {
		if !strings.Contains(item, "; "+want+";") {
			t.Errorf("Item has no field %q", want)
		}
	}
	for name, v := range map[string]any{"Card": Card{}, "Inputs": Inputs{}} {
		f, ok := reflect.TypeOf(v).FieldByName("Verdicts")
		if !ok || f.Type != reflect.TypeOf(&VerdictBlock{}) {
			t.Errorf("%s has no field Verdicts *VerdictBlock", name)
		}
	}
}

// The record package's own valid vectors make cards: the card's rules for
// host strings refuse nothing a record of the shape the design gives holds.
func TestTheRecordVectorsMakeCards(t *testing.T) {
	for _, name := range []string{"pending-valid.json", "pending-valid.pretty.json", "pending-valid-prune.json"} {
		data, err := os.ReadFile(filepath.Join("..", "record", "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		p, err := record.DecodePending(data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		c, err := New(p, Inputs{})
		if err != nil {
			t.Errorf("%s makes no card: %v", name, err)
			continue
		}
		if c.BindingSHA256 != p.BindingSHA256 || c.Title.ID != p.ID || len(c.Refs) != len(p.Binding.Effects) {
			t.Errorf("%s: the card is not the record's", name)
		}
		checkNothingRaw(t, c)
	}
}
