// Package hookrender renders a candidate sandbox from read-only pack data on
// the daemon side. It performs no provisioning or launch.
package hookrender

import (
	"crypto/sha256"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
	"unicode"

	"ataniq/internal/agentsession/hookwire"
	lp "ataniq/internal/launchpolicy"
	"ataniq/internal/pack"
)

// Source pairs a decoded declaration with its embedded sandbox templates.
// Layout comes from core. Templates are keyed by exact build version.
type Source struct {
	Declaration *pack.Declaration
	Layout      pack.BindLayout
	Templates   fs.FS
}

// Decode joins the pack's three declaration files using the declaration
// decoder. The template filesystem is the pack's embedded sandbox directory.
func Decode(manifest, hooks []byte, sandbox fs.FS, layout pack.BindLayout) (Source, error) {
	if sandbox == nil {
		return Source{}, errors.New("missing sandbox data")
	}
	raw, err := fs.ReadFile(sandbox, path.Base(pack.SandboxPath))
	if err != nil {
		return Source{}, err
	}
	d, err := pack.DecodeDeclaration(map[string][]byte{
		pack.ManifestPath: manifest,
		pack.HooksPath:    hooks,
		pack.SandboxPath:  raw,
	}, layout)
	if err != nil {
		return Source{}, err
	}
	return Source{d, layout, sandbox}, nil
}

// Files contains candidate bytes, including the entire trust-entry document
// where declared. Empty tree files and directories are represented by their
// declared kind and relative name. PK-7 supplies their host sources.
type Files struct {
	Documents    []Document
	HelperTable  string
	TreeOverlays []pack.Bind
}

// Document is a complete file at an inside destination. A trust-entry is
// replaced whole, with its destination checked against the start inventory.
type Document struct {
	Dest, Role, Content string
	view                lp.FileView
	definition          bool
}

// Launch is returned only after both the launch and the files are admitted.
type Launch struct {
	Spec    lp.Spec
	Verdict lp.Verdict
	Files   Files
}

func (src Source) build(s lp.Snapshot) (pack.Sandbox, error) {
	d := src.Declaration
	if d == nil || d.Manifest.Name != s.Pack {
		return pack.Sandbox{}, errors.New("sandbox declaration does not match the requested pack")
	}
	if err := d.Validate(src.Layout); err != nil {
		return pack.Sandbox{}, err
	}
	b := d.Sandbox[s.Version]
	if b.Launch == nil || !fs.ValidPath(s.Version) || path.Base(s.Version) != s.Version || s.Version == "." {
		return pack.Sandbox{}, errors.New("missing sandbox launch for exact build")
	}
	if !slices.ContainsFunc(d.Manifest.Builds, func(build pack.Build) bool {
		return build.Version == s.Version && slices.Contains(build.Transports, s.Transport)
	}) {
		return pack.Sandbox{}, errors.New("transport not declared for exact build")
	}
	return b, nil
}

// Render checks the written template, expansion, authenticated owner intent
// and every candidate policy-bearing file. The caller supplies the expected
// inventory independently, from its layout and the selected agent.
func (src Source) Render(s lp.Snapshot, p *lp.Policy, intent lp.Intent, inventory lp.Inventory) (Launch, error) {
	b, err := src.build(s)
	if err != nil {
		return Launch{}, err
	}
	spec, r := lp.Expand(p, *b.Launch, s)
	if r != nil {
		return Launch{}, r
	}
	verdict, r := lp.Judge(p, *b.Launch, s, spec, intent)
	if r != nil {
		return Launch{}, r
	}
	files, err := src.files(b, s)
	if err != nil {
		return Launch{}, err
	}
	if err := files.Check(p, s, inventory); err != nil {
		return Launch{}, err
	}
	return Launch{spec, verdict, files}, nil
}

// Preview renders only the candidate file set for inspection and goldens.
// The launch path must use Render to obtain admitted bytes and argv together.
func (src Source) Preview(s lp.Snapshot) (Files, error) {
	b, err := src.build(s)
	if err != nil {
		return Files{}, err
	}
	return src.files(b, s)
}

// Check binds the matcher views to the exact output bytes and independently
// expected destinations. No provider file from a previous launch is read.
func (f Files) Check(p *lp.Policy, s lp.Snapshot, inventory lp.Inventory) error {
	var views []lp.FileView
	for _, d := range f.Documents {
		v := d.view
		v.PathRole, v.Dest = d.Role, d.Dest
		v.InstalledSHA256 = hash(d.Content)
		for _, w := range inventory {
			if w.Dest == d.Dest && w.Definition != d.definition {
				return errors.New("agent definition differs from inventory")
			}
		}
		views = append(views, v)
	}
	if r := lp.JudgeFiles(p, s, inventory, views); r != nil {
		return r
	}
	return nil
}

func hash(content string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(content))) }

func (src Source) files(b pack.Sandbox, s lp.Snapshot) (Files, error) {
	rows, ok := src.Declaration.Hooks[s.Version]
	if !ok {
		return Files{}, errors.New("missing hook rows for exact build")
	}
	table, err := json.Marshal(struct {
		Schema string                    `json:"schema"`
		Builds map[string][]pack.HookRow `json:"builds"`
	}{pack.DeclarationSchema, map[string][]pack.HookRow{s.Version: rows}}, jsontext.WithIndent("  "))
	if err != nil {
		return Files{}, err
	}
	if _, err := hookwire.DecodeTable(table); err != nil {
		return Files{}, err
	}
	out := Files{HelperTable: string(table)}
	docs, err := src.templates(s.Version, rows)
	if err != nil {
		return Files{}, err
	}
	take := func(dest string, definition bool) error {
		t, ok := docs[dest]
		if !ok || (t.Format == "frontmatter") != definition || t.Role == "trust-entry" {
			return errors.New("missing or mismatched file template at declared destination")
		}
		delete(docs, dest)
		d, err := t.document(dest)
		if err == nil {
			out.Documents = append(out.Documents, d)
		}
		return err
	}
	var definition string
	for _, a := range b.Launch.Agents {
		if a.Name == s.Agent && !a.Builtin {
			definition = a.Definition
			// S1 renders tree overlays empty, even when an agent is selected.
			if strings.HasPrefix(definition, src.Layout.Tree+"/") {
				return Files{}, errors.New("agent definition on an empty tree overlay")
			}
			if err := take(definition, true); err != nil {
				return Files{}, err
			}
		}
	}
	for _, a := range b.Launch.Agents {
		if !a.Builtin {
			delete(docs, a.Definition)
		}
	}
	for _, bind := range b.Binds {
		switch {
		case bind.Role == pack.TreeOverlay:
			out.TreeOverlays = append(out.TreeOverlays, bind)
		case bind.Kind == "file":
			if bind.Inside == definition {
				continue
			}
			if err := take(bind.Inside, false); err != nil {
				return Files{}, err
			}
		}
	}
	for dest, t := range docs {
		if len(docs) != 1 || t.Role != "trust-entry" || t.Format != "json" || !src.trustDestination(dest, b.Binds) {
			return Files{}, errors.New("unexpected file template outside declared rows")
		}
		d, err := t.document(dest)
		if err != nil {
			return Files{}, err
		}
		out.Documents = append(out.Documents, d)
	}
	return out, nil
}

// Only a whole file directly in the mounted config folder may be replaced.
// Its destination must not be a mount point or an ancestor of one.
func (src Source) trustDestination(dest string, binds []pack.Bind) bool {
	if !path.IsAbs(dest) || path.Clean(dest) != dest || path.Dir(dest) != src.Layout.Config || strings.ContainsFunc(dest, unicode.IsControl) {
		return false
	}
	overMount := func(p string) bool { return p == dest || strings.HasPrefix(p, dest+"/") }
	return !slices.ContainsFunc(src.Layout.CoreRows, overMount) &&
		!slices.ContainsFunc(binds, func(b pack.Bind) bool { return overMount(b.Inside) })
}
