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
		{"fd leading zero", []string{"--spec", "/s", "--report-fd", "03", "--", "/a"}, "not a descriptor number"},
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

func TestParsePassFDs(t *testing.T) {
	got, err := parsePassFDs([]string{"--read", "5=/a/b", "--write", "6=/c=d", "--read", "3=/e", "--", "/bin/bwrap", "--x", "--", "y"})
	if err != nil {
		t.Fatal(err)
	}
	want := []fdFile{{5, "/a/b", false}, {6, "/c=d", true}, {3, "/e", false}}
	if len(got.Files) != len(want) {
		t.Fatalf("files %+v", got.Files)
	}
	for i := range want {
		if got.Files[i] != want[i] {
			t.Errorf("file %d: %+v, want %+v", i, got.Files[i], want[i])
		}
	}
	if strings.Join(got.Command, " ") != "/bin/bwrap --x -- y" {
		t.Errorf("command %q", got.Command)
	}
	if got, err := parsePassFDs([]string{"--", "/bin/true"}); err != nil || len(got.Files) != 0 {
		t.Errorf("no files: %+v, %v", got, err)
	}
	many := []string{}
	for fd := 3; fd < 3+maxPassFDs; fd++ {
		many = append(many, "--read", itoa(fd)+"=/f")
	}
	if got, err := parsePassFDs(append(many, "--", "/bin/true")); err != nil || len(got.Files) != maxPassFDs {
		t.Errorf("%d files: %v", maxPassFDs, err)
	}
	if maxPassFDs != 128 {
		t.Errorf("the cap is %d: the documentation and the changelog say 128", maxPassFDs)
	}

	bad := []struct {
		name string
		args []string
		want string
	}{
		{"nothing", nil, "required after the options"},
		{"no command", []string{"--read", "4=/f", "--"}, "command is empty"},
		{"no --", []string{"--read", "4=/f", "/bin/true"}, "unexpected argument"},
		{"relative command", []string{"--", "true"}, "command path must be absolute"},
		{"unknown option", []string{"--exec", "--", "/bin/true"}, "unknown option"},
		{"value missing", []string{"--read"}, "needs a value"},
		{"value is --", []string{"--write", "--", "/bin/true"}, "needs a value"},
		{"no equals", []string{"--read", "4/f", "--", "/bin/true"}, "the form is FD=PATH"},
		{"fd 2", []string{"--read", "2=/f", "--", "/bin/true"}, "descriptor is not a descriptor number"},
		{"fd 1024", []string{"--write", "1024=/f", "--", "/bin/true"}, "descriptor is not a descriptor number"},
		{"fd not a number", []string{"--read", "x=/f", "--", "/bin/true"}, "descriptor is not a descriptor number"},
		{"fd leading zero", []string{"--read", "04=/f", "--", "/bin/true"}, "descriptor is not a descriptor number"},
		{"fd zero zero", []string{"--write", "000=/f", "--", "/bin/true"}, "descriptor is not a descriptor number"},
		{"relative path", []string{"--read", "4=f", "--", "/bin/true"}, "path must be absolute"},
		{"empty path", []string{"--read", "4=", "--", "/bin/true"}, "path must be absolute"},
		{"same fd twice", []string{"--read", "4=/f", "--write", "4=/g", "--", "/bin/true"}, "descriptor 4 is given twice"},
		{"one file more than the cap", append(append([]string{}, many...), "--read", "999=/f", "--", "/bin/true"), "more than 128 descriptors"},
	}
	for _, tc := range bad {
		_, err := parsePassFDs(tc.args)
		var ue *usageError
		if !errors.As(err, &ue) {
			t.Errorf("%s: got %v, want a usage error", tc.name, err)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %q does not contain %q", tc.name, err, tc.want)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
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
		{"mode", modePassFDs, plan.PassFDsMode},
		{"read", optRead, plan.PassFDsRead},
		{"write", optWrite, plan.PassFDsWrite},
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
