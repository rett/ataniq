package appcontract

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"ataniq/internal/appmodel"
)

func TestAdminVectors(t *testing.T) {
	var corpus struct {
		Admin map[string]struct{ Canonical struct{ Hex string } }
	}
	data, err := os.ReadFile("testdata/synthetic-v2.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	names := []string{"project_create", "lead_create", "conversation_create", "client_enroll_scope_0", "client_enroll_scope_1", "fixture_revoke_bootstrap", "credential_rotate", "client_revoke", "permissions_match", "permissions_absent", "disposition_cancel_resolve", "disposition_release"}
	i := Instance{Authority: "a"}
	for n := 0; n < 16; n++ {
		i.StoreID[n], i.IncarnationID[n] = byte(n), byte(n+16)
	}
	var digest [32]byte
	for n := range digest {
		digest[n] = byte(n + 128)
	}
	c := CredentialBinding{Client: "c", Credential: "k", Generation: 1, ExpiresMS: 1, Artifact: ArtifactRef{"art", digest}}
	rotate := CredentialBinding{Client: "c", Credential: "k2", Generation: 2, ExpiresMS: 100, Artifact: ArtifactRef{"art2", digest}}
	clientRev := "82efcc3cd3284ed6be9eda8ad7825308e86d27871b25fa418a5d632791716745"
	permRev := "31c5a89f5a0a43fd990443026eeb185775df928ad88c0668bee026eabde57743"
	one := uint64(1)
	target := appmodel.CommandKey{Authority: "a", EnrolledClientID: "c", CommandID: "q"}
	for index, tc := range []struct {
		actor, command string
		op             AdminOperation
		length         int
		hash           string
	}{
		{"owner", "p1", ProjectCreate{"p", "P", Absent{}}, 107, "a50a4ce80b8e7b01d7c34c6f4bcf9da92db5e58af6c3abe39d54225cd17bb53e"},
		{"owner", "l1", LeadCreate{"p", "lead", "Lead", Absent{}}, 115, "a9456b7f4ade1325e156ce2883644bcfcce227bea3f7dc75a128593aea4017f4"},
		{"owner", "q1", ConversationCreate{"p", "q", "lead", Absent{}}, 120, "41fb89e4a2e57f0ee394268760bd70c70baaf44c7434208318fcf4b98d110120"},
		{"owner", "enroll-c", ClientEnroll{c, None, Absent{}}, 196, "694723e1c26aca3e8bb2a7cee91e6df0a0f1876d74e751e62f3322ab9886a3af"},
		{"owner", "enroll-c", ClientEnroll{c, OwnerAdministration, Absent{}}, 196, "41b8580ac62301413e4d03d7f183ee2dd9725258ecdc62f514185c67467d3f59"},
		{"c", "revoke-owner", ClientRevoke{"owner", Match{"7620356f980ecf941cddb369ed7086b8202cbbc1aa57c2cc5bd146af38d44bc5"}}, 143, "fd18e8ad1a91dd23584963085af3f053f0660314061876d6d9c373d79dd8b663"},
		{"owner", "rotate-c", CredentialRotate{rotate, Match{clientRev}}, 229, "0ddabb7d39a9a9934c760a126c37a92cb1cca7a589de10e7c170b4c910515dc0"},
		{"owner", "revoke-c", ClientRevoke{"c", Match{clientRev}}, 139, "381283504a51c3f10e9c954441dcbd738ef601f68c23283cec91ea1530bbbe0a"},
		{"owner", "perm-c", PermissionsReplace{"p", "c", 2, Match{permRev}}, 161, "61dd1f6f3ba77702060ed6db830d99c40868925b599f69b2854fe6bbe7d5259c"},
		{"owner", "perm-c", PermissionsReplace{"p", "c", 2, Absent{}}, 129, "fda5e1df61d3ab886356d832d5430d0ef37f69b643e82dba26b6ce57e0d098b3"},
		{"owner", "resolve-q", DispositionCommand{"p", target, Match{"33e6c7e7416544523991a7780643673df47d9a693c89e882b4e512c7d1adafdd"}, CancelAndResolve, "review", &one, nil}, 208, "4ca4378f1d9ae7d5db03b3dc57aa32f015068e42dafc9a98e4a3da8b9ed2d2c3"},
		{"owner", "release-q", DispositionCommand{"p", target, Match{"ae1b3af8ec27b2ccc776c3aefc89c032534e182abbddcc9731618671db66d566"}, Release, "review", nil, &one}, 197, "650653331811b30315c791a921e8e7ef823c3b83d30303520c551074a1302a16"},
	} {
		binding, err := BindAdmin(i, tc.actor, tc.command, tc.op)
		b, h := binding.Bytes(), binding.Digest()
		if err != nil || len(b) != tc.length || fmt.Sprintf("%x", h) != tc.hash {
			t.Errorf("%T: length %d, digest %x, err %v", tc.op, len(b), h, err)
		}
		if hex.EncodeToString(b) != corpus.Admin[names[index]].Canonical.Hex {
			t.Errorf("%s: canonical bytes differ", names[index])
		}
	}
	for _, op := range []AdminOperation{nil, &ProjectCreate{}, ProjectCreate{"p", "x", nil}, ClientEnroll{c, 3, Absent{}}, PermissionsReplace{"p", "c", 16, Absent{}}, ClientRevoke{"c", Absent{}}, ProjectCreate{"p", string([]byte{0xff}), Absent{}}} {
		if _, err := BindAdmin(i, "owner", "x", op); err == nil {
			t.Errorf("accepted invalid %T", op)
		}
	}
}
