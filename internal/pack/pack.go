// Package pack defines built-in pack manifests and the registry populated by
// each composition root before it starts serving requests.
package pack

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

type Name string

var namePattern = regexp.MustCompile("^[a-z][a-z0-9-]{1,39}$")

func ParseName(s string) (Name, error) {
	if !namePattern.MatchString(s) {
		return "", fmt.Errorf("invalid pack name %q", s)
	}
	return Name(s), nil
}

// BuiltinIdentity names the compiled code in a release. The declaration's
// digest travels alongside it to identify the exact data that code reads.
func BuiltinIdentity(name Name, version, revision string) (string, error) {
	if _, err := ParseName(string(name)); err != nil {
		return "", err
	}
	for _, part := range []string{version, revision} {
		if part == "" || strings.ContainsFunc(part, func(r rune) bool {
			return r < '!' || r > '~' || r == '@' || r == '+'
		}) {
			return "", fmt.Errorf("invalid built-in identity component %q", part)
		}
	}
	return string(name) + "@" + version + "+" + revision, nil
}

type Pack interface {
	Name() Name
	Manifest() Manifest
}

type Manifest struct {
	Name          Name    `json:"name"`
	Class         string  `json:"class"` // runtime or agent-files
	Vendor        string  `json:"vendor"`
	Display       string  `json:"display"`
	QargiName     string  `json:"qargi_name,omitempty"`
	Words         []Word  `json:"words,omitempty"`
	NoWordsReason string  `json:"no_words_reason,omitempty"`
	Builds        []Build `json:"builds"`
}

type Word struct {
	Text  string `json:"text"`
	Match string `json:"match"` // substring, token or literal
}

type Transport string

const (
	Terminal Transport = "terminal"
	Protocol Transport = "protocol"
)

type Build struct {
	Version    string      `json:"version"`
	Transports []Transport `json:"transports"`
	Interfaces []Interface `json:"interfaces,omitempty"`
	Aliases    []string    `json:"aliases,omitempty"`
	VerifiedOn string      `json:"verified_on"` // empty for documentary evidence
	Source     string      `json:"source"`
}

type Interface struct {
	Kind           string `json:"kind"`      // hooks, protocol, osc or statusline
	Stability      string `json:"stability"` // ga, preview or experimental
	MinimumVersion string `json:"minimum_version"`
	SchemaDigest   string `json:"schema_digest,omitempty"`
}

// Registry is populated and frozen during setup. Once frozen it may be
// shared by readers. Registered packs must keep their names stable.
type Registry struct {
	packs  map[Name]Pack
	frozen bool
}

func New() *Registry { return &Registry{packs: make(map[Name]Pack)} }

func (r *Registry) Register(p Pack) error {
	if r.frozen {
		return fmt.Errorf("pack registry is frozen")
	}
	if p == nil {
		return fmt.Errorf("nil pack")
	}
	name, err := ParseName(string(p.Name()))
	if err != nil {
		return err
	}
	if _, exists := r.packs[name]; exists {
		return fmt.Errorf("pack %q already registered", name)
	}
	r.packs[name] = p
	return nil
}

func (r *Registry) Freeze() {
	if !r.frozen {
		// A value copy made during setup may still write the original map.
		r.packs = maps.Clone(r.packs)
		r.frozen = true
	}
}

func (r *Registry) Lookup(n Name) (Pack, bool) {
	p, ok := r.packs[n]
	return p, ok
}

func (r *Registry) Names() []Name { return slices.Sorted(maps.Keys(r.packs)) }
