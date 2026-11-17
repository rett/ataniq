package main

import (
	"errors"
	"strings"
	"testing"

	"ataniq/internal/leadsandbox/plan"
)

func TestParseInside(t *testing.T) {
	ok := []struct {
		name string
		args []string
		want insideArgs
	}{
		{"plain", []string{"--spec", "/s/spec.json", "--report-fd", "3", "--", "/bin/agent"},
			insideArgs{"/s/spec.json", 3, []string{"/bin/agent"}}},
		{"other order, agent arguments, a later --", []string{"--report-fd", "1023", "--spec", "/s", "--", "/a", "--spec", "x", "--", "-y"},
			insideArgs{"/s", 1023, []string{"/a", "--spec", "x", "--", "-y"}}},
	}
	for _, tc := range ok {
		got, err := parseInside(tc.args)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if got.Spec != tc.want.Spec || got.ReportFD != tc.want.ReportFD || strings.Join(got.Agent, "\x00") != strings.Join(tc.want.Agent, "\x00") {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}
	bad := []struct {
		name string
		args []string
		want string
	}{
		{"no arguments", nil, "--spec is required"},
		{"no report fd", []string{"--spec", "/s", "--", "/a"}, "--report-fd is required"},
		{"no spec", []string{"--report-fd", "3", "--", "/a"}, "--spec is required"},
		{"no --", []string{"--spec", "/s", "--report-fd", "3"}, "required after the options"},
		{"no -- and an agent", []string{"--spec", "/s", "--report-fd", "3", "/a"}, "unexpected argument"},
		{"empty agent", []string{"--spec", "/s", "--report-fd", "3", "--"}, "agent command is empty"},
		{"relative agent", []string{"--spec", "/s", "--report-fd", "3", "--", "agent"}, "agent path must be absolute"},
		{"empty agent path", []string{"--spec", "/s", "--report-fd", "3", "--", ""}, "agent path must be absolute"},
		{"relative spec", []string{"--spec", "s.json", "--report-fd", "3", "--", "/a"}, "--spec must be an absolute path"},
		{"unknown option", []string{"--spec", "/s", "--report-fd", "3", "--verbose", "--", "/a"}, "unknown option"},
		{"unknown option first", []string{"-x"}, "unknown option"},
		{"equals form", []string{"--spec=/s", "--report-fd", "3", "--", "/a"}, "unknown option"},
		{"value missing", []string{"--spec", "/s", "--report-fd"}, "--report-fd needs a value"},
		{"value is --", []string{"--spec", "--", "/a"}, "--spec needs a value"},
		{"spec twice", []string{"--spec", "/s", "--spec", "/t", "--report-fd", "3", "--", "/a"}, "given twice"},
		{"fd twice", []string{"--spec", "/s", "--report-fd", "3", "--report-fd", "4", "--", "/a"}, "given twice"},
		{"fd 2", []string{"--spec", "/s", "--report-fd", "2", "--", "/a"}, "not a descriptor number from 3 to 1023"},
		{"fd 0", []string{"--spec", "/s", "--report-fd", "0", "--", "/a"}, "not a descriptor number"},
		{"fd 1024", []string{"--spec", "/s", "--report-fd", "1024", "--", "/a"}, "not a descriptor number"},
		{"fd negative", []string{"--spec", "/s", "--report-fd", "-3", "--", "/a"}, "not a descriptor number"},
		{"fd plus", []string{"--spec", "/s", "--report-fd", "+3", "--", "/a"}, "not a descriptor number"},
		{"fd spaced", []string{"--spec", "/s", "--report-fd", " 3", "--", "/a"}, "not a descriptor number"},
		{"fd hex", []string{"--spec", "/s", "--report-fd", "0x5", "--", "/a"}, "not a descriptor number"},
		{"fd empty", []string{"--spec", "/s", "--report-fd", "", "--", "/a"}, "not a descriptor number"},
		{"fd long", []string{"--spec", "/s", "--report-fd", "00003", "--", "/a"}, "not a descriptor number"},
		{"fd huge", []string{"--spec", "/s", "--report-fd", "99999999999999999999", "--", "/a"}, "not a descriptor number"},
	}
	for _, tc := range bad {
		_, err := parseInside(tc.args)
		var ue *usageError
		if !errors.As(err, &ue) {
			t.Errorf("%s: got %v, want a usage error", tc.name, err)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %q does not contain %q", tc.name, err, tc.want)
		}
		if strings.Contains(err.Error(), "\n") {
			t.Errorf("%s: the message has a newline", tc.name)
		}
	}
}

func TestQuoteClips(t *testing.T) {
	if got := quote(strings.Repeat("x", 100)); len(got) > 70 {
		t.Errorf("quote did not clip: %d bytes", len(got))
	}
}

// TestAgreesWithThePlan checks the words of the command line against the
// package that writes it.
func TestAgreesWithThePlan(t *testing.T) {
	for _, c := range []struct{ name, got, want string }{
		{"report fd", optReportFD, plan.ProbeReportFD},
	} {
		if c.got != c.want {
			t.Errorf("%s: the probe reads %q, the plan writes %q", c.name, c.got, c.want)
		}
	}
	if minFD != 3 || maxFD != 1023 {
		t.Errorf("descriptor bounds %d..%d", minFD, maxFD)
	}
}
