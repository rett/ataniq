package tests

import (
	"strings"
	"testing"
)

// The holder's alternate-screen counters (alt_screen_on, alt_screen_off) say
// which renderer the agent drew with. Step 22 prints them from the final
// record with the 7501 counters, and step 16 names them as the thing to read.
func TestCanaryRunKitPrintsTheAlternateScreenCounters(t *testing.T) {
	const none = "     (alt_screen_on and alt_screen_off are not in the record)"
	for name, c := range map[string]struct{ fields, want string }{
		"both counters": {
			"child.exit_status=0\ncounters.alt_screen_off=1\ncounters.alt_screen_on=2\ncounters.osc7501-reports=4\nversion=1",
			"     counters.alt_screen_off=1\n     counters.alt_screen_on=2",
		},
		"main-screen agent":      {"counters.alt_screen_off=0\ncounters.alt_screen_on=0", "     counters.alt_screen_off=0\n     counters.alt_screen_on=0"},
		"none in the record":     {"child.exit_status=0\ncounters.osc7501-reports=4\nversion=1", none},
		"named in another value": {"screen.text=sha256:alt_screen_on\ndetail=counters.x=1", none},
		"an empty record":        {"", none},
	} {
		t.Run(name, func(t *testing.T) {
			if got, code := canarySelfTest(t, "counters-alt", c.fields); code != 0 || got != c.want {
				t.Errorf("exit status %d, got:\n%s\nwant:\n%s", code, got, c.want)
			}
		})
	}
	// The 7501 printer still prints only its own.
	if got, _ := canarySelfTest(t, "counters-7501", "counters.alt_screen_on=1\ncounters.osc7501-reports=4"); got != "     counters.osc7501-reports=4" {
		t.Errorf("counters-7501 printed %q", got)
	}

	funcs := canaryFuncs(t, canaryScriptText(t))
	if !strings.Contains(funcs["step_22"], `counters_alt "$_fin" || out '`+none+`'`) {
		t.Error("step 22 does not print the alternate-screen counters of the final record")
	}
	sixteen := funcs["step_16"]
	for _, want := range []string{"alt_screen_on", "fullscreen renderer", "every inject ends not-now"} {
		if !strings.Contains(sixteen, want) {
			t.Errorf("step 16 does not say %q", want)
		}
	}
	if canaryCalls(sixteen, "counters_alt") != 0 {
		t.Error("step 16 prints the counters from the final record, which does not exist while the holder runs")
	}
	out, _ := canaryDry(t, "--pass", "B", "--config", "/nonexistent/sample/canary.json")
	for _, b := range canaryBlocks(out) {
		switch {
		case strings.HasPrefix(b.heading, "== step 16 [pass B]: "):
			if does, _ := b.line("does:"); !strings.Contains(does, "alt_screen_on above 0 means the agent drew with the fullscreen renderer") {
				t.Errorf("the dry run of step 16 does not name alt_screen_on: %s", does)
			}
		case strings.HasPrefix(b.heading, "== step 22 "):
			if b.count("print the 7501 counters and the alternate-screen counters (alt_screen_on, alt_screen_off)") != 1 {
				t.Errorf("the dry run of step 22 does not print the alternate-screen counters:\n%s", strings.Join(b.lines, "\n"))
			}
		}
	}
	for _, f := range []string{"qualification/canary/run/README.md", "qualification/canary/run/checklist-b.txt.in"} {
		if text := readFileText(t, f); !strings.Contains(text, "alt_screen_on") {
			t.Errorf("%s does not name alt_screen_on", f)
		}
	}
}
