package qargiread

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// ExitMap is the shared qargi-ataniq.exit-map/1 vector. Its Steps slice sets
// precedence even when a single call satisfies several conditions.
type ExitMap struct {
	Schema        string                       `json:"schema"`
	QargiSurfaces []string                     `json:"qargi_surfaces"`
	Steps         []Step                       `json:"steps"`
	Tables        map[string]map[string]string `json:"map_tables"`
	Success       map[string][]Check           `json:"success_checks"`
}

type Check struct {
	ID   string `json:"id"`
	Code string `json:"code"`
}

type Step struct {
	Check
	Surfaces  []string          `json:"surfaces"`
	ByExit    map[string]string `json:"by_exit"`
	ByClass   map[string]string `json:"by_class"`
	BySurface map[string]string `json:"by_surface"`
}

// Call describes a fixture, including any outcome observed before a status.
// FitsResponse checks the success schema for the adapter verb under test.
type Call struct {
	Surface, Outcome   string
	Status             int
	Stdout, Stderr     string
	StructuredRefusals bool // the contract line is first written in 0.3.6
	Want               Pins
	FitsResponse       func(map[string]any) bool
}

type Result struct {
	Step, Code    string
	RefusalCode   string
	OriginUnknown bool
}

type refusal struct {
	raw, exit, code    string
	unreadable, differ bool
}

func readRefusals(stderr string) (r refusal) {
	const prefix = "qargi-refusal/1 "
	for line := range strings.SplitSeq(stderr, "\n") {
		body, found := strings.CutPrefix(line, prefix)
		if !found {
			continue
		}
		if len(line) > 4096 || !strings.HasPrefix(body, "{") || !strings.HasSuffix(body, "}") {
			r.unreadable = true
			return r
		}
		fields, err := object(body)
		exit, _ := fields["exit"].(json.Number)
		if err != nil || (exit != "1" && exit != "2" && exit != "77") {
			r.unreadable = true
			return r
		}
		if r.raw != "" && r.raw != line {
			r.differ = true
		}
		r.raw, r.exit = line, string(exit)
		r.code, _ = fields["code"].(string)
	}
	return r
}

func adapterClass(c Call) string {
	if c.Status != 0 && c.Stdout == "" {
		return "nonzero_empty"
	}
	fields, err := object(c.Stdout)
	if c.Status == 0 {
		if err == nil && c.FitsResponse != nil && c.FitsResponse(fields) {
			return "zero_one_fitting_response"
		}
		return "zero_other"
	}
	if msg, ok := fields["error"].(string); err == nil && ok && msg != "" && len(fields) == 1 {
		return "nonzero_one_error_object"
	}
	return "nonzero_other"
}

var releaseLabel = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

func (m ExitMap) success(surface string, c Call) (string, error) {
	label, _, _ := strings.Cut(c.Stdout, "\n")
	var pins Pins
	var parseErr error
	if surface == "provider-show" {
		pins, parseErr = ParseDefinition(c.Stdout)
	}
	for _, check := range m.Success[surface] {
		var match bool
		switch check.ID {
		case "stdout-not-empty":
			match = c.Stdout != ""
		case "first-line-not-a-label":
			match = !releaseLabel.MatchString(label)
		case "label-not-pinned":
			match = label != "0.3.5"
		case "not-one-object":
			match = parseErr == errObject
		case "pinned-field-absent-null-or-mistyped":
			match = parseErr != nil
		case "pinned-value-differs":
			match = !maps.Equal(pins, c.Want)
		case "passed":
			match = true
		default:
			return "", fmt.Errorf("unknown success check %q", check.ID)
		}
		if match {
			return check.Code, nil
		}
	}
	return "", fmt.Errorf("missing success check for %q", surface)
}

// Select returns the first deciding step and its code. An error represents a
// broken vector, distinct from a refusal or malformed provider output.
func (m ExitMap) Select(c Call) (Result, error) {
	var line refusal
	status := strconv.Itoa(c.Status)
	for _, step := range m.Steps {
		if !slices.Contains(step.Surfaces, c.Surface) {
			continue
		}
		result := Result{Step: step.ID, Code: step.Code}
		var match bool
		switch step.ID {
		case "install-absent", "install-unsafe", "not-started", "timeout", "signal":
			match = c.Outcome == step.ID
		case "output-over-limit":
			match = c.Outcome == step.ID || len(c.Stdout) > 256<<10 || len(c.Stderr) > 16<<10
		case "refusal-line-unreadable":
			if c.StructuredRefusals && slices.Contains(m.QargiSurfaces, c.Surface) {
				line = readRefusals(c.Stderr)
			}
			match = line.unreadable
		case "refusal-lines-differ":
			match = line.differ
		case "refusal-line-disagrees":
			match = line.raw != "" && line.exit != status
		case "stdout-on-nonzero":
			match = c.Status != 0 && c.Stdout != ""
		case "no-line-on-77":
			match = c.Status == 77 && line.raw == ""
		case "line-agrees":
			match = line.raw != ""
			result.Code, result.RefusalCode = step.ByExit[line.exit], line.code
		case "adapter-output":
			class := adapterClass(c)
			match = true
			result.Code = step.ByClass[class]
			result.OriginUnknown = class == "nonzero_empty"
		case "status":
			match = true
			result.Code = step.BySurface[c.Surface]
		default:
			return Result{}, fmt.Errorf("unknown selector step %q", step.ID)
		}
		if !match {
			continue
		}
		if name, ok := strings.CutPrefix(result.Code, "table:"); ok {
			table := m.Tables[name]
			result.Code = table[status]
			if result.Code == "" {
				result.Code = table["*"]
			}
		}
		if name, ok := strings.CutPrefix(result.Code, "success:"); ok {
			code, err := m.success(name, c)
			if err != nil {
				return Result{}, err
			}
			result.Code = code
		}
		if result.Code == "" {
			return Result{}, fmt.Errorf("missing result for %q at %q", c.Surface, step.ID)
		}
		return result, nil
	}
	return Result{}, fmt.Errorf("no deciding step for %q", c.Surface)
}
