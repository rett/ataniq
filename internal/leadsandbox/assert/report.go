package assert

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ReportVersion is the version of the report format.
const ReportVersion = 1

// ReportPrefix starts the one line of a report (the space is part of it): the
// prefix, one JSON object and a newline. The probe writes the line to its
// report descriptor, which the holder owns the other end of. The prefix does
// not authenticate the line: a line with the prefix that is seen anywhere else
// (the terminal the lead also writes to, say) is display only, and no input
// to Failed or FailedAll. See the package documentation, "The probe and the
// holder".
const ReportPrefix = "ATANIQ-PROBE-REPORT "

// Bounds on a report.
const (
	// MaxDetailBytes is the longest detail a result keeps.
	MaxDetailBytes = 512
	// MaxResults is the most results a report holds. The table allows 13
	// distinct results at most (ten assertions and three outside halves).
	MaxResults = 24
	// MaxReportLineBytes is the longest line ParseLine accepts, terminator
	// included. It holds MaxResults results with a detail of MaxDetailBytes
	// of characters that JSON writes as two bytes (a quote or a backslash):
	// a line EncodeLine makes from a valid report is never over it.
	MaxReportLineBytes = 32 << 10
)

// Status is the outcome of one check.
type Status string

const (
	// StatusOK: the check was made and the fact it looks for holds.
	StatusOK Status = "ok"
	// StatusFailed: the check was made and the fact does not hold.
	StatusFailed Status = "failed"
	// StatusNotChecked: the check could not be made. The detail says why.
	StatusNotChecked Status = "not-checked"
)

// Valid reports whether s is one of the three statuses.
func (s Status) Valid() bool {
	return s == StatusOK || s == StatusFailed || s == StatusNotChecked
}

// Result is the outcome of one check. It states a fact about one assertion;
// it does not say that the sandbox as a whole is acceptable.
type Result struct {
	Number int    `json:"number"`
	ID     string `json:"id"`
	Status Status `json:"status"`
	// Detail is bounded to MaxDetailBytes and escaped: it holds no control
	// character and only valid UTF-8, because it may quote text that a lead
	// or the host controls (a path, an environment name, a resolver error).
	Detail string `json:"detail"`
}

// Report is what the probe writes: the results of the inside checks, keyed to
// the profile digest of the spec it was given. It has no field that says the
// sandbox passed or is supported; a reader draws that conclusion, from
// [Failed] for the inside half, and from its own outside checks.
type Report struct {
	Version int      `json:"version"`
	Digest  string   `json:"digest"`
	Results []Result `json:"results"`
}

// With returns a copy of the report with more results appended: the holder
// adds its outside results to the probe's before it calls [FailedAll].
func (r Report) With(results ...Result) Report {
	out := r
	out.Results = append(append([]Result(nil), r.Results...), results...)
	return out
}

// EscapeDetail makes s fit a Result's Detail: invalid UTF-8 bytes and every
// character that is not printable (control characters, a newline, a tab,
// escape, the characters of Unicode's format and separator classes) become a
// visible escape (\xNN, \uNNNN or \UNNNNNNNN), and the result is cut to
// MaxDetailBytes, with "..." at the end of a cut text. It is idempotent.
func EscapeDetail(s string) string {
	var pieces []string
	total := 0
	for i := 0; i < len(s) && total <= MaxDetailBytes; {
		r, size := utf8.DecodeRuneInString(s[i:])
		var p string
		switch {
		case r == utf8.RuneError && size == 1:
			p = fmt.Sprintf(`\x%02x`, s[i])
		case r == ' ' || unicode.IsPrint(r):
			p = s[i : i+size]
		case r <= 0xffff:
			p = fmt.Sprintf(`\u%04x`, r)
		default:
			p = fmt.Sprintf(`\U%08x`, r)
		}
		pieces = append(pieces, p)
		total += len(p)
		i += size
	}
	if total <= MaxDetailBytes {
		return strings.Join(pieces, "")
	}
	var b strings.Builder
	for _, p := range pieces {
		if b.Len()+len(p) > MaxDetailBytes-len("...") {
			break
		}
		b.WriteString(p)
	}
	b.WriteString("...")
	return b.String()
}

// cleanDetail reports whether d is a detail a writer could have produced: at
// most MaxDetailBytes of valid UTF-8 that holds nothing but printable
// characters and spaces.
func cleanDetail(d string) bool {
	if len(d) > MaxDetailBytes || !utf8.ValidString(d) {
		return false
	}
	for _, r := range d {
		if r != ' ' && !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// EncodeLine returns the report as one line: ReportPrefix, one JSON object
// and a newline. It escapes and bounds every detail with EscapeDetail and
// turns an unknown status into StatusFailed, so a bug cannot make a result
// look better than it is; it checks nothing else. A report that ParseLine
// would refuse (a wrong digest, an unknown ID, too many results) is still
// encoded, and the reader refuses it.
func EncodeLine(r Report) []byte {
	out := Report{Version: r.Version, Digest: r.Digest, Results: make([]Result, len(r.Results))}
	for i, res := range r.Results {
		res.Detail = EscapeDetail(res.Detail)
		if !res.Status.Valid() {
			res.Status = StatusFailed
		}
		out.Results[i] = res
	}
	// HTML escaping is off: it would turn "<", ">" and "&" of a detail into
	// six bytes each and make a line longer than the limit ParseLine applies
	// to the same text.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		// Unreachable: the report holds only integers and strings. A line
		// that ParseLine refuses is the safe answer.
		buf.Reset()
		buf.WriteString("{}\n")
	}
	return append([]byte(ReportPrefix), buf.Bytes()...)
}

var reportShape = shapeOf(reflect.TypeOf(Report{}))

// ParseLine reads one report line, strictly. The line starts with
// ReportPrefix, holds one JSON object and ends with at most one newline (or
// carriage return and newline, which a terminal makes of it); it is at most
// MaxReportLineBytes long. Unknown fields, trailing data, another version, a
// digest that is not 64 lower-case hex digits, more than MaxResults results,
// a number and ID that the table does not pair, a duplicate result, a status
// that is not ok, failed or not-checked, and a detail that is too long or
// holds a control byte or invalid UTF-8 are all refused, with the reason.
func ParseLine(line []byte) (Report, error) {
	var zero Report
	if len(line) > MaxReportLineBytes {
		return zero, refuse("report line", "is longer than %d bytes", MaxReportLineBytes)
	}
	body := bytes.TrimSuffix(line, []byte("\n"))
	body = bytes.TrimSuffix(body, []byte("\r"))
	if bytes.ContainsAny(body, "\r\n") {
		return zero, refuse("report line", "holds more than one line")
	}
	rest, ok := bytes.CutPrefix(body, []byte(ReportPrefix))
	if !ok {
		return zero, refuse("report line", "does not start with %q", ReportPrefix)
	}
	if !utf8.Valid(rest) {
		// encoding/json would replace an invalid byte with U+FFFD and go on.
		return zero, refuse("report line", "is not valid UTF-8")
	}
	if err := checkKeys(rest, reportShape, "report line"); err != nil {
		if _, ok := err.(*Refusal); ok {
			return zero, err
		}
		return zero, refuse("report line", "is not a report: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(rest))
	dec.DisallowUnknownFields()
	var r Report
	if err := dec.Decode(&r); err != nil {
		return zero, refuse("report line", "is not a report: %v", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return zero, refuse("report line", "has data after the report")
	}
	if err := r.validate(); err != nil {
		return zero, err
	}
	return r, nil
}

// ParseProbeLine is ParseLine for the line a probe writes: the report holds
// the result of the inside half of every assertion that has one, 1 to 8, each
// once and no other. A result that only the holder makes (9, 10 and the
// outside halves) is refused, so a line that carries one cannot stand in for
// the holder's own checks, and a report that lacks an inside result is
// refused, not judged. ParseLine stays for the full report, which holds both.
func ParseProbeLine(line []byte) (Report, error) {
	r, err := ParseLine(line)
	if err != nil {
		return Report{}, err
	}
	seen := map[string]bool{}
	for i, res := range r.Results {
		a, ok := ByNumber(res.Number)
		if !ok || !a.HasInside() || res.ID != a.ID {
			return Report{}, refuse(fmt.Sprintf("results[%d]", i), "id %s is not a result the probe makes", clipQuote(res.ID))
		}
		if seen[res.ID] {
			return Report{}, refuse(fmt.Sprintf("results[%d]", i), "id %s appears twice", res.ID)
		}
		seen[res.ID] = true
	}
	for _, a := range assertions {
		if a.HasInside() && !seen[a.ID] {
			return Report{}, refuse("results", "assertion %d (%s) has no result", a.Number, a.ID)
		}
	}
	return r, nil
}

func (r Report) validate() error {
	if r.Version != ReportVersion {
		return refuse("version", "is %d, want %d", r.Version, ReportVersion)
	}
	if !validDigest(r.Digest) {
		return refuse("digest", "must be 64 lower-case hex digits")
	}
	if len(r.Results) > MaxResults {
		return refuse("results", "more than %d", MaxResults)
	}
	seen := map[string]bool{}
	for i, res := range r.Results {
		field := fmt.Sprintf("results[%d]", i)
		if !validResult(res.Number, res.ID) {
			return refuse(field, "number %d and id %s are not an assertion of the table", res.Number, clipQuote(res.ID))
		}
		if !res.Status.Valid() {
			return refuse(field, "status %s is not ok, failed or not-checked", clipQuote(string(res.Status)))
		}
		if !cleanDetail(res.Detail) {
			return refuse(field, "detail is longer than %d bytes or holds a control character or invalid UTF-8", MaxDetailBytes)
		}
		if seen[res.ID] {
			return refuse(field, "id %s appears twice", res.ID)
		}
		seen[res.ID] = true
	}
	return nil
}

func validDigest(d string) bool {
	if len(d) != 64 {
		return false
	}
	for i := 0; i < len(d); i++ {
		c := d[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// clipQuote quotes s for a refusal, cut to 40 bytes.
func clipQuote(s string) string {
	if len(s) > 40 {
		s = s[:40] + "..."
	}
	return fmt.Sprintf("%q", s)
}

// Require says which assertions a judgement of a report demands results for.
type Require int

const (
	// RequireInside demands the result of the inside half of every
	// assertion that has one (1 to 8): what a probe's report holds.
	RequireInside Require = iota
	// RequireAll also demands the outside results: 9, 10, and the outside
	// halves of 1 and 5. The holder judges a start with it.
	RequireAll
)

// Problems lists, in order, why a report does not allow a start; it is empty
// when none does. A report has a problem when the spec is refused (then it is
// the only problem listed, because a refused spec cannot say what is
// optional); when its digest is not the spec's; when a required result is
// missing; when any result it holds is failed; and when a result is
// not-checked and the spec does not mark its assertion optional. A
// not-checked result is never counted as ok; and when one ID has more than one
// result. Problems does not check that the report is well formed beyond that:
// ParseLine and ParseProbeLine do.
func Problems(r Report, s Spec, req Require) []string {
	var out []string
	if err := s.Validate(); err != nil {
		// A spec that is refused cannot say what is optional.
		return []string{EscapeDetail("the spec is refused: " + err.Error())}
	}
	if r.Version != ReportVersion {
		out = append(out, fmt.Sprintf("the report is version %d, not %d", r.Version, ReportVersion))
	}
	if r.Digest != s.Digest {
		out = append(out, "the report is for another profile digest than the spec")
	}
	have := map[string]Result{}
	for _, res := range r.Results {
		if _, dup := have[res.ID]; dup {
			out = append(out, fmt.Sprintf("assertion %d (%s): more than one result", res.Number, res.ID))
		}
		have[res.ID] = res
	}
	optional := map[int]bool{}
	for _, n := range s.Optional {
		optional[n] = true
	}
	for _, a := range assertions {
		need := []string{}
		if a.HasInside() {
			need = append(need, a.ID)
		}
		if req == RequireAll && a.HasOutside() {
			if a.Where == Both {
				need = append(need, a.ID+OutsideSuffix)
			} else {
				need = append(need, a.ID)
			}
		}
		for _, id := range need {
			if _, ok := have[id]; !ok {
				out = append(out, fmt.Sprintf("assertion %d (%s): no result", a.Number, id))
			}
		}
	}
	for _, res := range r.Results {
		switch res.Status {
		case StatusOK:
		case StatusFailed:
			out = append(out, fmt.Sprintf("assertion %d (%s): failed: %s", res.Number, res.ID, EscapeDetail(res.Detail)))
		case StatusNotChecked:
			if !optional[res.Number] {
				out = append(out, fmt.Sprintf("assertion %d (%s): not checked: %s", res.Number, res.ID, EscapeDetail(res.Detail)))
			}
		default:
			out = append(out, fmt.Sprintf("assertion %d (%s): unknown status", res.Number, res.ID))
		}
	}
	return out
}

// Failed says the inside half of a report does not allow a start: see
// Problems with RequireInside. This is the test the probe makes before it
// execs the agent, and it exits 97 when it is true. It is true for a report
// with a not-checked result unless the spec marks that assertion optional.
func Failed(r Report, s Spec) bool { return len(Problems(r, s, RequireInside)) > 0 }

// FailedAll is Failed with the outside results required too.
func FailedAll(r Report, s Spec) bool { return len(Problems(r, s, RequireAll)) > 0 }
