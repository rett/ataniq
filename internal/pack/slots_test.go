package pack_test

import (
	"fmt"
	"strings"
	"testing"

	"ataniq/internal/pack"
	"ataniq/internal/pack/packtest"
)

const modelGrammar = `^[A-Za-z0-9][A-Za-z0-9._:\[\]-]{0,63}$`

func sampleLaunch() pack.Launch {
	return pack.Launch{
		Argv:            []string{"run", "--model={model}", "--effort={effort}", "--mode={permission_mode}", "--agent={agent}", "--resume={resume}"},
		Agents:          []pack.Agent{{Name: "sample", Builtin: true}},
		PermissionModes: []pack.PermissionMode{{Name: "m-plan", Lead: true}},
		Grammars:        map[pack.Slot]string{pack.ModelSlot: modelGrammar, pack.EffortSlot: `^(low|high)$`},
	}
}

func validateSandbox(s pack.Sandbox) error {
	d := pack.Declaration{Manifest: packtest.Alpha.Manifest(), Sandbox: map[string]pack.Sandbox{"1.0": s}}
	return d.Validate(sampleLayout())
}

func TestSlotPlacement(t *testing.T) {
	for _, tc := range []struct {
		token        string
		after, valid bool
	}{
		{"--name={model}", false, true}, {"{model}", true, true}, {"--resume={resume}", false, true},
		{"{model}", false, false}, {"pre{model}", false, false}, {"={model}", false, false},
		{"--m=x{model}", false, false}, {"--m={model}x", false, false}, {"--m={model}{effort}", false, false},
		{"--{effort}={model}", false, false}, {"--m={other}", false, false}, {"--m={}", false, false},
		{"--m={model", false, false}, {"--m=model}", false, false}, {"{resume}", true, false},
		{"--alias={resume}", false, false}, {"{agent}", true, true}, {"--flag", false, true},
	} {
		t.Run(tc.token, func(t *testing.T) {
			l := sampleLaunch()
			l.Argv = []string{"run"}
			if tc.after {
				l.Argv = append(l.Argv, "--")
			}
			l.Argv = append(l.Argv, tc.token)
			err := validateSandbox(pack.Sandbox{Launch: &l})
			if (err == nil) != tc.valid {
				t.Errorf("%q: %v", l.Argv, err)
			}
		})
	}
}

func TestPositionalSlotsAfterSeparator(t *testing.T) {
	l := sampleLaunch()
	l.Argv = []string{"run", "--", "{model}", "fixed", "{effort}"}
	if err := validateSandbox(pack.Sandbox{Launch: &l}); err != nil {
		t.Fatal(err)
	}
	l.Argv = append(l.Argv, "{resume}")
	if err := validateSandbox(pack.Sandbox{Launch: &l}); err == nil {
		t.Error("accepted positional resume")
	}
}

func TestLaunchExtraEnv(t *testing.T) {
	env := make(map[string]string)
	for i := range 64 {
		env[fmt.Sprintf("SAMPLE_%d", i)] = strings.Repeat("x", 256)
	}
	check := func(wantValid bool) {
		t.Helper()
		l := pack.Launch{Argv: []string{"run"}, Env: env}
		if err := validateSandbox(pack.Sandbox{Launch: &l}); (err == nil) != wantValid {
			t.Errorf("env with %d names, want valid=%v: %v", len(env), wantValid, err)
		}
	}
	check(true)
	env["ONE_MORE"] = "x"
	check(false)
	env = map[string]string{"SAMPLE": strings.Repeat("x", 257)}
	check(false)
	env = map[string]string{"PATH": "example"}
	check(false)
	env = map[string]string{"SAMPLE_TOKEN": "example"}
	check(false)

	d := pack.Declaration{Manifest: packtest.Alpha.Manifest(), Sandbox: map[string]pack.Sandbox{
		"1.0": {Launch: &pack.Launch{Argv: []string{"run"}, Env: map[string]string{"SAMPLE": "on"}}},
	}}
	if err := d.Validate(pack.BindLayout{}); err == nil {
		t.Error("accepted extra environment without a profile check")
	}
}

func TestSlotValues(t *testing.T) {
	l := sampleLaunch()
	for slot, good := range map[pack.Slot]string{
		pack.ModelSlot: "M2.x_y:[a]-b", pack.EffortSlot: "low", pack.PermissionModeSlot: "m-plan",
		pack.AgentSlot: "sample", pack.ResumeSlot: "r-1",
	} {
		t.Run(string(slot), func(t *testing.T) {
			if !slot.Valid() {
				t.Fatal("slot missing from closed set")
			}
			if err := l.CheckSlotValue(slot, good); err != nil {
				t.Fatal(err)
			}
			for _, bad := range []string{"-x", "--yes", "--", "", "a\nx", "\nx", "x\n", "a=b", "a,b", "a/b", "a b", "a'b", `a"b`, "a@b", "_x", "[a]", strings.Repeat("a", 65)} {
				if err := l.CheckSlotValue(slot, bad); err == nil {
					t.Errorf("accepted %s value %q", slot, bad)
				}
			}
		})
	}
	for _, tc := range []struct {
		slot  pack.Slot
		value string
	}{
		{pack.EffortSlot, "medium"}, {pack.PermissionModeSlot, "other"}, {pack.AgentSlot, "other"}, {"access_mode", "x"},
	} {
		if err := l.CheckSlotValue(tc.slot, tc.value); err == nil {
			t.Errorf("accepted %s %q", tc.slot, tc.value)
		}
	}
	if err := l.CheckSlotValue(pack.ModelSlot, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	// A corpus can miss a value. Check the ceiling independently at use time.
	l.Grammars[pack.ModelSlot] = `^(allowed|abc/def)$`
	if err := l.CheckSlotValue(pack.ModelSlot, "abc/def"); err == nil {
		t.Error("declared grammar overrode core's ceiling")
	}
}

func TestDeclaredGrammar(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		valid   bool
	}{
		{`(?:^low$){1}`, true}, {`(?:^low$)+`, true}, {`(?:^low$){1,3}`, true},
		{`(?:^low$)?`, false}, {`(?:^low$)*`, false}, {`(?:^low$){0,3}`, false},
		{`(?:low$)+`, false}, {`(?:^low){1}`, false},
		{modelGrammar, true}, {`^[A-Z]{1,3}$`, true}, {`^a$|^b$`, true},
		{`^(abc|def)$`, true}, {`^x|y$`, false}, {`(?m)^x$`, false}, {`^x\$`, false},
		{`[a-z]+`, false}, {`^([a-z]$`, false}, {`^.*$`, false},
		{`^[A-Za-z0-9][A-Za-z0-9,]{0,63}$`, false}, {`^[a-z]{1,65}$`, false},
	} {
		for _, slot := range []pack.Slot{pack.ModelSlot, pack.EffortSlot} {
			l := sampleLaunch()
			l.Grammars[slot] = tc.pattern
			if err := validateSandbox(pack.Sandbox{Launch: &l}); (err == nil) != tc.valid {
				t.Errorf("%s grammar %q: %v", slot, tc.pattern, err)
			}
		}
	}
	for _, setup := range []func(*pack.Launch){
		func(l *pack.Launch) { delete(l.Grammars, pack.ModelSlot) },
		func(l *pack.Launch) { l.Grammars[pack.AgentSlot] = modelGrammar },
		func(l *pack.Launch) { l.Agents = nil }, func(l *pack.Launch) { l.PermissionModes = nil },
		func(l *pack.Launch) { l.PermissionModes[0].Name = "--yes" },
		func(l *pack.Launch) { l.Agents[0].Name = "x/y" },
	} {
		l := sampleLaunch()
		setup(&l)
		if err := validateSandbox(pack.Sandbox{Launch: &l}); err == nil {
			t.Errorf("accepted invalid launch declaration %+v", l)
		}
	}
}

func TestModeClasses(t *testing.T) {
	for _, c := range []pack.ModeClass{"ask", "plan", "accept-edits", "no-prompt", "bypass"} {
		if !c.Valid() {
			t.Errorf("missing mode class %q", c)
		}
	}
	for _, c := range []pack.ModeClass{"", "auto", "manual", "unknown"} {
		if c.Valid() {
			t.Errorf("accepted unknown mode class %q", c)
		}
	}
	if pack.Slot("other").Valid() {
		t.Error("accepted unknown slot")
	}
}
