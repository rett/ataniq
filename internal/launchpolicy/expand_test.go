package launchpolicy

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"ataniq/internal/pack"
	"ataniq/internal/pack/packtest"
)

const rid = "r731905"
const modeToken = "--mode={permission_mode}"

type trial struct {
	p *Policy
	l pack.Launch
	s Snapshot
}
type request struct {
	Provider, Version, Model, Effort, PermissionMode, AccessMode string
	Pack                                                         pack.Name
}

func fakeIntent(b []byte) (string, View, error) {
	var r request
	if err := json.Unmarshal(b, &r); err != nil {
		return "", View{}, err
	}
	digest := "d1"
	if r.AccessMode == "other" {
		digest = "d2"
	}
	if r.Provider != string(r.Pack) {
		return "", View{}, errors.New("unknown provider")
	}
	return digest, View{r.Pack, r.Version, r.PermissionMode, r.Model, r.Effort}, nil
}
func (x *trial) bind(t *testing.T) {
	access := "standard"
	if x.s.ProfileDigest == "d2" {
		access = "other"
	}
	x.s.Requested = wire(t, request{string(x.s.Pack), x.s.Version, x.s.Model, x.s.Effort, x.s.Mode, access, x.s.Pack})
}
func baseline(t *testing.T, name string) trial {
	t.Helper()
	x := trial{p: decode(t, alphaPolicy)}
	x.s = Snapshot{Pack: packtest.Alpha.Name(), Version: "1.0", Role: "demo/lead", Transport: pack.Terminal, Mode: "m-plan", Recognizer: "plan", ProfileDigest: "d1"}
	x.l = pack.Launch{Argv: []string{"alpha-cli", modeToken, "--model={model}", "--effort={effort}", "--agent={agent}", "--resume={resume}"},
		Env: map[string]string{"ALPHA_HOME": "/h"}, Grammars: map[pack.Slot]string{pack.ModelSlot: "^[a-z][a-z0-9.-]{0,31}$", pack.EffortSlot: "^(low|high)$"},
		PermissionModes: []pack.PermissionMode{{Name: "m-ask", Lead: true}, {Name: "m-auto"}, {Name: "m-edit", Lead: true}, {Name: "m-plan", Lead: true}, {Name: "m-quiet"}},
		Agents:          []pack.Agent{{Name: "builder"}, {Name: "reviewer"}}}
	if name != "BL" {
		x.s.Role = "demo/coord/f1"
		x.s.Mode = "m-quiet"
		x.s.Agent = "reviewer"
		x.s.Recognizer = "quiet"
	}
	if name == "BP" || name == "BQ" {
		x.s.Transport = pack.Protocol
		x.s.OwnerProtocol = true
		x.s.Recognizer = ""
	}
	if name == "BQ" {
		x.p = decode(t, betaPolicy)
		x.s.Pack = packtest.Beta.Name()
		x.s.Mode = "q-quiet"
		x.s.Agent = ""
		x.s.Model = "m-x"
		x.l = pack.Launch{Argv: []string{"beta-cli", modeToken, "--", "{model}"}, Grammars: map[pack.Slot]string{pack.ModelSlot: "^[a-z][a-z0-9.-]{0,31}$"}, PermissionModes: []pack.PermissionMode{{Name: "q-plan", Lead: true}, {Name: "q-quiet"}}}
	}
	x.bind(t)
	return x
}
func (x *trial) policy(t *testing.T, mutate func(obj)) {
	d := tree(t, Canonical(x.p))
	mutate(d)
	x.p = decode(t, wire(t, d))
}
func (x trial) expand(t *testing.T) Spec {
	t.Helper()
	spec, r := Expand(x.p, x.l, x.s)
	if r != nil {
		t.Fatal(r)
	}
	return spec
}

// Each refusal assertion includes the complete rendered line. Expected values
// are literals supplied by the case, never derived from the Refusal fields.
func line(t *testing.T, r *Refusal, code string, stage string, at string, detail ...string) {
	t.Helper()
	want := "launch refused [pack-launch-" + code + "] stage=" + stage + " at=" + at
	if len(detail) > 0 {
		want += " value=" + detail[0]
	}
	if r == nil {
		t.Fatalf("accepted; want %s", want)
	}
	if got := r.Error(); got != want || len(got) > 200 || strings.Contains(got, rid) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}
func TestT22RequiredAgent(t *testing.T) {
	x := baseline(t, "BC")
	x.l.Argv = slices.Delete(x.l.Argv, 4, 5)
	_, r := Expand(x.p, x.l, x.s)
	line(t, r, "required-missing", "1", "form[agent]")
	x = baseline(t, "BC")
	x.s.Agent = ""
	_, r = Expand(x.p, x.l, x.s)
	line(t, r, "required-missing", "1", "slot[agent]")
}
func TestT38aPositionsAfterOmission(t *testing.T) {
	x := baseline(t, "BQ")
	x.s.Model = ""
	if got := x.expand(t).Argv; !slices.Equal(got, []string{"beta-cli", "--mode=q-quiet"}) {
		t.Fatal(got)
	}
	x.policy(t, func(d obj) { formRow(d, "sep")["kinds"].(obj)["coordinator"] = "required" })
	if got := x.expand(t).Argv; !slices.Equal(got, []string{"beta-cli", "--mode=q-quiet", "--"}) {
		t.Fatal(got)
	}
	x = baseline(t, "BQ")
	x.policy(t, func(d obj) {
		f := formRow(d, "model")
		effort := obj{}
		for k, v := range f {
			effort[k] = v
		}
		effort["id"] = "effort"
		effort["slot"] = "effort"
		effort["after_separator_index"] = 1
		addForm(d, effort)
		d["slots"].(obj)["effort"] = tree(t, alphaPolicy)["slots"].(obj)["effort"]
	})
	x.l.Argv = append(x.l.Argv, "{effort}")
	if got := x.expand(t).Argv; !slices.Equal(got, []string{"beta-cli", "--mode=q-quiet", "--", "m-x"}) {
		t.Fatal(got)
	}
	x = baseline(t, "BL")
	x.policy(t, func(d obj) { formRow(d, "quiet")["at"] = obj{"index": 1} })
	x.l.Argv = []string{"alpha-cli", "--model={model}", "--quiet", modeToken}
	if got := x.expand(t).Argv; !slices.Equal(got, []string{"alpha-cli", "--quiet", "--mode=m-plan"}) {
		t.Fatal(got)
	}
	x = baseline(t, "BQ")
	x.s.Model = "beta-cli"
	if x.expand(t).Argv[3] != "beta-cli" {
		t.Fatal("lost positional value")
	}
}
