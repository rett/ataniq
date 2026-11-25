package pack

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	DeclarationSchema = "ataniq.pack-declaration/1"
	ManifestPath      = "declaration.json"
	HooksPath         = "hook/rows.json"
	SandboxPath       = "sandbox/declaration.json"
	// MaxDeclarationBytes is the total input byte limit for DecodeDeclaration.
	MaxDeclarationBytes = 1 << 20
)

// Declaration assembles the files embedded by a pack. Each companion document
// is optional and keys its rows by the exact (unaliased) manifest version.
type Declaration struct {
	Manifest Manifest
	Hooks    map[string][]HookRow
	Sandbox  map[string]Sandbox
}

type Sandbox struct {
	Binds  []Bind  `json:"binds,omitempty"`
	Launch *Launch `json:"launch,omitempty"`
}

func checkFiles(files map[string][]byte) error {
	if _, ok := files[ManifestPath]; !ok {
		return fmt.Errorf("missing %s", ManifestPath)
	}
	for path := range files {
		switch path {
		case ManifestPath, HooksPath, SandboxPath:
		default:
			return fmt.Errorf("unknown declaration file %q", path)
		}
	}
	return nil
}

// DeclarationDigest hashes the original bytes, including any differences in
// spelling or formatting. Each sorted line is "path lowercase-hex-sha256\n".
func DeclarationDigest(files map[string][]byte) (string, error) {
	if err := checkFiles(files); err != nil {
		return "", err
	}
	h := sha256.New()
	for _, path := range slices.Sorted(maps.Keys(files)) {
		fmt.Fprintf(h, "%s %x\n", path, sha256.Sum256(files[path]))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Declaration members have concrete types. In particular, null cannot stand
// for false, the empty string, or an omitted optional member.
var refuseNull = json.UnmarshalFromFunc(func(d *jsontext.Decoder, _ any) error {
	if d.PeekKind() == 'n' {
		return errors.New("unexpected null")
	}
	return errors.ErrUnsupported
})

// Two required members admit their Go zero value: verified_on can be empty
// for documentary evidence and async can be false for a synchronous row.
var declarationMembers = json.JoinUnmarshalers(refuseNull,
	json.UnmarshalFromFunc(func(d *jsontext.Decoder, b *Build) error {
		type fields Build
		var row struct {
			fields
			Date *string `json:"verified_on"`
		}
		if err := json.UnmarshalDecode(d, &row); err != nil {
			return err
		}
		if row.Date == nil {
			return errors.New("missing verified_on")
		}
		*b = Build(row.fields)
		b.VerifiedOn = *row.Date
		return nil
	}),
	json.UnmarshalFromFunc(func(d *jsontext.Decoder, h *HookRow) error {
		type fields HookRow
		var row struct {
			fields
			Async *bool `json:"async"`
		}
		if err := json.UnmarshalDecode(d, &row); err != nil {
			return err
		}
		if row.Async == nil {
			return errors.New("missing async")
		}
		*h = HookRow(row.fields)
		h.Async = *row.Async
		return nil
	}),
)

func readDocument(data []byte, path string, dst any) error {
	d := jsontext.NewDecoder(bytes.NewReader(data))
	if err := json.UnmarshalDecode(d, dst, json.RejectUnknownMembers(true), json.WithUnmarshalers(declarationMembers)); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if d.InputOffset() != int64(len(data)) {
		return fmt.Errorf("%s: trailing bytes", path)
	}
	return nil
}

// DecodeDeclaration accepts only the three declaration paths. The layout is
// supplied by core for checking bind destinations and extra environment
// variables. No path in files is opened on the host.
func DecodeDeclaration(files map[string][]byte, layout BindLayout) (*Declaration, error) {
	if err := checkFiles(files); err != nil {
		return nil, err
	}
	remaining := MaxDeclarationBytes
	for _, data := range files {
		if len(data) > remaining {
			return nil, fmt.Errorf("declaration exceeds %d bytes in total", MaxDeclarationBytes)
		}
		remaining -= len(data)
	}
	var root struct {
		Schema string `json:"schema"`
		Manifest
	}
	if err := readDocument(files[ManifestPath], ManifestPath, &root); err != nil {
		return nil, err
	}
	if root.Schema != DeclarationSchema {
		return nil, fmt.Errorf("%s: unknown schema %q", ManifestPath, root.Schema)
	}
	d := &Declaration{Manifest: root.Manifest}
	var err error
	d.Hooks, err = readBuilds[[]HookRow](files, HooksPath)
	if err != nil {
		return nil, err
	}
	d.Sandbox, err = readBuilds[Sandbox](files, SandboxPath)
	if err != nil {
		return nil, err
	}
	if err := d.Validate(layout); err != nil {
		return nil, err
	}
	return d, nil
}

func readBuilds[T any](files map[string][]byte, path string) (map[string]T, error) {
	data, ok := files[path]
	if !ok {
		return nil, nil
	}
	var doc struct {
		Schema string       `json:"schema"`
		Builds map[string]T `json:"builds"`
	}
	if err := readDocument(data, path, &doc); err != nil {
		return nil, err
	}
	if doc.Schema != DeclarationSchema || doc.Builds == nil {
		return nil, fmt.Errorf("%s: missing builds or wrong schema", path)
	}
	return doc.Builds, nil
}

func (d Declaration) Validate(layout BindLayout) error {
	m := d.Manifest
	if _, err := ParseName(string(m.Name)); err != nil {
		return err
	}
	if m.Class != "runtime" && m.Class != "agent-files" {
		return fmt.Errorf("invalid pack class %q", m.Class)
	}
	if strings.TrimSpace(m.Display) == "" || strings.TrimSpace(m.Vendor) == "" || m.Builds == nil {
		return errors.New("pack needs display, vendor and builds")
	}
	if m.Class == "agent-files" && len(m.Builds) != 0 {
		return errors.New("agent-files pack cannot declare runtime builds")
	}
	if len(m.Words) == 0 && strings.TrimSpace(m.NoWordsReason) == "" {
		return errors.New("pack needs words or a reason for their absence")
	}
	for _, w := range m.Words {
		if !w.valid() {
			return fmt.Errorf("invalid pack word %+v", w)
		}
	}
	versions := make(map[string]bool)
	spellings := make(map[string]bool)
	for _, b := range m.Builds {
		if err := b.validate(); err != nil {
			return fmt.Errorf("build %q: %w", b.Version, err)
		}
		versions[b.Version] = true
		for _, s := range append(slices.Clone(b.Aliases), b.Version) {
			if strings.TrimSpace(s) == "" || spellings[s] {
				return fmt.Errorf("empty or repeated version spelling %q", s)
			}
			spellings[s] = true
		}
	}
	for version, rows := range d.Hooks {
		if !versions[version] {
			return fmt.Errorf("%s: undeclared version %q", HooksPath, version)
		}
		if err := checkHooks(rows); err != nil {
			return fmt.Errorf("%s build %q: %w", HooksPath, version, err)
		}
	}
	for version, s := range d.Sandbox {
		if !versions[version] {
			return fmt.Errorf("%s: undeclared version %q", SandboxPath, version)
		}
		if err := s.validate(layout); err != nil {
			return fmt.Errorf("%s build %q: %w", SandboxPath, version, err)
		}
	}
	return nil
}

func (w Word) valid() bool {
	switch w.Match {
	case "substring":
		letters := 0
		for _, r := range w.Text {
			if unicode.IsLetter(r) {
				letters++
			}
		}
		return letters >= 5
	case "token":
		return utf8.RuneCountInString(w.Text) >= 3 && strings.IndexFunc(w.Text, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		}) == -1
	case "literal":
		return len(w.Text) >= 4
	}
	return false
}

var sha256Hex = regexp.MustCompile("^[a-f0-9]{64}$")

func (b Build) validate() error {
	if len(b.Transports) == 0 || strings.TrimSpace(b.Source) == "" {
		return errors.New("missing transport or evidence source")
	}
	if b.VerifiedOn != "" {
		if _, err := time.Parse(time.DateOnly, b.VerifiedOn); err != nil {
			return fmt.Errorf("verified_on: %w", err)
		}
	}
	for i, tr := range b.Transports {
		if (tr != Terminal && tr != Protocol) || slices.Contains(b.Transports[:i], tr) {
			return fmt.Errorf("unknown or repeated transport %q", tr)
		}
	}
	for _, iface := range b.Interfaces {
		switch iface.Kind {
		case "hooks", "protocol", "osc", "statusline":
		default:
			return fmt.Errorf("unknown interface kind %q", iface.Kind)
		}
		switch iface.Stability {
		case "ga", "preview", "experimental":
		default:
			return fmt.Errorf("unknown interface stability %q", iface.Stability)
		}
		if strings.TrimSpace(iface.MinimumVersion) == "" || (iface.SchemaDigest != "" && !sha256Hex.MatchString(iface.SchemaDigest)) {
			return fmt.Errorf("invalid version or schema digest for %q", iface.Kind)
		}
	}
	return nil
}
