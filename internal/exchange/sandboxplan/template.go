package sandboxplan

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
//	    "measure": ["/abs/sandbox", "/usr/bin/du", "-s", "-B1", "/work/{project}/.git/objects"],
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
// they are {project}, {mirror}, {scratch} and {home}. They work in every
// string of their section. Any other brace is refused, so a literal brace
// cannot be written. The first element of each argv is an absolute path
// written out in full.
//
// Either section may be absent. The matching method then returns
// [ErrNotConfigured].
//
// A value is checked before it is put in. A role or a project must pass
// [ValidName]. A path must be absolute and clean and hold no NUL byte,
// newline or brace.
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

var (
	servePlaceholders  = []string{"role", "project", "tree"}
	verifyPlaceholders = []string{"project", "mirror", "scratch", "home"}
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
	if s.repository, err = compileWord("repository", f.Repository, ph); err != nil {
		return nil, err
	}
	if s.measure, err = compileArgv("measure", f.Measure, ph); err != nil {
		return nil, err
	}
	if s.measureEnv, err = compileList("measure_env", f.MeasureEnv, ph); err != nil {
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
	if v.env, err = compileList("env", f.Env, ph); err != nil {
		return nil, err
	}
	if v.mirror, err = compileWord("mirror", f.Mirror, ph); err != nil {
		return nil, err
	}
	if v.scratch, err = compileWord("scratch", f.Scratch, ph); err != nil {
		return nil, err
	}
	if v.home, err = compileWord("home", f.Home, ph); err != nil {
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
	if !ValidName(r.Role) {
		return ServePlan{}, errors.New("serve: role is not a valid name")
	}
	if !ValidName(r.Project) {
		return ServePlan{}, errors.New("serve: project is not a valid name")
	}
	if err := validPath(r.Tree); err != nil {
		return ServePlan{}, fmt.Errorf("serve: tree: %w", err)
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
	if !ValidName(r.Project) {
		return VerifyPlan{}, errors.New("verify: project is not a valid name")
	}
	for _, f := range []struct{ name, path string }{
		{"mirror", r.Mirror}, {"scratch", r.Scratch}, {"home", r.Home},
	} {
		if err := validPath(f.path); err != nil {
			return VerifyPlan{}, fmt.Errorf("verify: %s: %w", f.name, err)
		}
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

func compileWord(field, s string, allowed []string) (word, error) {
	w, err := parseWord(s, allowed)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", field, err)
	}
	return w, nil
}

func compileList(field string, in []string, allowed []string) ([]word, error) {
	out := make([]word, 0, len(in))
	for i, s := range in {
		w, err := parseWord(s, allowed)
		if err != nil {
			return nil, fmt.Errorf("%s[%d]: %w", field, i, err)
		}
		out = append(out, w)
	}
	return out, nil
}

// compileArgv compiles an argv that must not be empty and whose first
// element is an absolute path written out in full.
func compileArgv(field string, in []string, allowed []string) ([]word, error) {
	if len(in) == 0 {
		return nil, fmt.Errorf("%s: empty argv", field)
	}
	if !strings.HasPrefix(in[0], "/") {
		return nil, fmt.Errorf("%s[0]: not an absolute path written out in full", field)
	}
	return compileList(field, in, allowed)
}

func parseWord(s string, allowed []string) (word, error) {
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
		if !contains(allowed, name) {
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
