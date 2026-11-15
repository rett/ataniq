package appcontract

import (
	"encoding/hex"
	"strings"
	"testing"

	"ataniq/internal/appmodel"
)

func TestScalarEncodings(t *testing.T) {
	one := uint64(1)
	match, err := preconditionBytes(Match{strings.Repeat("11", 32)}, false, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		got  []byte
		want string
	}{
		{number(1), "0000000000000001"}, {[]byte{1}, "01"}, {optionalNumber(nil), "00"}, {optionalNumber(&one), "010000000000000001"}, {[]byte{0}, "00"}, {match, "01" + strings.Repeat("11", 32)},
	} {
		if hex.EncodeToString(tc.got) != tc.want {
			t.Errorf("got %x, want %s", tc.got, tc.want)
		}
	}
}

func TestConflictShapes(t *testing.T) {
	i := Instance{Authority: "a"}
	rev := strings.Repeat("a", 64)
	key := appmodel.CommandKey{Authority: "a", EnrolledClientID: "c", CommandID: "q"}
	valid := []ConflictView{ConversationAdmissionConflict{"p", "q", rev}, ExactEnqueueStatusConflict{"p", key, appmodel.CurrentStatus{Command: key, Revision: appmodel.Revision(rev), State: appmodel.Held}}, AdminResourceConflict{ProjectResource{"p"}, Absent{}}, AdminResourceConflict{ClientResource{"c"}, Match{rev}}}
	for _, v := range valid {
		if err := ValidateConflictView(i, v); err != nil {
			t.Errorf("%T: %v", v, err)
		}
	}
	invalid := []ConflictView{nil, &ConversationAdmissionConflict{}, AdminResourceConflict{CommandResource{"p", key}, Absent{}}, AdminResourceConflict{ProjectResource{"p"}, Match{"A"}}, ExactEnqueueStatusConflict{"p", key, appmodel.CurrentStatus{Command: key, Revision: appmodel.Revision(rev), State: appmodel.Applied}}}
	for _, v := range invalid {
		if err := ValidateConflictView(i, v); err == nil {
			t.Errorf("accepted %T", v)
		}
	}
}
