package leadregistry

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func samplePack() PackRef {
	return PackRef{Name: "sample-pack", ManifestDigest: strings.Repeat("ab", 32)}
}

// TestPackRefIsLeftOutWhenZero: a profile that names no pack is encoded, and
// digested, exactly as before the member existed; one that names a pack has
// the member and another digest.
func TestPackRefIsLeftOutWhenZero(t *testing.T) {
	p := goodProfile()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte(`"pack"`)) {
		t.Fatalf("a profile without a pack encodes one: %s", b)
	}
	before := `{"provider":"claude","version":"2.1.0","model":"model-a","effort":"high","permission_mode":"default","access_mode":"workspace"}`
	if string(b) != before {
		t.Fatalf("encoding %s, want %s", b, before)
	}
	withPack := p
	withPack.Pack = samplePack()
	b2, err := json.Marshal(withPack)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(b2, []byte(`,"pack":{"name":"sample-pack","manifest_digest":"`+strings.Repeat("ab", 32)+`"}}`)) {
		t.Fatalf("encoding with a pack: %s", b2)
	}
	if withPack.Digest() == p.Digest() {
		t.Fatal("the pack does not change the digest")
	}
}

// TestDefineRefusesAPack: packs are not wired, so a profile that names one is
// refused and nothing is written.
func TestDefineRefusesAPack(t *testing.T) {
	dir := regDir(t)
	r := openReg(t, dir)
	size := journalSize(t, dir)
	p := goodProfile()
	p.Pack = samplePack()
	if _, err := r.Define(lead(t, "alpha"), ManagerAtaniq, p); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Define with a pack = %v", err)
	}
	if journalSize(t, dir) != size {
		t.Fatal("the refusal wrote")
	}
}

// TestRecordedPackReplaysAndIsNotLaunched: a define record that names a pack,
// as a later build may write, replays (the member is known, the profile is not
// foreign), and this build refuses to launch it.
func TestRecordedPackReplaysAndIsNotLaunched(t *testing.T) {
	dir := regDir(t)
	openReg(t, dir).Close()
	data, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	alpha := lead(t, "alpha")
	p := goodProfile()
	p.Pack = samplePack()
	data = appendRecord(t, data, kindDefine, alpha, &definePayload{Manager: ManagerAtaniq, Requested: p})
	r := openReg(t, writeJournal(t, data))
	e, err := r.Get(alpha)
	if err != nil || e.Requested != p || e.foreignProfile || e.profileDigest != p.Digest() {
		t.Fatalf("%+v %v", e, err)
	}
	if _, err := r.BeginLaunch(alpha); !errors.Is(err, ErrLaunchProfile) {
		t.Fatalf("BeginLaunch = %v, want ErrLaunchProfile", err)
	}
	// A bad pack reference in a record is damage.
	bad := p
	bad.Pack.ManifestDigest = "not-a-digest"
	data2, _ := os.ReadFile(filepath.Join(dir, FileName))
	if err := openErr(t, appendRecord(t, data2, kindDefine, alpha, &definePayload{Manager: ManagerAtaniq, Requested: bad})); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("a bad pack reference: %v", err)
	}
}

// TestPackSessionRefShapeIsReserved keeps the reserved shape of a pack session
// reference (federation review ID-11) as it was reviewed. It is unused: a change
// here is a change of the reservation, made on purpose.
func TestPackSessionRefShapeIsReserved(t *testing.T) {
	want := []string{
		"authority string", "storeIncarnation string", "project string", "homeGeneration uint64",
		"role string", "conversation string", "roleEpoch uint64", "bindingGeneration uint64",
		"pack leadregistry.PackRef", "signerGeneration uint64", "profileDigest string",
		"transport string", "custody string",
	}
	typ := reflect.TypeOf(packSessionRef{})
	var got []string
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.IsExported() || f.Tag != "" {
			t.Errorf("%s is exported or has a tag: the shape has no wire form yet", f.Name)
		}
		got = append(got, f.Name+" "+f.Type.String())
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("packSessionRef is\n%v\nreviewed as\n%v", got, want)
	}
}
