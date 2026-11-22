//go:build linux && ataniq_ownerchannel

package hostadapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scratchJSON is the settings text with the pieces in place, so that a test
// can add a repeated key to one object.
func scratchJSON(door, identity, seat, gate string) string {
	return `{"project":"project-a","mirror":"/scratch/mirror.git","role":"role-a","target":"refs/heads/develop",` +
		identity + `,"gates":[` + gate + `],"drop":"/scratch/drop",` + seat + `,` + door + `,"roots":["/scratch"]}`
}

const (
	okDoor     = `"door":"stand-in"`
	okIdentity = `"identity":{"name":"Host Identity","email":"host@example.invalid"}`
	okSeat     = `"seat":{"epoch":7,"seat_rev":12}`
	okGate     = `{"name":"suite","argv":["make","test"]}`
)

func loadScratchText(t *testing.T, text string) error {
	t.Helper()
	path := filepath.Join(t.TempDir(), ScratchName)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadScratch(path)
	return err
}

// A key that appears twice in an object of the settings is refused, at the top
// level and in the objects below it, even when the last value is the right one.
func TestTheScratchSettingsRefuseARepeatedKey(t *testing.T) {
	if err := loadScratchText(t, scratchJSON(okDoor, okIdentity, okSeat, okGate)); err != nil {
		t.Fatalf("the control was refused: %v", err)
	}
	for name, text := range map[string]string{
		"the door, real then stand-in":   scratchJSON(`"door":"real","door":"stand-in"`, okIdentity, okSeat, okGate),
		"the door, by case":              scratchJSON(`"door":"real","Door":"stand-in"`, okIdentity, okSeat, okGate),
		"a key of the identity, by case": scratchJSON(okDoor, `"identity":{"name":"A","Name":"B","email":"host@example.invalid"}`, okSeat, okGate),
		"the door, stand-in twice":       scratchJSON(`"door":"stand-in","door":"stand-in"`, okIdentity, okSeat, okGate),
		"a key of the identity":          scratchJSON(okDoor, `"identity":{"name":"A","name":"B","email":"host@example.invalid"}`, okSeat, okGate),
		"a key of the seat":              scratchJSON(okDoor, okIdentity, `"seat":{"epoch":7,"epoch":8,"seat_rev":12}`, okGate),
		"a key of a gate":                scratchJSON(okDoor, okIdentity, okSeat, `{"name":"suite","argv":["make"],"argv":["true"]}`),
		"a key of the second gate":       scratchJSON(okDoor, okIdentity, okSeat, okGate+`,{"name":"a","name":"b","argv":["x"]}`),
	} {
		err := loadScratchText(t, text)
		if err == nil || !strings.Contains(err.Error(), "appears twice") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The same key in two different objects is no repetition.
	text := scratchJSON(okDoor, okIdentity, okSeat, okGate+`,`+strings.Replace(okGate, "suite", "second", 1))
	if err := loadScratchText(t, text); err != nil {
		t.Errorf("the same key in two objects: %v", err)
	}
}

// The settings may name the kinds of credential the project accepts. Without
// the key, or with an empty list, every kind is accepted; a name that is not a
// kind, or a kind twice, refuses the settings.
func TestTheScratchSettingsNameTheAcceptedKinds(t *testing.T) {
	load := func(t *testing.T, kinds string) (Scratch, error) {
		t.Helper()
		text := scratchJSON(okDoor, okIdentity, okSeat, okGate)
		if kinds != "" {
			text = strings.TrimSuffix(text, "}") + `,"kinds":` + kinds + `}`
		}
		path := filepath.Join(t.TempDir(), ScratchName)
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		return LoadScratch(path)
	}
	for _, kinds := range []string{"", `[]`} {
		s, err := load(t, kinds)
		if err != nil {
			t.Fatalf("%q: %v", kinds, err)
		}
		p := s.KindPolicy()
		for _, k := range []string{"platform-passkey", "vault-passkey", "passkey"} {
			if !p.Accepts(k) {
				t.Errorf("%q: the kind %s is not accepted", kinds, k)
			}
		}
		if p.Accepts("unverified") {
			t.Errorf("%q: the kind unverified is accepted", kinds)
		}
	}
	s, err := load(t, `["vault-passkey","platform-passkey"]`)
	if err != nil {
		t.Fatal(err)
	}
	if p := s.KindPolicy(); !p.Accepts("vault-passkey") || !p.Accepts("platform-passkey") || p.Accepts("passkey") {
		t.Errorf("the policy %+v", p)
	}
	for name, kinds := range map[string]string{
		"a kind that is not one": `["hardware-key"]`,
		"unverified":             `["unverified"]`,
		"a kind twice":           `["passkey","passkey"]`,
		"a kind in capitals":     `["Passkey"]`,
		"not a list":             `"passkey"`,
	} {
		if _, err := load(t, kinds); err == nil {
			t.Errorf("%s: the settings were taken", name)
		}
	}
}
