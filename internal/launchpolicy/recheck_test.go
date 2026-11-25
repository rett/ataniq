package launchpolicy

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"testing"

	"ataniq/internal/pack"
	"ataniq/internal/pack/packtest"
)

func decodedLaunch(t *testing.T, l pack.Launch) pack.Launch {
	t.Helper()
	manifest := tree(t, wire(t, packtest.Alpha.Manifest()))
	manifest["schema"] = pack.DeclarationSchema
	for i := range l.Agents {
		l.Agents[i].Builtin = true
	}
	files := map[string][]byte{
		pack.ManifestPath: wire(t, manifest),
		pack.SandboxPath:  wire(t, obj{"schema": pack.DeclarationSchema, "builds": obj{"1.0": pack.Sandbox{Launch: &l}}}),
	}
	layout := pack.BindLayout{CheckExtraEnv: func(env map[string]string) error {
		if !maps.Equal(env, map[string]string{"ALPHA_HOME": "/h"}) {
			return errors.New("unexpected fixture env")
		}
		return nil
	}}
	d, err := pack.DecodeDeclaration(files, layout)
	if err != nil {
		t.Fatal(err)
	}
	return *d.Sandbox["1.0"].Launch
}
func TestT41Resume(t *testing.T) {
	x := baseline(t, "BL")
	x.l = decodedLaunch(t, x.l)
	fresh := x.expand(t)
	pass(t, x.judge(fresh))
	if !slices.Equal(fresh.Argv, []string{"alpha-cli", "--mode=m-plan"}) {
		t.Fatal(fresh.Argv)
	}
	x.s.Resume = rid
	resumed := x.expand(t)
	pass(t, x.judge(resumed))
	if !slices.Equal(resumed.Argv, []string{"alpha-cli", "--mode=m-plan", "--resume=" + rid}) {
		t.Fatal("incorrect resume expansion")
	}
	x.s.Resume = ""
	line(t, x.recheck(t, resumed), "expected-unexpected", "3", "slot[resume]")
	x.s.Resume = rid
	line(t, x.recheck(t, fresh), "expected-missing", "3", "slot[resume]")
	resumed.Argv[2] = "--resume=r2"
	line(t, x.recheck(t, resumed), "expected-differs", "3", "slot[resume]")
	resumed.Argv = append(resumed.Argv, "--resume="+rid)
	line(t, x.recheck(t, resumed), "form-repeated", "2", "argv[3]")
	resumed.Argv = resumed.Argv[:3]
	resumed.Argv[2] = "--resume:" + rid
	line(t, x.recheck(t, resumed), "unknown-token", "2", "argv[2]")
	resumed.Argv[2] = "--resume=" + rid + "x"
	line(t, x.recheck(t, resumed), "value-grammar", "2", "argv[2]")
	x.l.Argv[5] = "{resume}"
	line(t, x.judge(fresh), "form-position", "1", "argv[5]")
	x.l.Argv[5] = "--resume=" + rid
	line(t, x.judge(fresh), "literal-in-slot", "1", "argv[5]")
	x.policy(t, func(d obj) { dropForm(d, "resume") })
	x.l.Argv[5] = "--resume={model}"
	line(t, x.judge(fresh), "unknown-token", "1", "argv[5]")
}

func TestT52DeterministicPrecedence(t *testing.T) {
	x := baseline(t, "BL")
	spec := x.expand(t)
	spec.Role = "invalid"
	x.l.Argv = append(x.l.Argv, "--unknown")
	line(t, x.judge(spec), "unknown-token", "1", "argv[6]")
	for i := range 100 {
		spec.Env = map[string]string{}
		keys := []string{"ZED", "ALPHA_OPT"}
		if i%2 == 1 {
			slices.Reverse(keys)
		}
		for _, k := range keys {
			spec.Env[k] = "private"
		}
		line(t, x.recheck(t, spec), "env-refused", "2", "env[ALPHA_OPT]")
	}
	spec.Argv = append(spec.Argv, "--unknown", "--quiet", "--yolo")
	line(t, x.recheck(t, spec), "unknown-token", "2", "argv[2]")
}

func TestT59IndependentPrelude(t *testing.T) {
	x := baseline(t, "BL")
	spec := x.expand(t)
	x.p = decode(t, bytes.ReplaceAll(alphaPolicy, []byte("1.0"), []byte("2.0")))
	line(t, x.judge(spec), "policy-missing", "0", "policy")
	line(t, x.recheck(t, spec), "policy-missing", "0", "policy")
	x.s.Version = "2.0"
	line(t, x.recheck(t, spec), "expected-differs", "0", "snapshot")
	x = baseline(t, "BL")
	x.p = decode(t, unrecorded(t, alphaPolicy))
	line(t, x.recheck(t, spec), "policy-unrecorded", "0", "policy")
	x = baseline(t, "BL")
	x.s.Role = "invalid"
	line(t, x.recheck(t, spec), "snapshot-invalid", "0", "snapshot")
}
func TestT60RoleParsing(t *testing.T) {
	for _, role := range []string{"l1", "supervisor"} {
		x := baseline(t, "BL")
		spec := x.expand(t)
		x.s.Role = role
		line(t, x.judge(spec), "snapshot-invalid", "0", "snapshot")
	}
	x := baseline(t, "BC")
	x.policy(t, func(d obj) { delete(formRow(d, "agent")["kinds"].(obj), "lead") })
	pass(t, x.judge(x.expand(t)))
	x.s.Role = "demo/lead"
	x.s.Mode = "m-plan"
	x.s.Recognizer = "plan"
	x.bind(t)
	line(t, x.judge(Spec{}), "form-not-admitted", "1", "argv[4]")
}

func TestT61ProtocolAdmission(t *testing.T) {
	for _, change := range []func(*trial){
		func(x *trial) { x.s.Role = "demo/lead" }, func(x *trial) { x.s.OwnerProtocol = false },
		func(x *trial) { x.s.Mode = "m-ask"; x.bind(t) }, func(x *trial) { x.s.ProfileDigest = "d2"; x.bind(t) },
	} {
		x := baseline(t, "BP")
		change(&x)
		spec := x.expand(t)
		line(t, x.judge(spec), "transport-not-admitted", "0", "snapshot")
		line(t, x.recheck(t, spec), "transport-not-admitted", "0", "snapshot")
	}
	for _, fault := range []string{"transport", "build"} {
		d := tree(t, alphaPolicy)
		v := modeRow(d, "m-quiet")["vector"].(obj)
		if fault == "transport" {
			v["protocol"] = v["terminal"]
		} else {
			v["protocol"].(obj)["build"] = "2.0"
		}
		if _, err := Decode(wire(t, d), []byte(fixtureHelp)); err == nil {
			t.Fatal("observation qualified wrong " + fault)
		}
	}
	x := baseline(t, "BP")
	pass(t, x.judge(x.expand(t)))
}
func TestT62ModeAdmissionOrder(t *testing.T) {
	x := baseline(t, "BC")
	spec := x.expand(t)
	x.s.Mode = ""
	x.bind(t)
	line(t, x.judge(spec), "required-missing", "1", "slot[permission_mode]")
	for _, base := range []string{"BC", "BP"} {
		x := baseline(t, base)
		x.policy(t, func(d obj) { modeRow(d, "m-quiet")["vector"].(obj)[string(x.s.Transport)] = "pending" })
		r := x.judge(x.expand(t))
		if base == "BC" {
			line(t, r, "mode-vector-pending", "2", "argv[1]", `"m-quiet"`)
		} else {
			line(t, r, "transport-not-admitted", "0", "snapshot")
		}
	}
}
func TestT64GrammarCorpusAndUse(t *testing.T) {
	x := baseline(t, "BL")
	x.l.Grammars[pack.ModelSlot] += "|^EXTRA9$"
	spec := x.expand(t)
	pass(t, x.judge(spec))
	spec.Argv = append(spec.Argv, "--model=EXTRA9")
	line(t, x.judge(spec), "value-grammar", "2", "argv[2]", `"EXTRA9"`)
	x.l.Grammars[pack.ModelSlot] += "|^x/y$"
	line(t, x.judge(spec), "selection-outside-policy", "0", "policy")
	x = baseline(t, "BL")
	x.policy(t, func(d obj) { d["slots"].(obj)["model"].(obj)["grammar"] = "^m[-a-z0-9]{0,3}$" })
	pass(t, x.judge(x.expand(t)))
	// The declaration can also be narrower. The holder only knows the policy.
	x = baseline(t, "BL")
	x.l.Grammars[pack.ModelSlot] = "^(m-x|m1)$"
	x.s.Model = "another"
	x.bind(t)
	spec = x.expand(t)
	pass(t, x.recheck(t, spec))
	line(t, x.judge(spec), "value-unadmitted", "2", "argv[2]", `"another"`)
}
func TestT65DifferentView(t *testing.T) {
	x := baseline(t, "BL")
	var received Spec
	if err := json.Unmarshal(wire(t, x.expand(t)), &received); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*View){func(v *View) { v.Model = "other" }, func(v *View) { v.Pack = packtest.Beta.Name() }, func(v *View) { v.Version = "2.0" }, func(v *View) { v.Mode = "m-edit" }, func(v *View) { v.Effort = "high" }} {
		_, r := Recheck(x.p, received, x.s, func(b []byte) (string, View, error) {
			digest, v, err := fakeIntent(b)
			change(&v)
			return digest, v, err
		})
		line(t, r, "expected-differs", "0", "snapshot")
	}
}

func TestInvalidSnapshotIdentity(t *testing.T) {
	for _, change := range []func(*Snapshot){
		func(s *Snapshot) { s.Pack = "?" },
		func(s *Snapshot) { s.Version = "../x" },
		func(s *Snapshot) { s.Recognizer = "Invalid" },
		func(s *Snapshot) { s.Transport = "other" },
		func(s *Snapshot) { s.ProfileDigest = "d1\n" },
	} {
		x := baseline(t, "BL")
		spec := x.expand(t)
		change(&x.s)
		line(t, x.recheck(t, spec), "snapshot-invalid", "0", "snapshot")
	}
}

func TestUnsupportedTransportWithEmptyLaunch(t *testing.T) {
	x := baseline(t, "BQ")
	x.s.Transport = pack.Terminal
	x.s.Mode, x.s.Model = "", ""
	x.bind(t)
	x.l.Argv = nil
	spec := Spec{Role: x.s.Role, Transport: x.s.Transport, ProfileDigest: x.s.ProfileDigest}
	t.Run("expand", func(t *testing.T) {
		_, r := Expand(x.p, x.l, x.s)
		line(t, r, "transport-not-admitted", "0", "snapshot")
	})
	t.Run("judge", func(t *testing.T) {
		line(t, x.judge(spec), "transport-not-admitted", "0", "snapshot")
	})
	t.Run("recheck", func(t *testing.T) {
		line(t, x.recheck(t, spec), "transport-not-admitted", "0", "snapshot")
	})
	x.s.Requested = nil
	line(t, x.recheck(t, spec), "snapshot-invalid", "0", "snapshot")
}
