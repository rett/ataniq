package agentsession

import (
	"crypto/sha256"
	"encoding/binary"
	"slices"
	"strings"
	"unicode/utf8"
)

type PolicySource struct {
	Kind, Name string
	Content    [32]byte
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
		if err := sourceText(path+".Kind", v.Kind); err != nil {
			return nil, err
		}
		if err := sourceText(path+".Name", v.Name); err != nil {
			return nil, err
		}
		key := [2]string{v.Kind, v.Name}
		if seen[key] {
			return nil, invalid(path+".Name", "duplicate")
		}
		seen[key] = true
	}
	entries := slices.Clone(s)
	slices.SortFunc(entries, func(a, b PolicySource) int {
		if d := strings.Compare(a.Kind, b.Kind); d != 0 {
			return d
		}
		return strings.Compare(a.Name, b.Name)
	})
	b := binary.BigEndian.AppendUint32([]byte("ataniq.policy-sources/1\n"), uint32(len(entries)))
	for _, v := range entries {
		for _, field := range []string{v.Kind, v.Name} {
			b = binary.BigEndian.AppendUint32(b, uint32(len(field)))
			b = append(b, field...)
		}
		b = append(b, v.Content[:]...)
	}
	return b, nil
}
