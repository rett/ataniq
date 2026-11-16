package appcontract

import (
	"bytes"
	"math"
	"strings"
	"testing"

	"ataniq/internal/appmodel"
)

func TestResourceIdentityAndKeys(t *testing.T) {
	i := Instance{Authority: "a"}
	p := appmodel.ProjectRef{Authority: "a", ID: "p"}
	q := appmodel.ConversationRef{Authority: "a", ID: "q"}
	key := appmodel.CommandKey{Authority: "a", EnrolledClientID: "c", CommandID: "q"}
	rev := strings.Repeat("a", 64)
	k := NewProjectionKeys([32]byte{})
	foreign := i
	foreign.StoreID[0] = 1
	other := i
	other.IncarnationID[0] = 1
	for _, r := range []Resource{ProjectResource{appmodel.ProjectRef{Authority: "b", ID: "p"}}, LeadResource{appmodel.ProjectRef{Authority: "b", ID: "p"}, "r"}, ConversationResource{p, appmodel.ConversationRef{Authority: "b", ID: "q"}}, ClientResource{foreign, "c"}, ClientResource{other, "c"}, PermissionResource{appmodel.ProjectRef{Authority: "b", ID: "p"}, "c"}, CommandResource{p, key}} {
		if _, err := k.ResourceRevision(i, r, 1); err == nil {
			t.Errorf("accepted %#v", r)
		}
		if err := ValidateConflictView(i, AdminResourceConflict{r, Absent{}}); err == nil {
			t.Errorf("accepted conflict %#v", r)
		}
	}
	for _, v := range []ConflictView{ConversationAdmissionConflict{appmodel.ProjectRef{Authority: "b", ID: "p"}, q, rev}, ConversationAdmissionConflict{p, appmodel.ConversationRef{Authority: "b", ID: "q"}, rev}, ExactEnqueueStatusConflict{appmodel.ProjectRef{Authority: "b", ID: "p"}, key, appmodel.CurrentStatus{Command: key, Revision: appmodel.Revision(rev), State: appmodel.Queued}}} {
		if ValidateConflictView(i, v) == nil {
			t.Errorf("accepted %#v", v)
		}
	}
	foreignKey := key
	foreignKey.Authority = "b"
	if ValidateConflictView(i, ExactEnqueueStatusConflict{p, foreignKey, appmodel.CurrentStatus{Command: foreignKey, Revision: appmodel.Revision(rev), State: appmodel.Queued}}) == nil {
		t.Fatal("foreign command")
	}
	zero := ProjectionKeys{}
	if _, err := zero.ResourceRevision(i, ProjectResource{p}, 1); err == nil {
		t.Error("zero resource key")
	}
	if _, err := zero.StatusRevision(i, key, 1, appmodel.Applied, nil); err == nil {
		t.Error("zero status key")
	}
	if _, err := zero.EventID(i, "p", "q", 1, InputAdmitted); err == nil {
		t.Error("zero event key")
	}
	if _, err := zero.CauseEventID(i, 1); err == nil {
		t.Error("zero cause key")
	}
	if _, err := k.EventID(i, "p", "q", 1, InputAdmitted); err != nil {
		t.Fatal("zero master must be constructible", err)
	}
}

func TestAdminBoundariesAndOwnership(t *testing.T) {
	i := Instance{Authority: "a"}
	rev := strings.Repeat("a", 64)
	key := appmodel.CommandKey{Authority: "a", EnrolledClientID: "c", CommandID: "q"}
	credential := CredentialBinding{Client: "c", Credential: "k", Generation: 1, ExpiresMS: 1, Artifact: ArtifactRef{ID: "art"}}
	max := uint64(math.MaxInt64)
	overflow := max + 1
	zero := uint64(0)
	valid := []AdminOperation{ProjectCreate{strings.Repeat("x", 128), strings.Repeat("é", 128), Absent{}}, ProjectCreate{"A0._:-", "", Absent{}}, LeadCreate{"p", "r", "", Absent{}}, ClientEnroll{credential, None, Absent{}}, CredentialRotate{CredentialBinding{Client: "c", Credential: "k", Generation: max, NotBeforeMS: max - 1, ExpiresMS: max, Artifact: ArtifactRef{ID: "a"}}, Match{rev}}, DispositionCommand{"p", key, Match{rev}, Release, strings.Repeat("é", 512), &max, &zero}}
	for _, op := range valid {
		if _, err := BindAdmin(i, "actor", "cmd", op); err != nil {
			t.Errorf("rejected %#v: %v", op, err)
		}
	}
	var typedNil *Match
	invalid := []AdminOperation{ProjectCreate{strings.Repeat("x", 129), "", Absent{}}, ProjectCreate{"é", "", Absent{}}, ProjectCreate{"bad/", "", Absent{}}, ProjectCreate{"p", strings.Repeat("é", 128) + "x", Absent{}}, ProjectCreate{"p", string([]byte{255}), Absent{}}, ProjectCreate{"p", "", Match{rev}}, LeadCreate{"p", "r", "", Match{rev}}, ConversationCreate{"p", "q", "r", Match{rev}}, ClientEnroll{credential, 0, Absent{}}, ClientEnroll{credential, None, Match{rev}}, CredentialRotate{credential, Absent{}}, ClientRevoke{"c", Absent{}}, ClientRevoke{"c", typedNil}, ClientRevoke{"c", &Match{rev}}, ClientRevoke{"c", Match{strings.Repeat("A", 64)}}, ClientRevoke{"c", Match{rev[:63]}}, ClientRevoke{"c", Match{rev + "a"}}, PermissionsReplace{"p", "c", 0, typedNil}, DispositionCommand{"p", key, Absent{}, Release, "", nil, nil}, DispositionCommand{"p", key, Match{rev}, Release, strings.Repeat("é", 512) + "x", nil, nil}, DispositionCommand{"p", key, Match{rev}, "other", "", nil, nil}, DispositionCommand{"p", appmodel.CommandKey{Authority: "b", EnrolledClientID: "c", CommandID: "q"}, Match{rev}, Release, "", nil, nil}, DispositionCommand{"p", key, Match{rev}, Release, "", &overflow, nil}}
	bad := credential
	bad.Generation = 2
	invalid = append(invalid, ClientEnroll{bad, None, Absent{}})
	bad = credential
	bad.ExpiresMS = 0
	invalid = append(invalid, ClientEnroll{bad, None, Absent{}})
	bad = credential
	bad.ExpiresMS = overflow
	invalid = append(invalid, ClientEnroll{bad, None, Absent{}})
	for _, op := range invalid {
		if b, err := BindAdmin(i, "actor", "cmd", op); err == nil || b.Equal(b) {
			t.Errorf("accepted %#v", op)
		}
	}
	a, err := BindAdmin(i, "actor", "cmd", valid[0])
	if err != nil {
		t.Fatal(err)
	}
	b, _ := BindAdmin(i, "actor", "cmd", valid[0])
	c, _ := BindAdmin(i, "actor", "other", valid[0])
	if !a.Equal(b) || a.Equal(c) || (AdminBinding{}).Equal(AdminBinding{}) {
		t.Fatal("binding equality")
	}
	forged := AdminBinding{canonical: c.canonical, digest: a.digest}
	if a.Equal(forged) {
		t.Fatal("digest-only equality")
	}
	returned := a.Bytes()
	returned[0] ^= 1
	if !a.Equal(b) || bytes.Equal(returned, a.Bytes()) || a.Digest() != b.Digest() {
		t.Fatal("binding ownership")
	}
	if _, err := BindAdmin(Instance{Authority: "bad/"}, "actor", "cmd", valid[0]); err == nil {
		t.Fatal("invalid authority")
	}
	x, _ := BindAdmin(i, "actor", "cmd", DispositionCommand{"p", key, Match{rev}, Release, "", nil, nil})
	y, _ := BindAdmin(i, "actor", "cmd", DispositionCommand{"p", key, Match{rev}, Release, "", &zero, nil})
	if x.Equal(y) {
		t.Fatal("absent and zero")
	}
	before := y.Bytes()
	zero = 1
	if !bytes.Equal(before, y.Bytes()) {
		t.Fatal("binding retained input pointer")
	}
}

func TestProjectionBoundaries(t *testing.T) {
	i := Instance{Authority: "a"}
	k := NewProjectionKeys([32]byte{})
	key := appmodel.CommandKey{Authority: "a", EnrolledClientID: "c", CommandID: "q"}
	max := uint64(math.MaxInt64)
	over := max + 1
	p := ProjectResource{appmodel.ProjectRef{Authority: "a", ID: "p"}}
	if _, err := k.ResourceRevision(i, p, max); err != nil {
		t.Fatal(err)
	}
	if _, err := k.ResourceRevision(i, p, over); err == nil {
		t.Fatal("resource overflow")
	}
	if _, err := k.EventID(i, "p", "q", max, InputAdmitted); err != nil {
		t.Fatal(err)
	}
	if _, err := k.EventID(i, "p", "q", over, InputAdmitted); err == nil {
		t.Fatal("event overflow")
	}
	if _, err := k.CauseEventID(i, max); err != nil {
		t.Fatal(err)
	}
	if _, err := k.CauseEventID(i, over); err == nil {
		t.Fatal("cause overflow")
	}
	if _, err := CredentialVerifier(i, "c", "k", max, [32]byte{}, [32]byte{}); err != nil {
		t.Fatal(err)
	}
	if _, err := CredentialVerifier(i, "c", "k", over, [32]byte{}, [32]byte{}); err == nil {
		t.Fatal("credential overflow")
	}
	r := LossRange{Baseline: max, Acknowledged: max, Upper: max, Cutoff: &max}
	terminal := &EnqueueStatus{Project: "p", Enrollment: r, Enqueue: r}
	if _, err := k.StatusRevision(i, key, max, appmodel.Cancelled, terminal); err != nil {
		t.Fatal(err)
	}
	badCutoff := max - 1
	for _, e := range []*EnqueueStatus{nil, {Project: "p"}, {Project: "p", Enrollment: r}, {Project: "p", Enrollment: LossRange{Upper: max, Cutoff: &badCutoff}, Enqueue: r}, {Project: "p", Enrollment: LossRange{Baseline: 2, Acknowledged: 1, Upper: 2}, Enqueue: LossRange{}}, {Project: "p", Enrollment: LossRange{Upper: over}, Enqueue: LossRange{}}} {
		if _, err := k.StatusRevision(i, key, 1, appmodel.Cancelled, e); err == nil {
			t.Errorf("invalid terminal %#v", e)
		}
	}
	if _, err := k.StatusRevision(i, key, 1, appmodel.Queued, terminal); err == nil {
		t.Fatal("queued cutoff")
	}
	if _, err := k.StatusRevision(i, key, over, appmodel.Applied, nil); err == nil {
		t.Fatal("status overflow")
	}
	rev := strings.Repeat("a", 64)
	for _, d := range []EventDetail{EnqueueDetail{"c", "q", 0, rev, appmodel.Queued}, EnqueueDetail{"c", "q", over, rev, appmodel.Queued}, EnqueueDetail{"c", "q", 1, rev, appmodel.Applied}, DispositionDetail{"c", "q", rev, appmodel.Queued, "unknown"}} {
		if _, err := EncodeEventDetail(d); err == nil {
			t.Errorf("invalid detail %#v", d)
		}
	}
	a, err := EncodeEventDetail(EnqueueDetail{"c", "q", max, rev, appmodel.Queued})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := EncodeEventDetail(EnqueueDetail{"c", "q", max, rev, appmodel.Queued})
	a[0] ^= 1
	if bytes.Equal(a, b) {
		t.Fatal("detail ownership")
	}
}
