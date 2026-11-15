package appcontract

import (
	"ataniq/internal/appmodel"
	"math"
	"testing"
)

func TestResourceRevisions(t *testing.T) {
	i := Instance{Authority: "a"}
	var master [32]byte
	for n := 0; n < 16; n++ {
		i.StoreID[n], i.IncarnationID[n] = byte(n), byte(n+16)
	}
	for n := range master {
		master[n] = byte(n + 32)
	}
	k := NewProjectionKeys(master)
	p := appmodel.ProjectRef{Authority: "a", ID: "p"}
	q := appmodel.ConversationRef{Authority: "a", ID: "q"}
	for _, tc := range []struct {
		r       Resource
		version uint64
		want    string
	}{
		{ProjectResource{p}, 1, "c79395018f01b76b2515d00c3c00e5b77ccc7462dcdbddb8b3216dcec5e17700"},
		{LeadResource{p, "lead"}, 1, "d648ec77f8654002348dd70c6fed0cf67bfc61a3b6e8c6803c74d98ef0b1e0ee"},
		{ConversationResource{p, q}, 1, "d7dec8a81cb7d644f771bbab9ccfefe07e2402351a8f7d4f20b3f5a060b6194a"},
		{ClientResource{i, "c"}, 1, "82efcc3cd3284ed6be9eda8ad7825308e86d27871b25fa418a5d632791716745"},
		{PermissionResource{p, "c"}, 1, "31c5a89f5a0a43fd990443026eeb185775df928ad88c0668bee026eabde57743"},
		{ClientResource{i, "owner"}, 1, "7620356f980ecf941cddb369ed7086b8202cbbc1aa57c2cc5bd146af38d44bc5"},
		{ClientResource{i, "owner"}, 2, "fbc70c63c508c90020089954fff2bc485e277a2372208eec2c313d38d68cb5d2"},
	} {
		got, err := k.ResourceRevision(i, tc.r, tc.version)
		if err != nil || got != tc.want {
			t.Errorf("%T: %s, %v; want %s", tc.r, got, err, tc.want)
		}
	}
	for _, tc := range []struct {
		r Resource
		v uint64
	}{
		{nil, 1}, {&ProjectResource{p}, 1}, {ProjectResource{appmodel.ProjectRef{}}, 1},
		{PermissionResource{p, ""}, 1}, {ProjectResource{p}, 0}, {ProjectResource{p}, math.MaxUint64},
	} {
		if _, err := k.ResourceRevision(i, tc.r, tc.v); err == nil {
			t.Errorf("accepted invalid %#v", tc)
		}
	}
}
