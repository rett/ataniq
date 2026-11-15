package appcontract

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"ataniq/internal/appmodel"
)

func TestScalarEncodings(t *testing.T) {
	data, err := os.ReadFile("testdata/synthetic-v2.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		ScalarFields map[string]struct {
			UnframedHex string `json:"unframed_hex"`
			LPHex       string `json:"lp_hex"`
		} `json:"scalar_fields"`
	}
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	one := uint64(1)
	match, err := preconditionBytes(Match{strings.Repeat("11", 32)}, false, true)
	if err != nil {
		t.Fatal(err)
	}
	absent, err := preconditionBytes(Absent{}, true, false)
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string][]byte{"u1": number(1), "b1": byteField(1), "optional_none": optionalNumber(nil), "optional_1": optionalNumber(&one), "absent": absent, "match_11": match} {
		want := corpus.ScalarFields[name]
		lp := frame("", got)[len("ATANIQ-")+2:]
		if hex.EncodeToString(got) != want.UnframedHex || hex.EncodeToString(lp) != want.LPHex {
			t.Errorf("%s: got %x LP %x", name, got, lp)
		}
	}
}

func TestConflictShapes(t *testing.T) {
	i := Instance{Authority: "a"}
	rev := strings.Repeat("a", 64)
	key := appmodel.CommandKey{Authority: "a", EnrolledClientID: "c", CommandID: "q"}
	p := appmodel.ProjectRef{Authority: "a", ID: "p"}
	q := appmodel.ConversationRef{Authority: "a", ID: "q"}
	valid := []ConflictView{ConversationAdmissionConflict{p, q, rev}, ExactEnqueueStatusConflict{p, key, appmodel.CurrentStatus{Command: key, Revision: appmodel.Revision(rev), State: appmodel.Held}}, AdminResourceConflict{ProjectResource{p}, Absent{}}, AdminResourceConflict{ClientResource{i, "c"}, Match{rev}}}
	for _, v := range valid {
		if err := ValidateConflictView(i, v); err != nil {
			t.Errorf("%T: %v", v, err)
		}
	}
	invalid := []ConflictView{nil, &ConversationAdmissionConflict{}, AdminResourceConflict{CommandResource{p, key}, Absent{}}, AdminResourceConflict{ProjectResource{p}, Match{"A"}}, ExactEnqueueStatusConflict{p, key, appmodel.CurrentStatus{Command: key, Revision: appmodel.Revision(rev), State: appmodel.Applied}}}
	for _, v := range invalid {
		if err := ValidateConflictView(i, v); err == nil {
			t.Errorf("accepted %T", v)
		}
	}
}
