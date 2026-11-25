package all_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	lp "ataniq/internal/launchpolicy"
	"ataniq/internal/pack"
	"ataniq/packs/all"
	"ataniq/packs/holder"
)

type build struct {
	name                        pack.Name
	version, older, captureHash string
}

var builds = []build{
	{"claude", "2.1.332", "2.1.325", "b296ad20ed563e8c57033bd8570724d63a508dd5a7c1de126e95edcbc73461f3"},
	{"codex", "0.172.0", "0.169", "01d84a04796243348f9ae37cad9b4693451ef85aa444bb002583f97f600cb991"},
}

func read(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (b build) load(t *testing.T) ([]byte, []byte, *lp.Policy) {
	t.Helper()
	doc, help, ok := holder.LaunchPolicy(b.name, b.version)
	if !ok {
		t.Fatal("missing embedded build", b)
	}
	p, err := lp.Decode(doc, help)
	if err != nil {
		t.Fatal(err)
	}
	return doc, help, p
}

func digest(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }

func TestCommittedLaunchPolicies(t *testing.T) {
	r := pack.New()
	if err := all.Register(r); err != nil {
		t.Fatal(err)
	}
	for _, b := range builds {
		t.Run(string(b.name), func(t *testing.T) {
			doc, help, p := b.load(t)
			root := filepath.Join("..", string(b.name))
			dir := filepath.Join(root, "launchpolicy")
			if !bytes.Equal(doc, read(t, filepath.Join(dir, b.version+".json"))) ||
				!bytes.Equal(help, read(t, filepath.Join(dir, b.version+".help.txt"))) {
				t.Fatal("holder listing differs from the pack")
			}
			if bytes.Contains(bytes.ToLower(help), []byte("stand-in")) || digest(help) != b.captureHash {
				t.Fatal("fixture differs from the captured help")
			}
			var d struct {
				Pack    pack.Name
				Version string
				Fixture struct{ Path, SHA256 string }
				Slots   struct {
					Permission struct {
						Values []struct{ Vector map[string]string }
					} `json:"permission_mode"`
				}
			}
			if err := json.Unmarshal(doc, &d); err != nil {
				t.Fatal(err)
			}
			if d.Pack != b.name || d.Version != b.version || d.Fixture.Path != b.version+".help.txt" || d.Fixture.SHA256 != "" {
				t.Fatal("wrong build, fixture or recording state")
			}
			for _, m := range d.Slots.Permission.Values {
				if len(m.Vector) != 1 || m.Vector["terminal"] != "pending" {
					t.Fatal("unexpected observation", m)
				}
			}
			golden := read(t, filepath.Join("testdata", "launchpolicy", string(b.name), b.version+".golden"))
			if !bytes.Equal(lp.Canonical(p), golden) || p.Digest() != digest(golden) {
				t.Fatal("canonical launch policy differs from golden")
			}
			t.Logf("launch_policy_digest=%s", p.Digest())
			full, ok := r.Lookup(b.name)
			if !ok {
				t.Fatal("missing registration")
			}
			var versions []string
			for _, row := range full.Manifest().Builds {
				versions = append(versions, row.Version)
				if row.Version == b.version {
					continue
				}
				for _, version := range append(slices.Clone(row.Aliases), row.Version) {
					if _, _, ok := holder.LaunchPolicy(b.name, version); ok {
						t.Fatal("unexpected policy", version)
					}
					_, ref := lp.Recheck(nil, lp.Spec{}, lp.Snapshot{Pack: b.name, Version: row.Version}, nil)
					if ref == nil || ref.Error() != "launch refused [pack-launch-policy-missing] stage=0 at=policy" {
						t.Fatal(ref)
					}
				}
			}
			if !slices.Contains(versions, b.older) || !slices.Contains(versions, b.version) {
				t.Fatal(versions)
			}
			_, ref := lp.Recheck(p, lp.Spec{}, lp.Snapshot{Pack: b.name, Version: b.version}, nil)
			if ref == nil || ref.Code != "policy-unrecorded" {
				t.Fatal(ref)
			}
			// Boundary (i), (iv): a declaration can neither carry nor select a policy.
			manifest := read(t, filepath.Join(root, pack.ManifestPath))
			for _, docs := range []map[string][]byte{
				{pack.ManifestPath: manifest, "launchpolicy/" + b.version + ".json": doc},
				{pack.ManifestPath: bytes.Replace(manifest, []byte(`"builds":`), []byte(`"launch_policy":"replacement","builds":`), 1)},
			} {
				if _, err := pack.DecodeDeclaration(docs, pack.BindLayout{}); err == nil {
					t.Fatal("declaration supplied a policy")
				}
			}
			// With the original bytes and hash, qualification next encounters
			// pending evidence. A prose-only change must fail at the hash first.
			recorded := bytes.Replace(doc, []byte(`"sha256": ""`), []byte(fmt.Sprintf(`"sha256": "%s"`, b.captureHash)), 1)
			if _, err := lp.Decode(recorded, help); err == nil || !strings.Contains(err.Error(), "evidence") {
				t.Fatalf("recording with pending evidence: %v", err)
			}
			changed := bytes.Clone(help)
			changed[0] ^= 1
			if _, err := lp.Decode(recorded, changed); err == nil || err.Error() != "fixture hash differs" {
				t.Fatalf("changed fixture: %v", err)
			}
		})
	}
}
