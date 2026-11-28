package agentsession

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"slices"
	"strings"
	"unicode/utf8"
)

type PolicySource struct {
	kind, name string
	content    [32]byte
}

func (s PolicySource) Kind() string      { return s.kind }
func (s PolicySource) Name() string      { return s.name }
func (s PolicySource) Content() [32]byte { return s.content }

func (s PolicySource) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Kind, Name string
		Content    [32]byte
	}{s.kind, s.name, s.content})
}

// SourceMaterial carries the exact bytes read from one named policy source.
type SourceMaterial struct {
	Kind, Name string
	Raw        []byte
}

// ExpectedPolicySources records the qualification's source set.
func ExpectedPolicySources(s []SourceMaterial) (SourcesExp, error) {
	digest, err := digestMaterial(s)
	return SourcesExp{Digest: digest}, err
}

// ObservedPolicySources records a completed read, including an empty set.
func ObservedPolicySources(s []SourceMaterial) (SourcesObs, error) {
	digest, err := digestMaterial(s)
	if err != nil {
		return SourcesObs{}, err
	}
	return SourcesObs{Seen: true, Digest: digest}, nil
}

func digestMaterial(s []SourceMaterial) ([32]byte, error) {
	entries := make([]PolicySource, 0, len(s))
	for _, v := range s {
		entry, err := PolicySourceFromBytes(v.Kind, v.Name, v.Raw)
		if err != nil {
			return [32]byte{}, err
		}
		entries = append(entries, entry)
	}
	return PolicySourcesDigest(entries)
}

func sourceText(path, s string) error {
	switch {
	case len(s) == 0:
		return invalid(path, "empty-value")
	case len(s) > 128:
		return invalid(path, "too-long")
	case !utf8.ValidString(s) || strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r >= 0x7f && r <= 0x9f }):
		return invalid(path, "not-printable")
	}
	return nil
}

func PolicySourceFromBytes(kind, name string, raw []byte) (PolicySource, error) {
	if err := sourceText("PolicySource.Kind", kind); err != nil {
		return PolicySource{}, err
	}
	if err := sourceText("PolicySource.Name", name); err != nil {
		return PolicySource{}, err
	}
	return PolicySource{kind, name, sha256.Sum256(raw)}, nil
}

func PolicySourcesDigest(s []PolicySource) ([32]byte, error) {
	b, err := sourceFrame(s)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(b), nil
}

func sourceFrame(s []PolicySource) ([]byte, error) {
	if uint64(len(s)) > 1<<32-1 {
		return nil, invalid("PolicySources", "too-long")
	}
	seen := make(map[[2]string]bool, len(s))
	for i, v := range s {
		path := index("PolicySources", i)
		if err := sourceText(path+".Kind", v.kind); err != nil {
			return nil, err
		}
		if err := sourceText(path+".Name", v.name); err != nil {
			return nil, err
		}
		key := [2]string{v.kind, v.name}
		if seen[key] {
			return nil, invalid(path+".Name", "duplicate")
		}
		seen[key] = true
	}
	entries := slices.Clone(s)
	slices.SortFunc(entries, func(a, b PolicySource) int {
		if d := strings.Compare(a.kind, b.kind); d != 0 {
			return d
		}
		return strings.Compare(a.name, b.name)
	})
	b := binary.BigEndian.AppendUint32([]byte("ataniq.policy-sources/1\n"), uint32(len(entries)))
	for _, v := range entries {
		for _, field := range []string{v.kind, v.name} {
			b = binary.BigEndian.AppendUint32(b, uint32(len(field)))
			b = append(b, field...)
		}
		b = append(b, v.content[:]...)
	}
	return b, nil
}
