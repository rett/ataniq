package credkind

import (
	"encoding/hex"
	"strings"
)

// The table of AAGUIDs is data. Each line is the AAGUID an authenticator
// claims and the provider's name; the package depends on none of them. The
// values follow the public, community-kept list of passkey providers'
// AAGUIDs and are claims, not attested: a value missing here, or an
// authenticator that sends the all-zero AAGUID (as some platforms do), gives
// the kind "passkey", shown as "passkey (claimed: unknown)". Check a new
// entry against a real registration before adding it.
var aaguidTable = []aaguidRow{
	// Passkeys of a device's own platform.
	{"fbfc3007-154e-4ecc-8c0b-6e020557d7bd", NamePlatform, "iCloud Keychain"},
	{"ea9b8d66-4d01-1d21-3ce4-b6b48cb575d4", NamePlatform, "Google Password Manager"},
	{"08987058-cadc-4b81-b6e1-30de50dcbe96", NamePlatform, "Windows Hello"},
	{"9ddd1817-af5a-4672-a2b9-3e3dd95000a9", NamePlatform, "Windows Hello"},
	{"6028b017-b1d4-4c02-b4b3-afcdafc96bb2", NamePlatform, "Windows Hello"},

	// Passkeys kept by a password vault.
	{"d548826e-79b4-db40-a3d8-11116f7e8349", NameVault, "Bitwarden"},
	{"bada5566-a7aa-401f-bd96-45619a55120d", NameVault, "1Password"},
	{"531126d6-e717-415c-9320-3d9aa6981239", NameVault, "Dashlane"},
	{"fdb141b2-5d84-443e-8a35-4698c205a502", NameVault, "KeePassXC"},
	{"50726f74-6f6e-5061-7373-50726f746f6e", NameVault, "Proton Pass"},
}

// aaguidRow is one row of the table as written.
type aaguidRow struct {
	aaguid   string
	kind     string
	provider string
}

// aaguidEntry is one row of the table, parsed.
type aaguidEntry struct {
	kind     string
	provider string
}

// aaguids is the table by AAGUID, built once.
var aaguids = buildAAGUIDs(aaguidTable)

// buildAAGUIDs parses the table. A malformed or repeated AAGUID, the all-zero
// one, or a kind that is not a table kind is a fault of the table itself and
// stops the program at start; a test holds the table well formed.
func buildAAGUIDs(rows []aaguidRow) map[[16]byte]aaguidEntry {
	m := make(map[[16]byte]aaguidEntry, len(rows))
	for _, row := range rows {
		id, ok := parseAAGUID(row.aaguid)
		if !ok || id == ([16]byte{}) {
			panic("credkind: bad AAGUID in the table: " + row.aaguid)
		}
		if _, dup := m[id]; dup {
			panic("credkind: AAGUID twice in the table: " + row.aaguid)
		}
		if (row.kind != NamePlatform && row.kind != NameVault) || row.provider == "" {
			panic("credkind: bad row in the table: " + row.aaguid)
		}
		m[id] = aaguidEntry{kind: row.kind, provider: row.provider}
	}
	return m
}

// parseAAGUID parses the lowercase 8-4-4-4-12 form of an AAGUID.
func parseAAGUID(s string) ([16]byte, bool) {
	var id [16]byte
	if len(s) != 36 || s != strings.ToLower(s) {
		return id, false
	}
	for _, i := range []int{8, 13, 18, 23} {
		if s[i] != '-' {
			return id, false
		}
	}
	plain := strings.ReplaceAll(s, "-", "")
	if len(plain) != 32 {
		return id, false
	}
	if _, err := hex.Decode(id[:], []byte(plain)); err != nil {
		return id, false
	}
	return id, true
}

// Claim is what an AAGUID claims: the kind, and the provider when the table
// knows it ("" otherwise).
type Claim struct {
	Kind     string
	Provider string
}

// Lookup returns the claim of an AAGUID. An AAGUID the table does not hold,
// the all-zero one included, claims the kind "passkey" and no provider.
func Lookup(aaguid [16]byte) Claim {
	if e, ok := aaguids[aaguid]; ok {
		return Claim{Kind: e.kind, Provider: e.provider}
	}
	return Claim{Kind: NameUnknown}
}

// String is the claim as a person reads it, saying it is a claim:
// "vault passkey (claimed: Bitwarden)", "passkey (claimed: unknown)".
func (c Claim) String() string {
	provider := c.Provider
	if provider == "" {
		provider = "unknown"
	}
	return labelOf(c.Kind) + " (claimed: " + provider + ")"
}

// labelOf is a kind's name as a person reads it.
func labelOf(name string) string {
	switch name {
	case NamePlatform:
		return "platform passkey"
	case NameVault:
		return "vault passkey"
	}
	return "passkey"
}
