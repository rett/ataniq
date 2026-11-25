package agentsession

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
)

func checkEnum[T interface {
	~string
	All() []T
	Valid() bool
}](t *testing.T, rows map[string][]string, name string, zero T) {
	t.Helper()
	var got []string
	for _, v := range zero.All() {
		if !v.Valid() {
			t.Fatal(name, v)
		}
		got = append(got, string(v))
	}
	if !slices.Equal(rows[name], got) || T("invalid").Valid() {
		t.Fatal(name, got)
	}
	delete(rows, name)
}
func TestClosedSets(t *testing.T) {
	b, err := os.ReadFile("sessiontest/testdata/enums.golden")
	if err != nil {
		t.Fatal(err)
	}
	var rows map[string][]string
	if err = json.Unmarshal(b, &rows); err != nil {
		t.Fatal(err)
	}
	checkEnum(t, rows, "State", State(""))
	checkEnum(t, rows, "Basis", Basis(""))
	checkEnum(t, rows, "Sight", Sight(""))
	checkEnum(t, rows, "Permit", Permit(""))
	checkEnum(t, rows, "Assertion", Assertion(""))
	checkEnum(t, rows, "Reading", Reading(""))
	checkEnum(t, rows, "Cause", Cause(""))
	checkEnum(t, rows, "Fact", Fact(""))
	checkEnum(t, rows, "Want", Want(""))
	checkEnum(t, rows, "Outcome", Outcome(""))
	checkEnum(t, rows, "HookKind", HookKind(""))
	checkEnum(t, rows, "IDForm", IDForm(""))
	checkEnum(t, rows, "Class", Class(""))
	checkEnum(t, rows, "DeliveryReason", DeliveryReason(""))
	checkEnum(t, rows, "DenyReason", DenyReason(""))
	checkEnum(t, rows, "ClaimSource", ClaimSource(""))
	checkEnum(t, rows, "Slot", Slot(""))
	checkEnum(t, rows, "DroppedReason", DroppedReason(""))
	checkEnum(t, rows, "InputReason", InputReason(""))
	if len(rows) != 0 {
		t.Fatal(rows)
	}
}
