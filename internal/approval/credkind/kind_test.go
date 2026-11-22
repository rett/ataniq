package credkind

import (
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

// TestKinds: the three kinds, by name, in order.
func TestKinds(t *testing.T) {
	var names []string
	for _, k := range Kinds() {
		names = append(names, k.Name())
		got, ok := ByName(k.Name())
		if !ok || got.Name() != k.Name() {
			t.Errorf("ByName(%q)", k.Name())
		}
	}
	if strings.Join(names, " ") != "platform-passkey vault-passkey passkey" {
		t.Errorf("kinds %v", names)
	}
	for _, name := range []string{"", "Passkey", "hardware-key", "unverified"} {
		if _, ok := ByName(name); ok {
			t.Errorf("ByName(%q) found a kind", name)
		}
	}
	ks := Kinds()
	ks[0] = nil
	if Kinds()[0] == nil {
		t.Error("Kinds hands out its own list")
	}
}

// TestLookup: the table's AAGUIDs claim their kind and provider, and every
// other AAGUID, the zero one included, claims "passkey (claimed: unknown)".
func TestLookup(t *testing.T) {
	cases := []struct {
		aaguid [16]byte
		kind   string
		label  string
	}{
		{bitwardenAAGUID, NameVault, "vault passkey (claimed: Bitwarden)"},
		{icloudAAGUID, NamePlatform, "platform passkey (claimed: iCloud Keychain)"},
		{[16]byte{}, NameUnknown, "passkey (claimed: unknown)"},
		{[16]byte{1}, NameUnknown, "passkey (claimed: unknown)"},
		{func() [16]byte { b := bitwardenAAGUID; b[15] ^= 1; return b }(), NameUnknown, "passkey (claimed: unknown)"},
	}
	for _, tc := range cases {
		c := Lookup(tc.aaguid)
		if c.Kind != tc.kind || c.String() != tc.label {
			t.Errorf("%x: %+v %q", tc.aaguid, c, c.String())
		}
		if got := Classify(tc.aaguid).Name(); got != tc.kind {
			t.Errorf("%x: classified %s", tc.aaguid, got)
		}
		if (Credential{AAGUID: tc.aaguid}).Claim() != c {
			t.Errorf("%x: the credential's claim", tc.aaguid)
		}
	}
	for _, row := range aaguidTable {
		id, _ := parseAAGUID(row.aaguid)
		c := Lookup(id)
		if c.Kind != row.kind || c.Provider != row.provider || !strings.Contains(c.String(), "claimed: "+row.provider) {
			t.Errorf("%s: %+v", row.aaguid, c)
		}
	}
	if len(aaguids) != len(aaguidTable) {
		t.Error("the table lost a row")
	}
}

// TestTableFaults: a bad table stops the program at start.
func TestTableFaults(t *testing.T) {
	good := aaguidRow{"d548826e-79b4-db40-a3d8-11116f7e8349", NameVault, "V"}
	for name, rows := range map[string][]aaguidRow{
		"malformed":    {{"d548826e79b4db40a3d811116f7e8349", NameVault, "V"}},
		"zero":         {{"00000000-0000-0000-0000-000000000000", NameVault, "V"}},
		"twice":        {good, good},
		"unknown kind": {{good.aaguid, NameUnknown, "V"}},
		"no provider":  {{good.aaguid, NameVault, ""}},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: no panic", name)
				}
			}()
			buildAAGUIDs(rows)
		}()
	}
	if len(buildAAGUIDs([]aaguidRow{good})) != 1 {
		t.Error("a good row")
	}
}

// TestParseAAGUID: only the lowercase 8-4-4-4-12 form.
func TestParseAAGUID(t *testing.T) {
	id, ok := parseAAGUID("00112233-4455-6677-8899-aabbccddeeff")
	if !ok || id != [16]byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff} {
		t.Errorf("%x %v", id, ok)
	}
	for _, s := range []string{
		"", "00112233-4455-6677-8899-AABBCCDDEEFF", "001122334-455-6677-8899-aabbccddeeff",
		"00112233-4455-6677-8899-aabbccddeef", "00112233-4455-6677-8899-aabbccddeefg",
		"{0112233-4455-6677-8899-aabbccddeef}", "00112233+4455-6677-8899-aabbccddeeff",
		"00112233-4455-6677-8899-aabbccddeeff0", "00112233-4455-6677-8899-aabbccdd-eff",
	} {
		if _, ok := parseAAGUID(s); ok {
			t.Errorf("%q accepted", s)
		}
	}
}

// TestPolicy: the zero policy accepts every kind; a list accepts only its
// kinds; a name that is no kind matches nothing, and Check finds it.
func TestPolicy(t *testing.T) {
	all := Policy{}
	for _, k := range Kinds() {
		if !all.Accepts(k.Name()) {
			t.Errorf("the zero policy refuses %s", k.Name())
		}
	}
	if all.Accepts("hardware-key") || all.Accepts("") {
		t.Error("the zero policy accepts a kind that does not exist")
	}
	vault := Policy{Accepted: []string{NameVault}}
	if !vault.Accepts(NameVault) || vault.Accepts(NamePlatform) || vault.Accepts(NameUnknown) {
		t.Error("a policy of one kind")
	}
	typo := Policy{Accepted: []string{"vault-paskey"}}
	for _, k := range Kinds() {
		if typo.Accepts(k.Name()) {
			t.Errorf("a policy of a typo accepts %s", k.Name())
		}
	}
	for name, p := range map[string]Policy{
		"zero": {}, "empty": {Accepted: []string{}}, "one": vault,
		"all": {Accepted: []string{NamePlatform, NameVault, NameUnknown}},
	} {
		if err := p.Check(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, p := range map[string]Policy{
		"typo": typo, "twice": {Accepted: []string{NameVault, NameVault}}, "empty name": {Accepted: []string{""}},
	} {
		wantErr(t, name, p.Check(), ErrPolicy)
	}
}

// TestPolicyRegisterVerify: a policy refuses a kind it does not accept, at
// registration and at every assertion, and passes the others through.
func TestPolicyRegisterVerify(t *testing.T) {
	a := newAuth(t)
	a.AAGUID = bitwardenAAGUID
	in := regInput(a.Create(chal(1)))
	platform := Policy{Accepted: []string{NamePlatform}}
	_, err := platform.Register(in)
	wantErr(t, "register", err, ErrNotAccepted)
	c, err := Policy{Accepted: []string{NameVault}}.Register(in)
	if err != nil || c.Kind != NameVault {
		t.Fatalf("%+v %v", c, err)
	}
	as := assertion(t, a, chal(2))
	_, err = platform.Verify(c, as, expect(chal(2)))
	wantErr(t, "verify", err, ErrNotAccepted)
	if _, err := (Policy{}).Verify(c, as, expect(chal(2))); err != nil {
		t.Error(err)
	}
	// The policy does not hide a bad assertion or a bad registration.
	_, err = (Policy{}).Verify(c, as, expect(chal(3)))
	wantErr(t, "verify, other challenge", err, ErrChallenge)
	in.RPID = "other.example.test"
	_, err = (Policy{}).Register(in)
	wantErr(t, "register, other relying party", err, ErrRPID)
	c.Kind = "hardware-key"
	_, err = (Policy{Accepted: []string{"hardware-key"}}).Verify(c, as, expect(chal(2)))
	wantErr(t, "no such kind", err, ErrKind)
}

// TestStandardLibraryOnly: the package imports the standard library only;
// its tests may also import the software authenticator.
func TestStandardLibraryOnly(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	files := 0
	for _, pkg := range pkgs {
		for name, f := range pkg.Files {
			files++
			for _, imp := range f.Imports {
				path, _ := strconv.Unquote(imp.Path.Value)
				if strings.HasSuffix(name, "_test.go") && path == "ataniq/internal/approval/webauthntest" {
					continue
				}
				first, _, _ := strings.Cut(path, "/")
				if strings.Contains(first, ".") || first == "ataniq" || first == "C" {
					t.Errorf("%s imports %s, which is not the standard library", name, path)
				}
			}
		}
	}
	if files < 8 {
		t.Errorf("only %d files read", files)
	}
}
