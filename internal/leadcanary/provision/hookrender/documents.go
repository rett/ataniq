package hookrender

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"
	"text/template"
	"unicode"

	lp "ataniq/internal/launchpolicy"
	"ataniq/internal/pack"
)

// Each reviewed template produces one object keyed by sandbox destination.
// Values is the sole source of both encoded fields and matcher entries.
type fileTemplate struct {
	Role   string         `json:"role"`
	Format string         `json:"format"`
	Values map[string]any `json:"values"`
	Body   string         `json:"body,omitempty"`
}

// Keep file numbers exact from the template through encoding and matching.
// The only numeric fields in these templates are integral hook timeouts.
var integers = json.UnmarshalFromFunc(func(d *jsontext.Decoder, dst *any) error {
	if d.PeekKind() != '0' {
		return errors.ErrUnsupported
	}
	raw, err := d.ReadValue()
	if err != nil {
		return err
	}
	n, err := strconv.ParseInt(string(raw), 10, 32)
	if err != nil {
		return errors.New("expected a small integer file field")
	}
	*dst = int32(n)
	return nil
})

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if len(p) > pack.MaxDeclarationBytes-b.Len() {
		return 0, errors.New("rendered template too large")
	}
	return b.Buffer.Write(p)
}

func (src Source) templates(version string, rows []pack.HookRow) (map[string]fileTemplate, error) {
	if src.Templates == nil {
		return nil, errors.New("missing sandbox templates")
	}
	data, err := fs.ReadFile(src.Templates, version+".tmpl")
	if err != nil {
		return nil, err
	}
	if len(data) > pack.MaxDeclarationBytes {
		return nil, errors.New("sandbox template too large")
	}
	t, err := template.New("files").Option("missingkey=error").Funcs(template.FuncMap{
		"quote": func(s string) (string, error) {
			b, err := json.Marshal(s)
			return string(b), err
		},
		"seconds": func(ms int32) (int32, error) {
			if ms%1000 != 0 {
				return 0, errors.New("timeout needs whole seconds")
			}
			return ms / 1000, nil
		},
	}).Parse(string(data))
	if err != nil {
		return nil, err
	}
	var buf limitedBuffer
	// No owner-channel switch exists. Remote control stays disabled for every lead.
	err = t.Execute(&buf, struct {
		Tree                  string
		Rows                  []pack.HookRow
		RemoteControlDisabled bool
	}{src.Layout.Tree, rows, true})
	if err != nil {
		return nil, err
	}
	var docs map[string]fileTemplate
	if err := json.Unmarshal(buf.Bytes(), &docs, json.RejectUnknownMembers(true), json.WithUnmarshalers(integers)); err != nil {
		return nil, err
	}
	if docs == nil {
		return nil, errors.New("expected file templates")
	}
	return docs, nil
}

func (t fileTemplate) document(dest string) (Document, error) {
	if t.Role == "" || t.Values == nil || (t.Body != "" && t.Format != "frontmatter") {
		return Document{}, errors.New("incomplete file template")
	}
	var content string
	var err error
	switch {
	case t.Format == "json" && path.Ext(dest) == ".json":
		var b []byte
		b, err = json.Marshal(t.Values, json.Deterministic(true), jsontext.WithIndent("  "))
		content = string(b) + "\n"
	case t.Format == "toml" && path.Ext(dest) == ".toml":
		content, err = fields(t.Values, false)
	case t.Format == "frontmatter" && path.Ext(dest) == ".md":
		var header string
		header, err = fields(t.Values, true)
		content = "---\n" + header + "---\n" + t.Body
	default:
		return Document{}, errors.New("unsupported encoding for file destination")
	}
	if err != nil {
		return Document{}, err
	}
	v := lp.FileView{BytesSHA256: hash(content)}
	if err := addEntries(&v.Entries, []lp.Comp{}, t.Values); err != nil {
		return Document{}, err
	}
	return Document{dest, t.Role, content, v, t.Format == "frontmatter"}, nil
}

// TOML here needs quoted names, strings, booleans and string lists only.
// The same quoted strings are safe YAML scalars in an agent's frontmatter.
func fields(m map[string]any, frontmatter bool) (string, error) {
	var b strings.Builder
	var write func([]string, map[string]any) error
	write = func(parent []string, fields map[string]any) error {
		for _, key := range slices.Sorted(maps.Keys(fields)) {
			k, err := quoted(key)
			if err != nil {
				return err
			}
			keys := append(slices.Clone(parent), k)
			v := fields[key]
			if frontmatter {
				s, ok := v.(string)
				if !ok {
					return errors.New("agent frontmatter needs string values")
				}
				value, err := quoted(s)
				if err != nil {
					return err
				}
				fmt.Fprintf(&b, "%s: %s\n", k, value)
				continue
			}
			if sub, ok := v.(map[string]any); ok {
				if len(sub) == 0 {
					return errors.New("empty TOML table")
				}
				if err := write(keys, sub); err != nil {
					return err
				}
				continue
			}
			value, err := tomlValue(v)
			if err != nil {
				return err
			}
			fmt.Fprintf(&b, "%s = %s\n", strings.Join(keys, "."), value)
		}
		return nil
	}
	err := write(nil, m)
	return b.String(), err
}

func quoted(s string) (string, error) {
	if strings.ContainsFunc(s, unicode.IsControl) {
		return "", errors.New("control character in file field")
	}
	b, err := json.Marshal(s)
	return string(b), err
}
func tomlValue(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return quoted(x)
	case bool:
		return strconv.FormatBool(x), nil
	case []any:
		var a []string
		for _, v := range x {
			s, ok := v.(string)
			if !ok {
				return "", errors.New("expected TOML string list")
			}
			q, err := quoted(s)
			if err != nil {
				return "", err
			}
			a = append(a, q)
		}
		return "[" + strings.Join(a, ", ") + "]", nil
	default:
		return "", errors.New("unsupported TOML value")
	}
}

func addEntries(out *[]lp.Entry, p []lp.Comp, v any) error {
	e := lp.Entry{Path: p}
	switch x := v.(type) {
	case map[string]any:
		if len(x) > 0 {
			for _, k := range slices.Sorted(maps.Keys(x)) {
				if err := addEntries(out, append(slices.Clip(p), k), x[k]); err != nil {
					return err
				}
			}
			return nil
		}
		e.Type = "empty-object"
	case []any:
		if len(x) > 0 {
			for i, v := range x {
				if err := addEntries(out, append(slices.Clip(p), i), v); err != nil {
					return err
				}
			}
			return nil
		}
		e.Type = "empty-array"
	case string:
		e.Type, e.Text = "string", x
	case bool:
		e.Type, e.Text = "bool", strconv.FormatBool(x)
	case int32:
		e.Type, e.Text = "int", strconv.FormatInt(int64(x), 10)
	default:
		return errors.New("unsupported file field")
	}
	*out = append(*out, e)
	return nil
}
