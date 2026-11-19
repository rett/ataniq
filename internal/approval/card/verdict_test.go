package card

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"ataniq/internal/approval/record"
)

func twoRuns(first, second bool) *VerdictBlock {
	return &VerdictBlock{Runs: [2]Run{
		{ID: "r1", Provider: "provider-a", Model: "model-a", Approve: first},
		{ID: "r2", Provider: "provider-b", Model: "model-b", Approve: second},
	}}
}

func TestVerdictTextCleansAndBounds(t *testing.T) {
	for in, want := range map[string]string{
		"model-a":                 "model-a",
		"  a \t\n  b ":            "a b",
		"x\x1b[31my\x07z":         "x[31myz",
		"a‮b​c":                   "abc",
		"é":                      "e",
		"bad\xffbyte":             "badbyte",
		strings.Repeat("a", 40):   strings.Repeat("a", 40),
		strings.Repeat("a", 41):   strings.Repeat("a", 40) + "…",
		strings.Repeat("é", 100):  strings.Repeat("é", 40) + "…",
		strings.Repeat("a ", 100): strings.Repeat("a ", 20)[:39] + "…",
	} {
		if got := VerdictText(in); got != want {
			t.Errorf("VerdictText(%q) = %q, want %q", in, got, want)
		}
	}
	if got := VerdictText(strings.Repeat("中", 500)); utf8.RuneCountInString(got) != MaxVerdictText+1 {
		t.Errorf("a long text kept %d characters", utf8.RuneCountInString(got))
	}
}

func TestVerdictCheckListIsClosed(t *testing.T) {
	seen := map[string]bool{}
	for k := CheckKind(1); k <= CheckKind(NumChecks); k++ {
		if !k.Valid() || k.String() == "unknown" || seen[k.String()] {
			t.Errorf("check %d: %q", k, k.String())
		}
		seen[k.String()] = true
	}
	if CheckKind(0).Valid() || CheckKind(NumChecks+1).Valid() || CheckKind(NumChecks+1).String() != "unknown" {
		t.Error("a value outside the list is valid")
	}
}

func TestVerdictAgreement(t *testing.T) {
	both := twoRuns(true, true)
	split := twoRuns(true, false)
	rej := twoRuns(false, false)
	for _, c := range []struct {
		name          string
		it            Item
		agreed, count bool
	}{
		{"approved, both approve", Item{State: record.StateApplied, Verdicts: both}, true, true},
		{"approved, one rejects", Item{State: record.StateApproved, Verdicts: split}, false, true},
		{"denied, both reject", Item{State: record.StateDenied, Verdicts: rej}, true, true},
		{"denied, both approve", Item{State: record.StateDenied, Verdicts: both}, false, true},
		{"no verdicts", Item{State: record.StateApproved}, false, false},
		{"by rule", Item{State: record.StateApprovedByRule, Verdicts: both}, false, false},
		{"pending", Item{Verdicts: both}, false, false},
		{"expired", Item{State: record.StateExpired, Verdicts: rej}, false, false},
	} {
		a, n := c.it.Agreed()
		if a != c.agreed || n != c.count {
			t.Errorf("%s: agreed %v counted %v", c.name, a, n)
		}
	}
}

// New keeps verdicts off a card that is not decided, and on one that is.
func TestVerdictOnlyOnDecidedCards(t *testing.T) {
	for _, s := range []record.State{"", record.StatePending, record.StateWithdrawn, record.StateExpired} {
		c := Card{After: After{State: s}}
		if c.Decided() {
			t.Errorf("state %q is decided", s)
		}
	}
	if !(Card{After: After{State: record.StateDenied}}).Decided() {
		t.Error("denied is not decided")
	}
}

// New copies one thing of the verdict block to a card that is not decided:
// whether the card exists because a run refused. Nothing else of the block
// reaches it. A decided card holds the whole block and no separate mark.
func TestVerdictRefusedMarkOnUndecidedCards(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "record", "testdata", "pending-valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := record.DecodePending(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, refused := range []bool{true, false} {
		v := twoRuns(true, true)
		v.Refused = refused
		for _, s := range []record.State{"", record.StatePending, record.StateExpired} {
			c, err := New(p, Inputs{State: s, Verdicts: v})
			if err != nil {
				t.Fatal(err)
			}
			if c.Refused != refused || c.Verdicts != nil {
				t.Errorf("state %q, refused %v: Refused %v, Verdicts %v", s, refused, c.Refused, c.Verdicts)
			}
		}
		c, err := New(p, Inputs{State: record.StateDenied, Verdicts: v})
		if err != nil {
			t.Fatal(err)
		}
		if c.Verdicts != v || c.Refused {
			t.Errorf("decided, refused %v: Verdicts %v, Refused %v", refused, c.Verdicts, c.Refused)
		}
	}
	c, err := New(p, Inputs{})
	if err != nil || c.Refused || c.Verdicts != nil {
		t.Errorf("no verdicts: %+v, %v", c.Refused, err)
	}
}
