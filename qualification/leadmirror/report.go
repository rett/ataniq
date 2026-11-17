package leadmirror

import (
	"context"
	"errors"
	"sort"

	"ataniq/internal/exchange/leadtext"
	"ataniq/internal/exchange/safefs"
	"ataniq/internal/exchange/sandboxplan"
	"ataniq/internal/intake"
	"ataniq/internal/mirror"
)

// ReportFormat is the version of the report's JSON layout.
const ReportFormat = 1

// Report is the one JSON document the harness writes. It holds facts. It has
// no field that says pass, supported or qualified, and a test keeps it so.
type Report struct {
	// Format is [ReportFormat].
	Format int `json:"format"`
	// Tools are the versions of the tools the run observed.
	Tools Tools `json:"tools"`
	// Template names the sandbox template that the run used.
	Template TemplateInfo `json:"template"`
	// Role and Project are the scratch lead and project of the run.
	Role    string `json:"role"`
	Project string `json:"project"`
	// Cases are the cases in the order they ran.
	Cases []*Case `json:"cases"`
}

// Tools are the tool versions the run observed.
type Tools struct {
	// Git is the output of "git version" of the git executable the harness
	// and the mirror use.
	Git string `json:"git"`
	// Go is the Go version the harness was built with, and GOOS and GOARCH
	// are what it was built for.
	Go     string `json:"go"`
	GOOS   string `json:"goos"`
	GOARCH string `json:"goarch"`
}

// TemplateInfo identifies the template. SHA256 is the hex digest of the
// template file's bytes.
type TemplateInfo struct {
	SHA256 string `json:"sha256"`
}

// Case is one group of facts. The cases are "s16-fetch", "oversize",
// "planted", "s19-review" and "verification".
type Case struct {
	// ID is the case's name.
	ID string `json:"id"`
	// Title says in one line what the case looks at.
	Title string `json:"title"`
	// Facts are the observations, in the order they were made.
	Facts []Fact `json:"facts"`
}

// Fact is one observation. Observed holds the values; the keys are named in
// the manual. A value that came from a lead is escaped with leadtext.Escape.
type Fact struct {
	// ID is the fact's name, "<case>.<name>".
	ID string `json:"id"`
	// Detail says what was observed and how, in plain words.
	Detail string `json:"detail"`
	// Observed holds the observed values.
	Observed map[string]any `json:"observed"`
}

// RefFact is one ref of the mirror. The name is lead text, escaped.
type RefFact struct {
	Name string `json:"name"`
	ID   string `json:"id"`
	Type string `json:"type"`
}

// SizeFact is the size of the mirror folder: allocated bytes and entries.
type SizeFact struct {
	Bytes   int64 `json:"bytes"`
	Entries int64 `json:"entries"`
}

// refFacts converts refs of the mirror. The names are escaped.
func refFacts(refs []mirror.Ref) []RefFact {
	out := make([]RefFact, 0, len(refs))
	for _, r := range refs {
		out = append(out, RefFact{Name: leadtext.Escape(r.Name), ID: r.ID, Type: r.Type})
	}
	return out
}

// sameRefs reports whether two lists hold the same refs with the same ids.
func sameRefs(a, b []RefFact) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// state is what the harness reads from a mirror: the upstream refs, one
// lead's refs and the size of the folder.
type state struct {
	Upstream []RefFact `json:"upstream_refs"`
	Lead     []RefFact `json:"lead_refs"`
	Size     SizeFact  `json:"size"`
}

// readState reads the refs and the size of the mirror through its own
// methods.
func readState(ctx context.Context, m *mirror.Mirror, role string, entries int64) (state, error) {
	var s state
	up, err := m.Upstream(ctx)
	if err != nil {
		return s, err
	}
	lead, err := m.LeadBranches(ctx, role)
	if err != nil {
		return s, err
	}
	size, err := m.Size(ctx, safefs.SizeLimits{MaxBytes: 1 << 62, MaxEntries: entries})
	if err != nil {
		return s, err
	}
	s.Upstream, s.Lead = refFacts(up), refFacts(lead)
	s.Size = SizeFact{Bytes: size.Bytes, Entries: size.Entries}
	return s, nil
}

// errorFacts describes the result of a call: "result" is "ok" or "error",
// and an error brings its text (escaped, as a precaution), the classes it
// matches and, for a limit error, the limit.
func errorFacts(err error) map[string]any {
	if err == nil {
		return map[string]any{"result": "ok"}
	}
	m := map[string]any{
		"result":        "error",
		"error":         leadtext.Escape(err.Error()),
		"error_classes": classify(err),
	}
	var le *mirror.LimitError
	if errors.As(err, &le) {
		m["limit"] = map[string]any{"what": le.What, "got": le.Got, "max": le.Max}
	}
	return m
}

// classify names the sentinel errors that err matches, sorted. The names are
// those of the packages, so a reader can look them up.
func classify(err error) []string {
	known := []struct {
		target error
		name   string
	}{
		{mirror.ErrBusy, "mirror.ErrBusy"},
		{mirror.ErrHostileTree, "mirror.ErrHostileTree"},
		{mirror.ErrUpstreamChanged, "mirror.ErrUpstreamChanged"},
		{mirror.ErrRefused, "mirror.ErrRefused"},
		{mirror.ErrFetchTimeout, "mirror.ErrFetchTimeout"},
		{mirror.ErrBadName, "mirror.ErrBadName"},
		{sandboxplan.ErrNotConfigured, "sandboxplan.ErrNotConfigured"},
		{intake.ErrNoRef, "intake.ErrNoRef"},
		{intake.ErrLimit, "intake.ErrLimit"},
		{context.DeadlineExceeded, "context.DeadlineExceeded"},
		{context.Canceled, "context.Canceled"},
	}
	out := []string{}
	for _, k := range known {
		if errors.Is(err, k.target) {
			out = append(out, k.name)
		}
	}
	var le *mirror.LimitError
	if errors.As(err, &le) {
		out = append(out, "mirror.LimitError")
	}
	sort.Strings(out)
	return out
}
