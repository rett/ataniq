package launchpolicy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ataniq/internal/pack"
	"ataniq/internal/pack/packtest"
)

func (x trial) judge(spec Spec) *Refusal { _, r := Judge(x.p, x.l, x.s, spec, fakeIntent); return r }
func (x trial) recheck(t *testing.T, spec Spec) *Refusal {
	t.Helper()
	var received Spec
	if err := json.Unmarshal(wire(t, spec), &received); err != nil {
		t.Fatal(err)
	}
	_, r := Recheck(x.p, received, x.s, fakeIntent)
	return r
}
func pass(t *testing.T, r *Refusal) {
	t.Helper()
	if r != nil {
		t.Fatal(r)
	}
}

func TestT1MissingPolicy(t *testing.T) {
	x := baseline(t, "BL")
	spec := x.expand(t)
	x.p = nil
	line(t, x.judge(spec), "policy-missing", "0", "policy")
}
func TestT2UnrecordedPolicy(t *testing.T) {
	x := baseline(t, "BL")
	spec := x.expand(t)
	x.p = decode(t, unrecorded(t, packtest.AlphaPolicy()), x.help)
	line(t, x.judge(spec), "policy-unrecorded", "0", "policy")
}
func TestSelection(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*trial)
		code   string
	}{
		{"T7/missing lead flag", func(x *trial) { x.l.PermissionModes[3].Lead = false }, "selection-lead-flag"},
		{"T7/bypass with lead flag", func(x *trial) { x.l.PermissionModes[1].Lead = true }, "selection-outside-policy"},
		{"T8/mode", func(x *trial) {
			x.l.PermissionModes = append(x.l.PermissionModes, pack.PermissionMode{Name: "m-extra"})
		}, "selection-outside-policy"},
		{"T8/agent", func(x *trial) { x.l.Agents = append(x.l.Agents, pack.Agent{Name: "eve"}) }, "selection-outside-policy"},
		{"T8/env", func(x *trial) { x.l.Env["X"] = "1" }, "selection-outside-policy"},
		{"T8/wider grammar", func(x *trial) {
			x.policy(t, func(d obj) { d["slots"].(obj)["model"].(obj)["reject"] = []string{"1", "EXTRA9", "Gpt"} })
			x.l.Grammars[pack.ModelSlot] += "|^EXTRA9$"
		}, "selection-outside-policy"},
		{"T8/narrower grammar", func(x *trial) { x.l.Grammars[pack.ModelSlot] = "^(m-x|m1)$" }, ""},
		{"T63/requested mode lacks lead flag", func(x *trial) {
			x.l.PermissionModes[2].Lead = false
			x.s.Mode = "m-edit"
			x.s.Recognizer = "edits"
			x.bind(t)
		}, "selection-lead-flag"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := baseline(t, "BL")
			tc.change(&x)
			r := x.judge(x.expand(t))
			if tc.code == "" {
				pass(t, r)
			} else {
				line(t, r, tc.code, "0", "policy")
			}
		})
	}
}
func TestVerdicts(t *testing.T) {
	for _, tc := range []struct {
		name, base string
		class      pack.ModeClass
		rec        string
	}{
		{"T9", "BL", pack.Plan, "plan"}, {"T33", "BQ", pack.NoPrompt, ""}, {"T66/terminal", "BL", pack.Plan, "plan"}, {"T66/protocol", "BP", pack.NoPrompt, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := baseline(t, tc.base)
			spec := x.expand(t)
			v, r := Judge(x.p, x.l, x.s, spec, fakeIntent)
			pass(t, r)
			if v != (Verdict{tc.class, tc.rec, x.p.Digest()}) {
				t.Fatal(v)
			}
			pass(t, x.recheck(t, spec))
		})
	}
}

func TestTemplateStage(t *testing.T) {
	// token lists here follow the executable unless the case supplies all of argv.
	for _, tc := range []struct {
		name, base       string
		argv             []string
		code, at, quoted string
	}{
		{"T10", "BL", []string{"--quiet"}, "required-missing", "form[mode]", ""},
		{"T11/request differs", "BL", []string{"--mode=m-edit"}, "literal-in-slot", "argv[1]", `"m-edit"`},
		{"T11/T12 request equal", "BL", []string{"--mode=m-edit"}, "literal-in-slot", "argv[1]", `"m-edit"`},
		{"T13/prefix", "BL", []string{"-m=x"}, "alias-spelling", "argv[1]", ""},
		{"T13/exact", "BL", []string{"-m", "m-edit"}, "alias-spelling", "argv[1]", ""},
		{"T14", "BL", []string{"--mode", "m-edit"}, "alias-spelling", "argv[1]", ""},
		{"T15/model in mode", "BL", []string{"--mode={model}"}, "slot-mismatch", "argv[1]", `"{model}"`},
		{"T15/mode in model", "BL", []string{"--model={permission_mode}"}, "slot-mismatch", "argv[1]", `"{permission_mode}"`},
		{"T16", "BL", []string{modeToken, modeToken}, "form-repeated", "argv[2]", `"{permission_mode}"`},
		{"T17", "BL", []string{"--mode=m-plan", modeToken}, "literal-in-slot", "argv[1]", `"m-plan"`},
		{"T18/optional twice", "BL", []string{modeToken, "--quiet", "--quiet"}, "form-repeated", "argv[3]", `"--quiet"`},
		{"T18/repeat limit", "BL", []string{modeToken, "--tag", "--tag", "--tag"}, "form-repeated", "argv[4]", `"--tag"`},
		{"T18/repeat allowed", "BL", []string{modeToken, "--tag", "--tag"}, "", "", ""},
		{"T19/token", "BL", []string{modeToken, "--yolo"}, "refused-option", "argv[2]", ""},
		{"T19/prefix", "BL", []string{modeToken, "--approve-all=1"}, "refused-option", "argv[2]", ""},
		{"T20/abbreviation", "BL", []string{modeToken, "--mod={permission_mode}"}, "unknown-token", "argv[2]", ""},
		{"T20/unknown", "BL", []string{modeToken, "--zzz"}, "unknown-token", "argv[2]", ""},
		{"T21/misplaced executable", "BL", []string{"--quiet", "alpha-cli", modeToken}, "form-position", "argv[0]", `"--quiet"`},
		{"T21/repeated executable", "BL", []string{modeToken, "alpha-cli"}, "form-position", "argv[2]", `"alpha-cli"`},
		{"T34/second separator", "BQ", []string{modeToken, "--", "{model}", "--"}, "form-repeated", "argv[4]", `"--"`},
		{"T36/extra positional", "BQ", []string{modeToken, "--", "{model}", "y"}, "unknown-token", "argv[4]", ""},
		{"T38", "BQ", []string{modeToken, "--", "{model}", "{model}"}, "form-repeated", "argv[4]", `"{model}"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := baseline(t, tc.base)
			spec := x.expand(t)
			if strings.Contains(tc.name, "T12") {
				x.s.Mode = "m-edit"
				x.s.Recognizer = "edits"
				x.bind(t)
			}
			if tc.name == "T21/misplaced executable" {
				x.l.Argv = tc.argv
			} else {
				x.l.Argv = append([]string{x.l.Argv[0]}, tc.argv...)
			}
			if tc.code == "" {
				pass(t, x.judge(x.expand(t)))
				return
			}
			r := x.judge(spec)
			if tc.quoted != "" {
				line(t, r, tc.code, "1", tc.at, tc.quoted)
			} else {
				line(t, r, tc.code, "1", tc.at)
			}
		})
	}
}

func TestT23KindAdmission(t *testing.T) {
	x := baseline(t, "BC")
	x.s.Resume = rid
	spec := x.expand(t)
	x.policy(t, func(d obj) { delete(formRow(d, "resume")["kinds"].(obj), "coordinator") })
	line(t, x.judge(spec), "form-not-admitted", "1", "argv[5]")
	x = baseline(t, "BL")
	spec = x.expand(t)
	x.policy(t, func(d obj) { delete(formRow(d, "agent")["kinds"].(obj), "lead") })
	line(t, x.judge(spec), "form-not-admitted", "1", "argv[4]")
}
func TestT24Ceiling(t *testing.T) {
	for _, slot := range slots {
		for _, value := range []string{"-x", "--yes", "--", "m-plan\n", "m-plan ", "a=b", "a,b", "a/b", `a"b`, "é", "--mode=m-edit"} {
			t.Run(string(slot)+"/"+strconv.Quote(value), func(t *testing.T) {
				x := baseline(t, "BL")
				spec := x.expand(t)
				fields := map[pack.Slot]*string{pack.PermissionModeSlot: &x.s.Mode, pack.ModelSlot: &x.s.Model, pack.EffortSlot: &x.s.Effort, pack.AgentSlot: &x.s.Agent, pack.ResumeSlot: &x.s.Resume}
				*fields[slot] = value
				line(t, x.judge(spec), "snapshot-invalid", "0", "snapshot")
				*fields[slot] = ""
				if slot == pack.PermissionModeSlot {
					x.s.Mode = "m-plan"
					spec.Argv = spec.Argv[:1]
				}
				spec.Argv = append(spec.Argv, string(x.p.bySlot[slot].Prefix)+value)
				r := x.recheck(t, spec)
				at := argvAt(len(spec.Argv) - 1)
				if slot == pack.ResumeSlot {
					line(t, r, "value-outside-ceiling", "2", at)
				} else {
					line(t, r, "value-outside-ceiling", "2", at, strconv.Quote(value))
				}
			})
		}
	}
}
func TestT25ModelLengths(t *testing.T) {
	for _, n := range []int{32, 33, 64} {
		x := baseline(t, "BL")
		x.s.Model = strings.Repeat("a", n)
		x.bind(t)
		if n == 64 {
			x.l.Grammars[pack.ModelSlot] = "^[a-z][a-z0-9.-]{0,63}$"
			x.policy(t, func(d obj) { d["slots"].(obj)["model"].(obj)["grammar"] = x.l.Grammars[pack.ModelSlot] })
		}
		r := x.judge(x.expand(t))
		if n == 33 {
			line(t, r, "value-grammar", "2", "argv[2]", strconv.Quote(x.s.Model))
		} else {
			pass(t, r)
		}
	}
}
func TestT26GrammarValues(t *testing.T) {
	for _, slot := range []string{"model", "effort"} {
		x := baseline(t, "BL")
		v := "Gpt"
		if slot == "model" {
			x.s.Model = v
		} else {
			v = "medium"
			x.s.Effort = v
		}
		x.bind(t)
		line(t, x.judge(x.expand(t)), "value-grammar", "2", "argv[2]", strconv.Quote(v))
	}
}
func TestT27UnadmittedValues(t *testing.T) {
	x := baseline(t, "BL")
	x.s.Mode = "m-unknown"
	x.bind(t)
	spec := x.expand(t)
	line(t, x.recheck(t, spec), "value-unadmitted", "2", "argv[1]", `"m-unknown"`)
	line(t, x.judge(spec), "selection-lead-flag", "0", "policy")
	x = baseline(t, "BC")
	x.s.Mode = "m-unknown"
	x.bind(t)
	line(t, x.judge(x.expand(t)), "value-unadmitted", "2", "argv[1]", `"m-unknown"`)
	x = baseline(t, "BL")
	spec = x.expand(t)
	spec.Argv = append(spec.Argv, "--agent=eve")
	line(t, x.recheck(t, spec), "value-unadmitted", "2", "argv[2]", `"eve"`)
}
func TestT28ModeClasses(t *testing.T) {
	for _, base := range []string{"BL", "BC"} {
		for _, mode := range []string{"m-auto", "m-quiet"} {
			x := baseline(t, base)
			x.s.Mode = mode
			x.bind(t)
			r := x.recheck(t, x.expand(t))
			if base == "BC" && mode == "m-quiet" {
				pass(t, r)
			} else {
				line(t, r, "mode-class-refused", "2", "argv[1]", strconv.Quote(mode))
			}
		}
	}
}
func TestT29PendingLeadVector(t *testing.T) {
	x := baseline(t, "BL")
	x.policy(t, func(d obj) { modeRow(d, "m-plan")["vector"].(obj)["terminal"] = "pending" })
	line(t, x.judge(x.expand(t)), "mode-vector-pending", "2", "argv[1]", `"m-plan"`)
}
func TestT30SpecEnvironment(t *testing.T) {
	x := baseline(t, "BL")
	spec := x.expand(t)
	spec.Env["ALPHA_HOME"] = "/h2"
	pass(t, x.recheck(t, spec))
	spec.Env["ALPHA_HOME"] = "/x"
	line(t, x.recheck(t, spec), "env-refused", "2", "env[ALPHA_HOME]")
	spec.Env = map[string]string{"LD_PRELOAD": "/x"}
	line(t, x.recheck(t, spec), "env-refused", "2", "env[LD_PRELOAD]")
}
func TestT31BoundsFirst(t *testing.T) {
	x := baseline(t, "BL")
	spec := x.expand(t)
	spec.Argv = append(spec.Argv, "--unknown")
	for len(spec.Argv) < 65 {
		spec.Argv = append(spec.Argv, "z")
	}
	line(t, x.recheck(t, spec), "bound", "2", "argv[64]")
	x.l.Argv = spec.Argv
	line(t, x.judge(spec), "bound", "1", "argv[64]")
	spec.Argv = append(spec.Argv[:3], strings.Repeat("x", 513))
	line(t, x.recheck(t, spec), "bound", "2", "argv[3]")
	x.l.Argv = spec.Argv
	line(t, x.judge(spec), "bound", "1", "argv[3]")
	spec.Argv = spec.Argv[:3]
	spec.Env = map[string]string{}
	for i := range 33 {
		spec.Env[fmt.Sprintf("E%02d", i)] = "x"
	}
	line(t, x.recheck(t, spec), "bound", "2", "env[E32]")
}
func TestSpecTokenPositions(t *testing.T) {
	for _, tc := range []struct {
		name, base      string
		argv            []string
		code, at, value string
	}{
		{"T32", "BL", []string{"alpha-cli", "--mode="}, "value-outside-ceiling", "argv[1]", `""`},
		{"T34", "BQ", []string{"beta-cli", "--mode=q-quiet", "--", "--"}, "value-outside-ceiling", "argv[3]", `"--"`},
		{"T35", "BQ", []string{"beta-cli", "--mode=q-quiet", "m-x"}, "unknown-token", "argv[2]", ""},
		{"T37", "BQ", []string{"beta-cli", "--mode=q-quiet", "--", "--mode=m"}, "value-outside-ceiling", "argv[3]", `"--mode=m"`},
		{"T40", "BL", []string{"alpha-cli"}, "required-missing", "form[mode]", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := baseline(t, tc.base)
			spec := x.expand(t)
			spec.Argv = tc.argv
			r := x.recheck(t, spec)
			if tc.value != "" {
				line(t, r, tc.code, "2", tc.at, tc.value)
			} else {
				line(t, r, tc.code, "2", tc.at)
			}
		})
	}
	t.Run("T36/invalid snapshot model", func(t *testing.T) {
		x := baseline(t, "BQ")
		spec := x.expand(t)
		x.s.Model = "--x"
		line(t, x.judge(spec), "snapshot-invalid", "0", "snapshot")
	})
}
func TestT39ExpectedMode(t *testing.T) {
	x := baseline(t, "BL")
	spec := x.expand(t)
	spec.Argv[1] = "--mode=m-edit"
	line(t, x.judge(spec), "template-expansion-differs", "2", "argv[1]")
	line(t, x.recheck(t, spec), "expected-differs", "3", "slot[permission_mode]")
}
func TestT42WrongRole(t *testing.T) {
	x := baseline(t, "BL")
	spec := x.expand(t)
	spec.Role = "demo/coord/f1"
	line(t, x.recheck(t, spec), "expected-role", "3", "snapshot")
}
func TestT43WrongTransport(t *testing.T) {
	x := baseline(t, "BL")
	spec := x.expand(t)
	spec.Transport = pack.Protocol
	line(t, x.recheck(t, spec), "expected-transport", "3", "snapshot")
	x.policy(t, func(d obj) { formRow(d, "model")["transports"] = []string{"protocol"} })
	line(t, x.judge(spec), "form-not-admitted", "1", "argv[2]")
}
func TestT44ExpectedRecognizer(t *testing.T) {
	for _, base := range []string{"BL", "BP"} {
		x := baseline(t, base)
		spec := x.expand(t)
		x.s.Recognizer = "manual"
		line(t, x.recheck(t, spec), "expected-recognizer", "3", "snapshot")
	}
}
func TestT45DigestAndView(t *testing.T) {
	x := baseline(t, "BL")
	spec := x.expand(t)
	spec.ProfileDigest = "d2"
	line(t, x.recheck(t, spec), "expected-digest", "3", "snapshot")
	x.s.ProfileDigest = ""
	line(t, x.recheck(t, spec), "snapshot-invalid", "0", "snapshot")
	x = baseline(t, "BL")
	spec = x.expand(t)
	x.s.ProfileDigest = "d2"
	x.bind(t)
	x.s.ProfileDigest = "d1"
	line(t, x.recheck(t, spec), "expected-digest", "0", "snapshot")
	x = baseline(t, "BL")
	x.s.Version = "2.0"
	x.p = decode(t, bytes.ReplaceAll(packtest.AlphaPolicy(), []byte("1.0"), []byte("2.0")), x.help)
	line(t, x.recheck(t, x.expand(t)), "expected-differs", "0", "snapshot")
	x.s.Requested = []byte("{")
	line(t, x.recheck(t, spec), "snapshot-invalid", "0", "snapshot")
	x = baseline(t, "BP")
	x.s.ProfileDigest = "d2"
	line(t, x.recheck(t, x.expand(t)), "expected-digest", "0", "snapshot")
}
func TestT46IndependentAgent(t *testing.T) {
	x := baseline(t, "BC")
	spec := x.expand(t)
	pass(t, x.judge(spec))
	spec.Argv[2] = "--agent=builder"
	line(t, x.judge(spec), "template-expansion-differs", "2", "argv[2]")
	line(t, x.recheck(t, spec), "expected-differs", "3", "slot[agent]")
}
func TestT47Unplaceable(t *testing.T) {
	x := baseline(t, "BL")
	spec := x.expand(t)
	x.s.Model = "m1"
	x.bind(t)
	x.policy(t, func(d obj) { dropForm(d, "model") })
	line(t, x.recheck(t, spec), "expected-unplaceable", "3", "slot[model]")
}
func TestT48Unexpected(t *testing.T) {
	x := baseline(t, "BL")
	spec := x.expand(t)
	spec.Argv = append(spec.Argv, "--model=m1")
	line(t, x.recheck(t, spec), "expected-unexpected", "3", "slot[model]")
}
func TestT49AbsentMode(t *testing.T) {
	for _, base := range []string{"BL", "BC"} {
		x := baseline(t, base)
		spec := x.expand(t)
		x.s.Mode = ""
		x.bind(t)
		if base == "BL" {
			line(t, x.judge(spec), "selection-lead-flag", "0", "policy")
		} else {
			line(t, x.judge(spec), "required-missing", "1", "slot[permission_mode]")
		}
	}
}
func TestT50ExtraFixedToken(t *testing.T) {
	x := baseline(t, "BL")
	spec := x.expand(t)
	spec.Argv = append(spec.Argv, "--quiet")
	line(t, x.judge(spec), "template-expansion-differs", "2", "argv[2]")
	pass(t, x.recheck(t, spec))
}
func TestT51ExpansionEquality(t *testing.T) {
	x := baseline(t, "BL")
	x.l.Argv = append(x.l.Argv, "--quiet")
	for _, tc := range []struct {
		edit func(*Spec)
		at   string
	}{
		{func(s *Spec) { s.Argv = slices.Delete(s.Argv, 2, 3) }, "argv[2]"},
		{func(s *Spec) { s.Argv[2] = "--tag" }, "argv[2]"},
		{func(s *Spec) { s.Env["ALPHA_HOME"] = "/h2" }, "env[ALPHA_HOME]"},
		{func(s *Spec) { s.Env["ALPHA_OPT"] = "1" }, "env[ALPHA_OPT]"},
		{func(s *Spec) { delete(s.Env, "ALPHA_HOME") }, "env[ALPHA_HOME]"},
	} {
		spec := x.expand(t)
		tc.edit(&spec)
		line(t, x.judge(spec), "template-expansion-differs", "2", tc.at)
	}
}
