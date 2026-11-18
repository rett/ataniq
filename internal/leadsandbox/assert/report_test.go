package assert

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func goodReport() Report {
	return Report{
		Version: ReportVersion,
		Digest:  strings.Repeat("ab", 32),
		Results: []Result{
			{1, IDHolderHidden, StatusOK, "fine"},
			{5, IDTIOCSTIBlocked, StatusFailed, "legacy_tiocsti is 1"},
			{8, IDManagedSettings, StatusNotChecked, "cannot be read"},
		},
	}
}

func TestEncodeLineShape(t *testing.T) {
	line := EncodeLine(goodReport())
	if !bytes.HasPrefix(line, []byte(ReportPrefix)) || line[len(line)-1] != '\n' {
		t.Fatalf("line %q", line)
	}
	if bytes.Count(line, []byte("\n")) != 1 {
		t.Errorf("not one line: %q", line)
	}
	for _, field := range []string{"pass", "passed", "supported", "ok_all"} {
		if bytes.Contains(line, []byte(`"`+field+`"`)) {
			t.Errorf("the report has a %q field", field)
		}
	}
}

func TestReportHasNoVerdictField(t *testing.T) {
	// The report and result types carry facts only: no field that says the
	// sandbox passed or is supported.
	var m map[string]any
	data, _ := json.Marshal(goodReport())
	_ = json.Unmarshal(data, &m)
	got := map[string]bool{}
	for k := range m {
		got[k] = true
	}
	for _, k := range []string{"version", "digest", "results"} {
		if !got[k] {
			t.Errorf("missing %s", k)
		}
		delete(got, k)
	}
	if len(got) != 0 {
		t.Errorf("unexpected report fields: %v", got)
	}
}

func TestParseLineRoundTrip(t *testing.T) {
	r := goodReport()
	back, err := ParseLine(EncodeLine(r))
	if err != nil {
		t.Fatal(err)
	}
	if back.Version != r.Version || back.Digest != r.Digest || len(back.Results) != 3 || back.Results[1] != r.Results[1] {
		t.Errorf("round trip: %+v", back)
	}
	// A terminal turns the newline into carriage return and newline; no
	// terminator at all is accepted too.
	line := bytes.TrimSuffix(EncodeLine(r), []byte("\n"))
	for _, l := range [][]byte{append(append([]byte(nil), line...), '\r', '\n'), line} {
		if _, err := ParseLine(l); err != nil {
			t.Errorf("%q: %v", l[len(l)-3:], err)
		}
	}
	// So does a report of every assertion, both halves.
	all := Report{Version: ReportVersion, Digest: r.Digest}
	for _, a := range Assertions() {
		all.Results = append(all.Results, Result{a.Number, a.ID, StatusOK, ""})
		if a.Where == Both {
			all.Results = append(all.Results, Result{a.Number, a.ID + OutsideSuffix, StatusOK, ""})
		}
	}
	if _, err := ParseLine(EncodeLine(all)); err != nil {
		t.Errorf("full report: %v", err)
	}
}

func TestParseLineRefusals(t *testing.T) {
	good := string(EncodeLine(goodReport()))
	body := strings.TrimSuffix(strings.TrimPrefix(good, ReportPrefix), "\n")
	mk := func(s string) []byte { return []byte(ReportPrefix + s + "\n") }
	// The version as the line writes it, and the same key in other shapes.
	version := fmt.Sprintf(`"version":%d`, ReportVersion)
	replace := func(old, new string) []byte {
		if !strings.Contains(body, old) {
			t.Fatalf("%q not in %s", old, body)
		}
		return mk(strings.Replace(body, old, new, 1))
	}
	cases := []struct {
		name  string
		line  []byte
		check string
	}{
		{"no prefix", []byte(body + "\n"), "does not start"},
		{"wrong prefix", []byte("REPORT " + body + "\n"), "does not start"},
		{"empty", nil, "does not start"},
		{"two lines", []byte(good + good), "more than one line"},
		{"embedded newline", []byte(ReportPrefix + "{\n}\n"), "more than one line"},
		{"embedded carriage return", []byte(ReportPrefix + "{\r}\n"), "more than one line"},
		{"newline then data", []byte(good + "x"), "more than one line"},
		{"oversized", append([]byte(ReportPrefix), bytes.Repeat([]byte("x"), MaxReportLineBytes)...), "longer than"},
		{"unknown field", replace(version, version+`,"sandbox_ok":true`), "sandbox_ok"},
		{"unknown result field", replace(`"detail":"fine"`, `"detail":"fine","extra":1`), "extra"},
		{"key in another case", replace(version, `"Version":1`), "case-sensitive"},
		{"result key in another case", replace(`"status":"ok"`, `"STATUS":"ok"`), "case-sensitive"},
		{"key twice", replace(version, version+`,`+version), "twice"},
		{"key twice in two cases", replace(version, version+`,"VERSION":1`), "case-sensitive"},
		{"result key twice", replace(`"detail":"fine"`, `"detail":"fine","detail":"other"`), "twice"},
		{"trailing data", mk(body + " {}"), "after the report"},
		{"not json", mk("version=1"), "not a report"},
		{"wrong version", replace(version, fmt.Sprintf(`"version":%d`, ReportVersion+1)), "version"},
		{"the version before", replace(version, fmt.Sprintf(`"version":%d`, ReportVersion-1)), "version"},
		{"bad digest", replace(strings.Repeat("ab", 32), "xyz"), "digest"},
		{"upper case digest", replace(strings.Repeat("ab", 32), strings.Repeat("AB", 32)), "digest"},
		{"bad status", replace(`"status":"ok"`, `"status":"passed"`), "status"},
		{"empty status", replace(`"status":"ok"`, `"status":""`), "status"},
		{"unknown id", replace(IDHolderHidden, "something-else"), "not an assertion"},
		{"number and id differ", replace(`"number":1`, `"number":2`), "not an assertion"},
		{"number zero", replace(`"number":1`, `"number":0`), "not an assertion"},
		{"number 11 with the id of 1", replace(`"number":1`, `"number":11`), "not an assertion"},
		{"number 12", replace(`"number":1`, `"number":12`), "not an assertion"},
		{"outside id of an inside-only assertion", replace(IDHolderHidden, IDEnvironment+OutsideSuffix), "not an assertion"},
		{"control byte in detail", replace(`"detail":"fine"`, `"detail":"a\u001bb"`), "detail"},
		{"newline in detail", replace(`"detail":"fine"`, `"detail":"a\nb"`), "detail"},
		{"nul in detail", replace(`"detail":"fine"`, `"detail":"a\u0000b"`), "detail"},
		{"c1 control in detail", replace(`"detail":"fine"`, `"detail":"a\u0085b"`), "detail"},
		{"line separator in detail", replace(`"detail":"fine"`, `"detail":"a\u2028b"`), "detail"},
		{"bidi override in detail", replace(`"detail":"fine"`, `"detail":"a\u202eb"`), "detail"},
		{"invalid utf8 in detail", mk(strings.Replace(body, `"fine"`, "\"a\xffb\"", 1)), "UTF-8"},
		{"detail too long", replace(`"detail":"fine"`, `"detail":"`+strings.Repeat("x", MaxDetailBytes+1)+`"`), "detail"},
		{"duplicate", replace(`{"number":5,"id":"tiocsti-blocked"`, `{"number":1,"id":"holder-hidden"`), "twice"},
		{"results is not a list", replace(`"results":[`, `"results":{"a":[`), "not a report"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseLine(tc.line)
			if err == nil {
				t.Fatalf("accepted %q", tc.line)
			}
			r := refusalOf(t, err)
			if !strings.Contains(r.Field+" "+r.Reason, tc.check) {
				t.Errorf("refusal %q does not mention %q", err, tc.check)
			}
		})
	}
	// Exactly MaxResults is allowed by count; one more is refused (the
	// duplicate check would also catch it, so use distinct outside IDs).
	var rep Report
	rep.Version, rep.Digest = ReportVersion, strings.Repeat("ab", 32)
	for i := 0; i < MaxResults+1; i++ {
		rep.Results = append(rep.Results, Result{1, fmt.Sprintf("x%d", i), StatusOK, ""})
	}
	if _, err := ParseLine(EncodeLine(rep)); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Errorf("too many results: %v", err)
	}
}

func TestEncodeLineEscapesAndBounds(t *testing.T) {
	r := goodReport()
	r.Results[0].Detail = "a\x1b]0;title\x07\nATANIQ-PROBE-REPORT {}\r\x00\xff" + strings.Repeat("y", 2*MaxDetailBytes)
	r.Results[1].Status = "great"
	line := EncodeLine(r)
	if bytes.Count(line, []byte("\n")) != 1 || bytes.ContainsAny(line[:len(line)-1], "\r\x1b\x00\x07") {
		t.Fatalf("control bytes in the line: %q", line)
	}
	back, err := ParseLine(line)
	if err != nil {
		t.Fatalf("ParseLine: %v", err)
	}
	if d := back.Results[0].Detail; len(d) > MaxDetailBytes || !strings.HasSuffix(d, "...") || !strings.Contains(d, `\u001b`) || !strings.Contains(d, `\xff`) {
		t.Errorf("detail %q", d)
	}
	if back.Results[1].Status != StatusFailed {
		t.Errorf("an unknown status was encoded as %q, want failed", back.Results[1].Status)
	}
}

func TestEscapeDetail(t *testing.T) {
	for _, tc := range []struct{ in, out string }{
		{"plain text", "plain text"},
		{"", ""},
		{"a\nb", `a\u000ab`},
		{"a\tb", `a\u0009b`},
		{"\x1b[31m", `\u001b[31m`},
		{"\x7f", `\u007f`},
		{"\x00", `\u0000`},
		{"\xff\xfe", `\xff\xfe`},
		{"é ü 日本", "é ü 日本"},
		{"a\u0085b", `a\u0085b`},
		{"a\u2028b", `a\u2028b`},
		{"a\u202eb", `a\u202eb`},
		{"\U000e0001", `\U000e0001`},
		{`back\slash`, `back\slash`},
		{`"quoted"`, `"quoted"`},
	} {
		if got := EscapeDetail(tc.in); got != tc.out {
			t.Errorf("EscapeDetail(%q) = %q, want %q", tc.in, got, tc.out)
		}
	}
}

func TestEscapeDetailProperties(t *testing.T) {
	inputs := []string{
		strings.Repeat("a", MaxDetailBytes),
		strings.Repeat("a", MaxDetailBytes+1),
		strings.Repeat("\n", MaxDetailBytes),
		strings.Repeat("\n", 3*MaxDetailBytes),
		strings.Repeat("日", MaxDetailBytes),
		strings.Repeat("\xff", MaxDetailBytes),
		strings.Repeat("a", MaxDetailBytes-3) + "\x00",
		strings.Repeat("a", MaxDetailBytes-5) + "\x00",
		strings.Repeat("a", 1<<20),
		string([]byte{0xe6, 0x97}), // a cut rune
	}
	for _, in := range inputs {
		out := EscapeDetail(in)
		if len(out) > MaxDetailBytes {
			t.Errorf("%d bytes in, %d out", len(in), len(out))
		}
		if !utf8.ValidString(out) {
			t.Errorf("invalid UTF-8 out for %d bytes in", len(in))
		}
		for _, r := range out {
			if r != ' ' && !unicode.IsPrint(r) {
				t.Errorf("non-printable %U in the output", r)
				break
			}
		}
		if again := EscapeDetail(out); again != out {
			t.Errorf("not idempotent: %q then %q", out, again)
		}
		if !cleanDetail(out) {
			t.Errorf("the output is not a clean detail")
		}
	}
	// Exactly at the bound is not cut.
	if got := EscapeDetail(strings.Repeat("a", MaxDetailBytes)); len(got) != MaxDetailBytes || strings.HasSuffix(got, "...") {
		t.Errorf("a detail of exactly the bound was cut")
	}
	if got := EscapeDetail(strings.Repeat("a", MaxDetailBytes+1)); len(got) != MaxDetailBytes || !strings.HasSuffix(got, "...") {
		t.Errorf("a detail over the bound was not cut to it")
	}
}

func TestReportWith(t *testing.T) {
	r := goodReport()
	w := r.With(Result{9, IDRoleLockLingering, StatusOK, ""})
	if len(r.Results) != 3 || len(w.Results) != 4 {
		t.Errorf("With changed the receiver or did not append: %d %d", len(r.Results), len(w.Results))
	}
}

func TestStatusValid(t *testing.T) {
	for _, s := range []Status{StatusOK, StatusFailed, StatusNotChecked} {
		if !s.Valid() {
			t.Errorf("%s", s)
		}
	}
	for _, s := range []Status{"", "OK", "pass", "unsupported"} {
		if s.Valid() {
			t.Errorf("%q", s)
		}
	}
}

func TestTable(t *testing.T) {
	as := Assertions()
	if len(as) != NumAssertions {
		t.Fatalf("%d assertions", len(as))
	}
	ids := map[string]bool{}
	inside := 0
	for i, a := range as {
		if a.Number != i+1 || a.ID == "" || a.Text == "" {
			t.Errorf("row %d: %+v", i, a)
		}
		if ids[a.ID] {
			t.Errorf("id %s twice", a.ID)
		}
		ids[a.ID] = true
		switch a.Where {
		case Inside, Outside, Both:
		default:
			t.Errorf("row %d where %q", i, a.Where)
		}
		if a.HasInside() {
			inside++
		}
		if len(a.Cases) == 0 {
			t.Errorf("assertion %d maps to no case", a.Number)
		}
	}
	if inside != 9 {
		t.Errorf("%d assertions have an inside half, want 9", inside)
	}
	if a, _ := ByNumber(11); a.Where != Inside || a.ID != IDParentFolders {
		t.Errorf("assertion 11 is %s %s, want inside and %s", a.Where, a.ID, IDParentFolders)
	}
	for _, n := range []int{1, 3, 5, 7} {
		if a, _ := ByNumber(n); a.Where != Both {
			t.Errorf("assertion %d is %s, want both", n, a.Where)
		}
	}
	for _, n := range []int{9, 10} {
		if a, _ := ByNumber(n); a.Where != Outside {
			t.Errorf("assertion %d is %s, want outside", n, a.Where)
		}
	}
	// The cases the task names.
	wantCase := map[int]string{1: "S1", 2: "S3", 3: "S2", 4: "S4", 5: "S7", 6: "S1", 7: "S16", 10: "S11", 11: "S1"}
	for n, c := range wantCase {
		a, _ := ByNumber(n)
		found := false
		for _, x := range a.Cases {
			found = found || x == c
		}
		if !found {
			t.Errorf("assertion %d does not map to %s: %v", n, c, a.Cases)
		}
	}
	// The copy is the caller's.
	as[0].Cases[0] = "changed"
	if a, _ := ByNumber(1); a.Cases[0] == "changed" {
		t.Error("Assertions returns the table's own slices")
	}
	if _, ok := ByNumber(0); ok {
		t.Error("ByNumber(0)")
	}
	if _, ok := ByNumber(12); ok {
		t.Error("ByNumber(12)")
	}
	if idOf(0) != "" || idOf(12) != "" {
		t.Error("idOf out of range")
	}
	// S21 is a case of assertion 7, and not of assertion 11.
	if a, _ := ByNumber(7); strings.Join(a.Cases, ",") != "S16,S21" {
		t.Errorf("assertion 7 maps to %v, want S16 and S21", a.Cases)
	}
	if a, _ := ByNumber(11); len(a.Cases) != 1 {
		t.Errorf("assertion 11 maps to %v, want S1 alone", a.Cases)
	}
	// The text of assertion 7 has both halves, as the design words them.
	if a, _ := ByNumber(7); !strings.Contains(a.Text, "with the previous instance proven stopped") || !strings.Contains(a.Text, "Inside, the tree is the lead's own clone") {
		t.Errorf("the text of assertion 7: %q", a.Text)
	}
}

// insideReport is a report with an ok result for every inside assertion.
func insideReport(s Spec) Report {
	r := Report{Version: ReportVersion, Digest: s.Digest}
	for _, a := range Assertions() {
		if a.HasInside() {
			r.Results = append(r.Results, Result{a.Number, a.ID, StatusOK, ""})
		}
	}
	return r
}

// The outside half of an assertion is never excused by the spec's optional
// list: a not-checked result that carries the number of the optional assertion
// and the ID of an outside half is still a problem. The table cannot make such a
// result (Validate allows only assertion 4, which has no outside half), so the
// test builds it by hand.
func TestProblemsOptionalExcusesOnlyTheInsideHalf(t *testing.T) {
	s := testSpec("claude")
	s.Optional = []int{OptionalAssertion}
	rep := insideReport(s).With(Result{OptionalAssertion, IDHolderHidden + OutsideSuffix, StatusNotChecked, "x"})
	p := Problems(rep, s, RequireInside)
	if len(p) != 1 || !strings.Contains(p[0], IDHolderHidden+OutsideSuffix) || !strings.Contains(p[0], "not checked") {
		t.Errorf("a not-checked result with an outside ID: %v", p)
	}
	// The inside result of the same number is excused.
	rep = insideReport(s)
	for i := range rep.Results {
		if rep.Results[i].Number == OptionalAssertion {
			rep.Results[i].Status = StatusNotChecked
		}
	}
	if p := Problems(rep, s, RequireInside); len(p) != 0 {
		t.Errorf("the inside result of the optional assertion: %v", p)
	}
}

func TestProblems(t *testing.T) {
	s := testSpec("claude")
	rep := insideReport(s)
	if p := Problems(rep, s, RequireInside); len(p) != 0 || Failed(rep, s) {
		t.Fatalf("a report of ok results: %v", p)
	}

	// A result that is missing.
	if p := Problems(Report{Version: ReportVersion, Digest: s.Digest, Results: rep.Results[:8]}, s, RequireInside); len(p) != 1 || !strings.Contains(p[0], "assertion 11") {
		t.Errorf("missing result: %v", p)
	}
	without8 := append(append([]Result(nil), rep.Results[:7]...), rep.Results[8:]...)
	if p := Problems(Report{Version: ReportVersion, Digest: s.Digest, Results: without8}, s, RequireInside); len(p) != 1 || !strings.Contains(p[0], "assertion 8") {
		t.Errorf("missing result: %v", p)
	}
	if !Failed(Report{}, s) {
		t.Error("an empty report passed")
	}
	// Another digest or another version.
	other := rep
	other.Digest = strings.Repeat("cd", 32)
	if !Failed(other, s) {
		t.Error("a report for another digest passed")
	}
	old := rep
	old.Version = 0
	if !Failed(old, s) {
		t.Error("a report of another version passed")
	}
	// A failed result, even for an optional assertion.
	failed := insideReport(s)
	failed.Results[3].Status = StatusFailed
	failed.Results[3].Detail = "x\ny"
	s.Optional = []int{4}
	p := Problems(failed, s, RequireInside)
	if len(p) != 1 || !strings.Contains(p[0], "failed") || strings.Contains(p[0], "\n") {
		t.Errorf("failed optional: %q", p)
	}
	// A not-checked result: a problem, unless optional.
	nc := insideReport(s)
	nc.Results[3].Status = StatusNotChecked
	if Failed(nc, s) {
		t.Errorf("not-checked and optional: %v", Problems(nc, s, RequireInside))
	}
	s.Optional = nil
	if !Failed(nc, s) {
		t.Error("not-checked and not optional passed")
	}
	// A status the format does not know is never ok.
	odd := insideReport(s)
	odd.Results[0].Status = "pass"
	if !Failed(odd, s) {
		t.Error("an unknown status passed")
	}
	// A spec that is refused allows nothing, whatever it lists as optional.
	s.Tree = "relative"
	s.Optional = []int{1, 2, 3, 4, 5, 6, 7, 8}
	if p := Problems(rep, s, RequireInside); len(p) != 1 || !strings.Contains(p[0], "spec is refused") {
		t.Errorf("refused spec: %v", p)
	}
	// RequireAll wants the outside results too: 9, 10 and the outside halves
	// of 1, 3, 5 and 7.
	good := testSpec("claude")
	p = Problems(rep, good, RequireAll)
	if len(p) != 6 {
		t.Fatalf("RequireAll without outside results: %v", p)
	}
	for _, id := range []string{IDHolderHidden + OutsideSuffix, IDLoopbackUnreach + OutsideSuffix, IDTIOCSTIBlocked + OutsideSuffix, IDTreeGitReadOnly + OutsideSuffix, IDRoleLockLingering, IDQualificationRecord} {
		found := false
		for _, line := range p {
			found = found || strings.Contains(line, id)
		}
		if !found {
			t.Errorf("no problem for %s: %v", id, p)
		}
	}
	all := rep.With(
		Result{1, IDHolderHidden + OutsideSuffix, StatusOK, ""}, Result{3, IDLoopbackUnreach + OutsideSuffix, StatusOK, ""},
		Result{5, IDTIOCSTIBlocked + OutsideSuffix, StatusOK, ""}, Result{7, IDTreeGitReadOnly + OutsideSuffix, StatusOK, ""},
		Result{9, IDRoleLockLingering, StatusOK, ""}, Result{10, IDQualificationRecord, StatusOK, ""},
	)
	if FailedAll(all, good) {
		t.Errorf("FailedAll: %v", Problems(all, good, RequireAll))
	}
	all.Results[len(all.Results)-1].Status = StatusFailed
	// Failed judges every result the report holds, so the outside failure
	// counts in both.
	if !FailedAll(all, good) || !Failed(all, good) {
		t.Error("an outside failure did not fail the report")
	}
}

// A line that EncodeLine makes from a valid report is one ParseLine takes,
// however many results and however the details read in JSON: no HTML
// escaping, and a limit that holds the largest valid report.
func TestLargestValidReportRoundTrips(t *testing.T) {
	for _, detail := range []string{
		strings.Repeat("<", MaxDetailBytes),
		strings.Repeat("&", MaxDetailBytes),
		strings.Repeat(`"`, MaxDetailBytes),
		strings.Repeat(`\`, MaxDetailBytes),
		strings.Repeat("\u00e9", MaxDetailBytes),
	} {
		var results []Result
		for _, a := range Assertions() {
			if a.HasInside() {
				results = append(results, Result{a.Number, a.ID, StatusNotChecked, detail})
			}
			if a.Where == Both {
				results = append(results, Result{a.Number, a.ID + OutsideSuffix, StatusNotChecked, detail})
			}
			if a.Where == Outside {
				results = append(results, Result{a.Number, a.ID, StatusNotChecked, detail})
			}
		}
		rep := Report{Version: ReportVersion, Digest: strings.Repeat("ab", 32), Results: results}
		line := EncodeLine(rep)
		if bytes.Contains(line, []byte(`\u003c`)) || bytes.Contains(line, []byte(`\u0026`)) {
			t.Errorf("a detail was HTML-escaped: %.80s", line)
		}
		back, err := ParseLine(line)
		if err != nil {
			t.Fatalf("%d results of %d bytes: %v", len(results), len(line), err)
		}
		if len(back.Results) != len(results) || back.Results[0].Detail != EscapeDetail(detail) {
			t.Errorf("the report changed in a round trip")
		}
	}
}

// The limits agree: even MaxResults results, each at the largest detail that
// JSON writes in two bytes a character, fit one line.
func TestReportLimitsAgree(t *testing.T) {
	worstResult := len(`{"number":10,"id":"host-loopback-unreachable.outside","status":"not-checked","detail":""},`) + 2*MaxDetailBytes
	header := len(ReportPrefix) + len(fmt.Sprintf(`{"version":%d,"digest":"`, ReportVersion)) + 64 + len(`","results":[]}`) + 1
	if got := header + MaxResults*worstResult; got > MaxReportLineBytes {
		t.Errorf("%d results of the longest kind take %d bytes, over MaxReportLineBytes %d", MaxResults, got, MaxReportLineBytes)
	}
	if got := len(allResultIDs()); got > MaxResults {
		t.Errorf("the table allows %d results, MaxResults is %d", got, MaxResults)
	}
}

func allResultIDs() []string {
	var ids []string
	for _, a := range Assertions() {
		ids = append(ids, a.ID)
		if a.Where == Both {
			ids = append(ids, a.ID+OutsideSuffix)
		}
	}
	return ids
}

func TestParseProbeLine(t *testing.T) {
	s := testSpec("claude")
	good := insideReport(s)
	line := EncodeLine(good)
	back, err := ParseProbeLine(line)
	if err != nil {
		t.Fatalf("a probe's line: %v", err)
	}
	if Failed(back, s) || len(back.Results) != 9 {
		t.Errorf("round trip: %d results, %v", len(back.Results), Problems(back, s, RequireInside))
	}
	// A report that failed is still a probe's report: it is judged, not refused.
	bad := insideReport(s)
	bad.Results[2].Status = StatusFailed
	if back, err := ParseProbeLine(EncodeLine(bad)); err != nil || !Failed(back, s) {
		t.Errorf("a failed probe report: %v", err)
	}

	with := func(extra ...Result) []byte { return EncodeLine(good.With(extra...)) }
	without := func(i int) []byte {
		r := good
		r.Results = append(append([]Result(nil), good.Results[:i]...), good.Results[i+1:]...)
		return EncodeLine(r)
	}
	for _, tc := range []struct {
		name  string
		line  []byte
		check string
	}{
		{"a forged outside half of 1", with(Result{1, IDHolderHidden + OutsideSuffix, StatusOK, ""}), "not a result the probe makes"},
		{"a forged outside half of 3", with(Result{3, IDLoopbackUnreach + OutsideSuffix, StatusOK, ""}), "not a result the probe makes"},
		{"a forged outside half of 5", with(Result{5, IDTIOCSTIBlocked + OutsideSuffix, StatusOK, ""}), "not a result the probe makes"},
		{"a forged outside half of 7", with(Result{7, IDTreeGitReadOnly + OutsideSuffix, StatusOK, ""}), "not a result the probe makes"},
		{"an outside half of 11, which has none", with(Result{11, IDParentFolders + OutsideSuffix, StatusOK, ""}), "not an assertion"},
		{"a forged assertion 9", with(Result{9, IDRoleLockLingering, StatusOK, ""}), "not a result the probe makes"},
		{"a forged assertion 10", with(Result{10, IDQualificationRecord, StatusOK, ""}), "not a result the probe makes"},
		{"an inside id twice", with(Result{2, IDEnvironment, StatusOK, ""}), "twice"},
		{"the first assertion missing", without(0), "assertion 1"},
		{"assertion 8 missing", without(7), "assertion 8"},
		{"the last assertion missing", without(8), "assertion 11"},
		{"no results", EncodeLine(Report{Version: ReportVersion, Digest: s.Digest}), "has no result"},
		{"not a report", []byte("hello\n"), "does not start"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseProbeLine(tc.line)
			if err == nil {
				t.Fatal("accepted")
			}
			if r := refusalOf(t, err); !strings.Contains(r.Field+" "+r.Reason, tc.check) {
				t.Errorf("refusal %q does not mention %q", err, tc.check)
			}
		})
	}
	// ParseLine still reads the full report, which holds both halves.
	full := good.With(Result{9, IDRoleLockLingering, StatusOK, ""})
	if _, err := ParseLine(EncodeLine(full)); err != nil {
		t.Errorf("ParseLine of a full report: %v", err)
	}
}

// The forged line of the review: ok results for the outside half and for 9
// and 10 in a probe's line must not let a holder that forgot its own checks
// pass the start.
func TestForgedOutsideResultsDoNotPassTheStart(t *testing.T) {
	s := testSpec("claude")
	forged := insideReport(s).With(
		Result{1, IDHolderHidden + OutsideSuffix, StatusOK, ""}, Result{3, IDLoopbackUnreach + OutsideSuffix, StatusOK, ""}, Result{5, IDTIOCSTIBlocked + OutsideSuffix, StatusOK, ""},
		Result{7, IDTreeGitReadOnly + OutsideSuffix, StatusOK, ""},
		Result{9, IDRoleLockLingering, StatusOK, ""}, Result{10, IDQualificationRecord, StatusOK, ""},
	)
	line := EncodeLine(forged)
	if _, err := ParseProbeLine(line); err == nil {
		t.Error("ParseProbeLine took a probe line with outside results")
	}
	// What the holder takes from the descriptor, then, holds inside results
	// only, and the holder's own checks are required by FailedAll.
	probe, err := ParseProbeLine(EncodeLine(insideReport(s)))
	if err != nil {
		t.Fatal(err)
	}
	if !FailedAll(probe, s) {
		t.Error("FailedAll passed a report without the holder's own results")
	}
}

func TestProblemsRefusesARepeatedID(t *testing.T) {
	s := testSpec("claude")
	rep := insideReport(s)
	rep.Results = append(rep.Results, Result{2, IDEnvironment, StatusOK, ""})
	p := Problems(rep, s, RequireInside)
	if len(p) != 1 || !strings.Contains(p[0], "more than one result") {
		t.Errorf("a repeated ID: %v", p)
	}
	// The first result being failed and the second ok must not pass.
	rep = insideReport(s)
	rep.Results[1].Status = StatusFailed
	rep.Results = append(rep.Results, Result{2, IDEnvironment, StatusOK, ""})
	if !Failed(rep, s) {
		t.Error("a failed result followed by an ok one passed")
	}
}
