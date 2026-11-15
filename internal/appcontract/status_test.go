package appcontract

import (
	"crypto/sha256"
	"fmt"
	"testing"

	"ataniq/internal/appmodel"
)

func TestStatusAndDetailVectors(t *testing.T) {
	i := Instance{Authority: "a"}
	var master [32]byte
	for n := 0; n < 16; n++ {
		i.StoreID[n], i.IncarnationID[n] = byte(n), byte(n+16)
	}
	for n := range master {
		master[n] = byte(n + 32)
	}
	k := NewProjectionKeys(master)
	key := appmodel.CommandKey{Authority: "a", EnrolledClientID: "c", CommandID: "q"}
	zero, one := uint64(0), uint64(1)
	for _, tc := range []struct {
		version uint64
		state   appmodel.State
		e       *EnqueueStatus
		want    string
	}{
		{1, appmodel.Queued, &EnqueueStatus{Project: "p"}, "96796e2fb38daf7c81e4daa100dea9c6859c7b70152ad4dbb624e3461a652eb1"},
		{1, appmodel.Queued, &EnqueueStatus{Project: "p", Enrollment: LossRange{Upper: 1}}, "33e6c7e7416544523991a7780643673df47d9a693c89e882b4e512c7d1adafdd"},
		{1, appmodel.Queued, &EnqueueStatus{Project: "p", Enqueue: LossRange{Upper: 1}}, "ae1b3af8ec27b2ccc776c3aefc89c032534e182abbddcc9731618671db66d566"},
		{2, appmodel.Cancelled, &EnqueueStatus{Project: "p", Enrollment: LossRange{Upper: 1, Cutoff: &one}, Enqueue: LossRange{Cutoff: &zero}}, "20a78a708a59b722cd710225fee0d79573fbd95ea0bd94cdf6cc69bed071062b"},
		{1, appmodel.Applied, nil, "728f166f82aa52bbe70b4f30279eb557d91c0107d497eca5f5c63dda88b82d70"},
	} {
		got, err := k.StatusRevision(i, key, tc.version, tc.state, tc.e)
		if err != nil || got != tc.want {
			t.Errorf("status: %s, %v; want %s", got, err, tc.want)
		}
	}
	if _, err := k.StatusRevision(i, key, 1, appmodel.Held, &EnqueueStatus{Project: "p"}); err == nil {
		t.Error("accepted held base state")
	}
	if _, err := k.StatusRevision(i, key, 1, appmodel.Queued, &EnqueueStatus{Project: "p", Enrollment: LossRange{Acknowledged: 1}}); err == nil {
		t.Error("accepted invalid loss range")
	}
	queued := "96796e2fb38daf7c81e4daa100dea9c6859c7b70152ad4dbb624e3461a652eb1"
	cancelled := "20a78a708a59b722cd710225fee0d79573fbd95ea0bd94cdf6cc69bed071062b"
	for _, tc := range []struct {
		d    EventDetail
		want string
	}{
		{EnqueueDetail{"c", "q", 1, queued, appmodel.Queued}, "6fbadf887c4d1c53cadc6553d00b688521155f5b7449236d634056f1d5b3c76a"},
		{CancelDetail{"c", "q", cancelled}, "5073854a43d94982e93d96b5517cd39fac257d34d80b292a3c40286ab4da5016"},
		{DispositionDetail{"c", "q", queued, appmodel.Queued, Unchanged}, "6f84b8dc0de41b6922775e03f70c4a4a10b72b7a55b9d9d2a74fc3beea673324"},
		{EmptyDetail{}, "06e2cd509f9ad5b377005156eaa460390ec6dfa01a2013916a88d8c565c099a4"},
	} {
		b, err := EncodeEventDetail(tc.d)
		sum := sha256.Sum256(b)
		if err != nil || fmt.Sprintf("%x", sum) != tc.want {
			t.Errorf("detail %T: %x, %v", tc.d, sum, err)
		}
	}
	if _, err := EncodeEventDetail(&EmptyDetail{}); err == nil {
		t.Error("accepted pointer variant")
	}
	if _, err := EncodeEventDetail(CancelDetail{"c", "q", "bad"}); err == nil {
		t.Error("accepted malformed revision")
	}
}
