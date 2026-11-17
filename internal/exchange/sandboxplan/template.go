package sandboxplan

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// MaxTemplateBytes is the largest template file [ParseTemplate] accepts.
const MaxTemplateBytes = 64 << 10

// TemplateVersion is the only template format version.
const TemplateVersion = 1

// Template is a [Planner] that fills in the argvs the owner wrote in a
// template file. It is not a statement that those argvs are a sandbox: the
// package does not check that they confine anything. The owner writes the
// template, and qualification of the sandbox is a separate step.
//
// The file is JSON, version 1:
//
//	{
//	  "version": 1,
//	  "serve": {
//	    "upload_pack": ["/abs/sandbox", "{tree}", "/usr/bin/git-upload-pack"],
//	    "repository": "/work/{project}",
//	    "measure": ["/abs/sandbox", "{tree}", "/usr/bin/du", "-s", "-B1", "/work/{project}/.git/objects"],
//	    "measure_env": ["PATH=/usr/bin:/bin"]
//	  },
//	  "verify": {
//	    "prefix": ["/abs/sandbox", "{mirror}", "{scratch}", "{home}"],
//	    "env": ["PATH=/usr/bin:/bin"],
//	    "mirror": "/ataniq/mirror/{project}.git",
//	    "scratch": "/work/verify",
//	    "home": "/home/verify"
//	  }
//	}
//
// In "serve" the placeholders are {role}, {project} and {tree}. In "verify"
// they are {project}, {mirror}, {scratch} and {home}. Any other brace is
// refused, so a literal brace cannot be written.
//
// {role} and {project} stand for a name. A name has no slash and no dot, so
// it cannot add a path component. They work in every string of their
// section, inside a longer string too.
//
// {tree}, {mirror}, {scratch} and {home} each stand for a root folder that
// the caller vouches for (see [ServeRequest] and [VerifyRequest]). Each must
// be a whole argv element: the element is exactly the placeholder, with
// nothing before or after it. A template can then never name a path below a
// root or around it, because the launcher resolves such a path as the owner,
// over folders that a sandbox may have changed. The launcher takes the root
// as a separate argument. A path placeholder is refused in "measure_env", in
// "env" and in the inside-path fields "repository", "mirror", "scratch" and
// "home". Those fields are paths as the sandbox sees them: they hold only
// {role}, {project} and fixed text.
//
// The first element of each argv is the launcher, which runs as the owner:
// it is an absolute, clean path written out in full, with no placeholder and
// no brace.
//
// Either section may be absent. The matching method then returns
// [ErrNotConfigured].
//
// A request is checked before a value is put in, with
// [ServeRequest.Validate] and [VerifyRequest.Validate]. A role or a project
// must pass [ValidName]. A path must be absolute and clean, hold no NUL byte,
// newline or brace, and not be the root of the file system. The verify
// roots must not be the same folder or lie inside each other. The checks read
// no file: the caller's precondition on each request type is its own to meet.
type Template struct {
	serve  *serveTemplate
	verify *verifyTemplate
}

type serveTemplate struct {
	uploadPack []word
	repository word
	measure    []word
	measureEnv []word
}

type verifyTemplate struct {
	prefix                []word
	env                   []word
	mirror, scratch, home word
}

type templateFile struct {
	Version int         `json:"version"`
	Serve   *serveFile  `json:"serve"`
	Verify  *verifyFile `json:"verify"`
}

type serveFile struct {
	UploadPack []string `json:"upload_pack"`
	Repository string   `json:"repository"`
	Measure    []string `json:"measure"`
	MeasureEnv []string `json:"measure_env"`
}

type verifyFile struct {
	Prefix  []string `json:"prefix"`
	Env     []string `json:"env"`
	Mirror  string   `json:"mirror"`
	Scratch string   `json:"scratch"`
	Home    string   `json:"home"`
}

// placeholders are the names a section knows. A name stands for a value
// that cannot add a path component. A path stands for a root folder that the
// caller vouches for: it may only be a whole argv element.
type placeholders struct {
	names, paths []string
}

var (
	servePlaceholders  = placeholders{names: []string{"role", "project"}, paths: []string{"tree"}}
	verifyPlaceholders = placeholders{names: []string{"project"}, paths: []string{"mirror", "scratch", "home"}}
)

// ParseTemplate reads a template file. It refuses a file over
// [MaxTemplateBytes], unknown fields, a version other than
// [TemplateVersion], trailing data, an empty argv, an argv whose first
// element is not an absolute path, a NUL byte, a placeholder the section
// does not have, and any other brace. It checks that the template builds
// valid plans.
func ParseTemplate(data []byte) (*Template, error) {
	if len(data) > MaxTemplateBytes {
		return nil, fmt.Errorf("sandbox template: larger than %d bytes", MaxTemplateBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var f templateFile
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("sandbox template: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("sandbox template: data after the template")
	}
	if f.Version != TemplateVersion {
		return nil, fmt.Errorf("sandbox template: version %d is not supported, want %d", f.Version, TemplateVersion)
	}
	t := &Template{}
	var err error
	if f.Serve != nil {
		if t.serve, err = compileServe(f.Serve); err != nil {
			return nil, fmt.Errorf("sandbox template: serve: %w", err)
		}
	}
	if f.Verify != nil {
		if t.verify, err = compileVerify(f.Verify); err != nil {
			return nil, fmt.Errorf("sandbox template: verify: %w", err)
		}
	}
	return t, nil
}

func compileServe(f *serveFile) (*serveTemplate, error) {
	var (
		s   serveTemplate
		err error
	)
	ph := servePlaceholders
	if s.uploadPack, err = compileArgv("upload_pack", f.UploadPack, ph); err != nil {
		return nil, err
	}
	if s.repository, err = compileWord("repository", f.Repository, ph, false); err != nil {
		return nil, err
	}
	if s.measure, err = compileArgv("measure", f.Measure, ph); err != nil {
		return nil, err
	}
	if s.measureEnv, err = compileList("measure_env", f.MeasureEnv, ph, false); err != nil {
		return nil, err
	}
	// Build one plan from dummy values: this finds a template that can
	// never make a valid plan.
	dummy := map[string]string{"role": "x", "project": "x", "tree": "/x"}
	if _, err := s.plan(dummy); err != nil {
		return nil, err
	}
	return &s, nil
}

func compileVerify(f *verifyFile) (*verifyTemplate, error) {
	var (
		v   verifyTemplate
		err error
	)
	ph := verifyPlaceholders
	if v.prefix, err = compileArgv("prefix", f.Prefix, ph); err != nil {
		return nil, err
	}
	if v.env, err = compileList("env", f.Env, ph, false); err != nil {
		return nil, err
	}
	if v.mirror, err = compileWord("mirror", f.Mirror, ph, false); err != nil {
		return nil, err
	}
	if v.scratch, err = compileWord("scratch", f.Scratch, ph, false); err != nil {
		return nil, err
	}
	if v.home, err = compileWord("home", f.Home, ph, false); err != nil {
		return nil, err
	}
	dummy := map[string]string{"project": "x", "mirror": "/x/m", "scratch": "/x/s", "home": "/x/h"}
	if _, err := v.plan(dummy); err != nil {
		return nil, err
	}
	return &v, nil
}

func (s *serveTemplate) plan(vals map[string]string) (ServePlan, error) {
	p := ServePlan{
		UploadPack: renderList(s.uploadPack, vals),
		Repository: s.repository.render(vals),
		Measure: Command{
			Argv: renderList(s.measure, vals),
			Env:  renderList(s.measureEnv, vals),
		},
	}
	if err := p.Validate(); err != nil {
		return ServePlan{}, err
	}
	return p, nil
}

func (v *verifyTemplate) plan(vals map[string]string) (VerifyPlan, error) {
	p := VerifyPlan{
		Prefix:  renderList(v.prefix, vals),
		Env:     renderList(v.env, vals),
		Mirror:  v.mirror.render(vals),
		Scratch: v.scratch.render(vals),
		Home:    v.home.render(vals),
	}
	if err := p.Validate(); err != nil {
		return VerifyPlan{}, err
	}
	return p, nil
}

// Serve returns the serve section with the request filled in, or an error
// wrapping [ErrNotConfigured] when the template has no serve section.
func (t *Template) Serve(r ServeRequest) (ServePlan, error) {
	if t == nil || t.serve == nil {
		return ServePlan{}, fmt.Errorf("%w for serve", ErrNotConfigured)
	}
	if err := r.Validate(); err != nil {
		return ServePlan{}, fmt.Errorf("serve: %w", err)
	}
	p, err := t.serve.plan(map[string]string{"role": r.Role, "project": r.Project, "tree": r.Tree})
	if err != nil {
		return ServePlan{}, fmt.Errorf("serve: %w", err)
	}
	return p, nil
}

// Verify returns the verify section with the request filled in, or an error
// wrapping [ErrNotConfigured] when the template has no verify section.
func (t *Template) Verify(r VerifyRequest) (VerifyPlan, error) {
	if t == nil || t.verify == nil {
		return VerifyPlan{}, fmt.Errorf("%w for verify", ErrNotConfigured)
	}
	if err := r.Validate(); err != nil {
		return VerifyPlan{}, fmt.Errorf("verify: %w", err)
	}
	p, err := t.verify.plan(map[string]string{
		"project": r.Project, "mirror": r.Mirror, "scratch": r.Scratch, "home": r.Home,
	})
	if err != nil {
		return VerifyPlan{}, fmt.Errorf("verify: %w", err)
	}
	return p, nil
}

var _ Planner = (*Template)(nil)

// A word is a template string split into literal text and placeholders.
type word []segment

type segment struct {
	lit string // literal text, when name is empty
	ph  string // placeholder name
}

// compileWord and compileList take argv true for the elements of an argv,
// where a path placeholder may be a whole element. Everywhere else a path
// placeholder is refused.
func compileWord(field, s string, ph placeholders, argv bool) (word, error) {
	w, err := parseWord(s, ph, argv)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", field, err)
	}
	return w, nil
}

func compileList(field string, in []string, ph placeholders, argv bool) ([]word, error) {
	out := make([]word, 0, len(in))
	for i, s := range in {
		w, err := parseWord(s, ph, argv)
		if err != nil {
			return nil, fmt.Errorf("%s[%d]: %w", field, i, err)
		}
		out = append(out, w)
	}
	return out, nil
}

// compileArgv compiles an argv that must not be empty and whose first
// element is an absolute, clean path written out in full: no placeholder and
// no brace. The first element is the launcher, which runs as the owner, so
// no request value and no lead path may choose it.
func compileArgv(field string, in []string, ph placeholders) ([]word, error) {
	if len(in) == 0 {
		return nil, fmt.Errorf("%s: empty argv", field)
	}
	first := in[0]
	if !strings.HasPrefix(first, "/") || strings.ContainsAny(first, "{}") || filepath.Clean(first) != first {
		return nil, fmt.Errorf("%s[0]: not an absolute, clean path written out in full", field)
	}
	return compileList(field, in, ph, true)
}

func parseWord(s string, ph placeholders, argv bool) (word, error) {
	whole := s
	if strings.IndexByte(s, 0) >= 0 {
		return nil, errors.New("NUL byte")
	}
	var w word
	for len(s) > 0 {
		i := strings.IndexAny(s, "{}")
		if i < 0 {
			w = append(w, segment{lit: s})
			break
		}
		if i > 0 {
			w = append(w, segment{lit: s[:i]})
		}
		if s[i] == '}' {
			return nil, errors.New("unmatched } (a literal brace is not supported)")
		}
		end := strings.IndexByte(s[i:], '}')
		if end < 0 {
			return nil, errors.New("unmatched { (a literal brace is not supported)")
		}
		name := s[i+1 : i+end]
		switch {
		case contains(ph.names, name):
		case contains(ph.paths, name):
			if !argv || len(whole) != len(name)+2 || i != 0 {
				return nil, fmt.Errorf("{%s} must be a whole argv element", name)
			}
		default:
			return nil, fmt.Errorf("unknown placeholder {%s}", name)
		}
		w = append(w, segment{ph: name})
		s = s[i+end+1:]
	}
	return w, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (w word) render(vals map[string]string) string {
	var b strings.Builder
	for _, s := range w {
		if s.ph != "" {
			b.WriteString(vals[s.ph])
		} else {
			b.WriteString(s.lit)
		}
	}
	return b.String()
}

func renderList(ws []word, vals map[string]string) []string {
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = w.render(vals)
	}
	return out
}
