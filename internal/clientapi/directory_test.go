package clientapi

import "testing"

func TestDirectoryTargetEncoding(t *testing.T) {
	target := DirectoryTarget{Authority: "ab", ProjectID: "é", LeadRole: ":lead",
		HomeGeneration: 1, Revision: 20, LaunchEpoch: 0}
	if got, want := target.ID(), "lead/2:ab2:é5::lead1:12:201:0"; got != want {
		t.Fatalf("target = %q, want %q", got, want)
	}
	// Moving a field boundary preserves the concatenation of its bytes.
	// It must change the identity, as must each generation or epoch.
	for _, change := range []func(*DirectoryTarget){
		func(v *DirectoryTarget) { v.Authority, v.ProjectID = "a", "bé" },
		func(v *DirectoryTarget) { v.ProjectID, v.LeadRole = "é:", "lead" },
		func(v *DirectoryTarget) { v.HomeGeneration++ },
		func(v *DirectoryTarget) { v.Revision++ },
		func(v *DirectoryTarget) { v.LaunchEpoch++ },
	} {
		v := target
		change(&v)
		if v.ID() == target.ID() {
			t.Errorf("target collision for %+v and %+v", target, v)
		}
	}
}
