package leadunit

import (
	"errors"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func validSpec() Spec {
	return Spec{
		Unit:    "ataniq-lead-demo.service",
		Epoch:   7,
		Intent:  "intent-0001",
		Argv:    []string{"/usr/bin/lead", "--flag", "x y"},
		WorkDir: "/srv/work",
		Env:     []string{"HOME=/srv/work", "EMPTY=", "A_1=b=c"},
	}
}

func TestSpecValidateAccepts(t *testing.T) {
	if err := validSpec().Validate(); err != nil {
		t.Fatalf("valid spec refused: %v", err)
	}
	s := validSpec()
	s.Unit = "ataniq-supervisor.service"
	s.Env = nil
	s.Argv = []string{"/bin/x"}
	if err := s.Validate(); err != nil {
		t.Fatalf("minimal spec refused: %v", err)
	}
	s.Unit = "a:b_c.d@e-f.service"
	if err := s.Validate(); err != nil {
		t.Fatalf("every allowed unit character refused: %v", err)
	}
}

func TestSpecValidateRefuses(t *testing.T) {
	long := strings.Repeat("a", maxUnitName)
	cases := []struct {
		name string
		mut  func(*Spec)
	}{
		{"unit empty", func(s *Spec) { s.Unit = "" }},
		{"unit no suffix", func(s *Spec) { s.Unit = "ataniq-lead-demo" }},
		{"unit wrong suffix", func(s *Spec) { s.Unit = "ataniq-lead-demo.scope" }},
		{"unit only suffix", func(s *Spec) { s.Unit = ".service" }},
		{"unit space", func(s *Spec) { s.Unit = "a b.service" }},
		{"unit slash", func(s *Spec) { s.Unit = "a/b.service" }},
		{"unit nul", func(s *Spec) { s.Unit = "a\x00b.service" }},
		{"unit newline", func(s *Spec) { s.Unit = "a\nb.service" }},
		{"unit non-ascii", func(s *Spec) { s.Unit = "ä.service" }},
		{"unit too long", func(s *Spec) { s.Unit = long + unitSuffix }},
		{"unit trailing newline", func(s *Spec) { s.Unit = "a.service\n" }},
		{"epoch zero", func(s *Spec) { s.Epoch = 0 }},
		{"intent empty", func(s *Spec) { s.Intent = "" }},
		{"intent space", func(s *Spec) { s.Intent = "a b" }},
		{"intent newline", func(s *Spec) { s.Intent = "a\nb" }},
		{"intent nul", func(s *Spec) { s.Intent = "a\x00" }},
		{"intent too long", func(s *Spec) { s.Intent = strings.Repeat("i", maxIntentLen+1) }},
		{"argv nil", func(s *Spec) { s.Argv = nil }},
		{"argv0 empty", func(s *Spec) { s.Argv = []string{""} }},
		{"argv0 relative", func(s *Spec) { s.Argv = []string{"lead"} }},
		{"argv0 dot relative", func(s *Spec) { s.Argv = []string{"./lead"} }},
		{"argv0 nul", func(s *Spec) { s.Argv = []string{"/bin/a\x00b"} }},
		{"argv0 newline", func(s *Spec) { s.Argv = []string{"/bin/a\nb"} }},
		{"argv arg nul", func(s *Spec) { s.Argv = []string{"/bin/a", "x\x00"} }},
		{"argv arg newline", func(s *Spec) { s.Argv = []string{"/bin/a", "x\ny"} }},
		{"workdir empty", func(s *Spec) { s.WorkDir = "" }},
		{"workdir relative", func(s *Spec) { s.WorkDir = "work" }},
		{"workdir nul", func(s *Spec) { s.WorkDir = "/a\x00" }},
		{"workdir newline", func(s *Spec) { s.WorkDir = "/a\n" }},
		{"env no equals", func(s *Spec) { s.Env = []string{"HOME"} }},
		{"env empty key", func(s *Spec) { s.Env = []string{"=x"} }},
		{"env key digit first", func(s *Spec) { s.Env = []string{"1A=x"} }},
		{"env key dash", func(s *Spec) { s.Env = []string{"A-B=x"} }},
		{"env key space", func(s *Spec) { s.Env = []string{"A B=x"} }},
		{"env value nul", func(s *Spec) { s.Env = []string{"A=x\x00"} }},
		{"env value newline", func(s *Spec) { s.Env = []string{"A=x\ny"} }},
		{"env duplicate key", func(s *Spec) { s.Env = []string{"A=1", "A=2"} }},
		{"hardening zero value", func(s *Spec) { s.Hardening = []Hardening{{}} }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := validSpec()
			c.mut(&s)
			err := s.Validate()
			if err == nil {
				t.Fatalf("spec accepted: %+v", s)
			}
			if !errors.Is(err, ErrInvalidSpec) {
				t.Fatalf("error does not wrap ErrInvalidSpec: %v", err)
			}
		})
	}
}

func TestValidateUnitName(t *testing.T) {
	if err := ValidateUnitName("ataniq-lead-x.service"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateUnitName("x.timer"); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("got %v", err)
	}
}

func TestFixedProperties(t *testing.T) {
	want := []Property{
		{"Type", "exec"},
		{"CollectMode", "inactive-or-failed"},
		{"KillMode", "control-group"},
		{"Restart", "no"},
		{"TimeoutStopSec", "2s"},
		{"NoNewPrivileges", "yes"},
	}
	got := FixedProperties()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	got[0].Value = "changed"
	if FixedProperties()[0].Value != "exec" {
		t.Fatal("FixedProperties shares its slice between calls")
	}
}

func TestHardeningClosedSet(t *testing.T) {
	if p, ok := (Hardening{}).Property(); ok || p != (Property{}) {
		t.Fatalf("zero Hardening is a property: %v %v", p, ok)
	}
}

func TestDescriptionRoundTrip(t *testing.T) {
	for _, c := range []struct {
		epoch  uint64
		intent string
	}{
		{1, "a"},
		{7, "intent-0001"},
		{math.MaxUint64, "01HZX:y_z.w@v-u"},
		{10, strings.Repeat("i", maxIntentLen)},
	} {
		s := validSpec()
		s.Epoch, s.Intent = c.epoch, c.intent
		desc := s.Description()
		want := "ataniq lead epoch=" + strconv.FormatUint(c.epoch, 10) + " intent=" + c.intent
		if desc != want {
			t.Fatalf("description %q, want %q", desc, want)
		}
		e, i, ok := ParseDescription(desc)
		if !ok || e != c.epoch || i != c.intent {
			t.Fatalf("round trip of %q gave %d %q %v", desc, e, i, ok)
		}
	}
}

func TestParseDescriptionStrict(t *testing.T) {
	for _, d := range []string{
		"",
		"ataniq lead",
		"ataniq lead epoch= intent=x",
		"ataniq lead epoch=0 intent=x",
		"ataniq lead epoch=01 intent=x",
		"ataniq lead epoch=+1 intent=x",
		"ataniq lead epoch=-1 intent=x",
		"ataniq lead epoch=1.0 intent=x",
		"ataniq lead epoch=0x1 intent=x",
		"ataniq lead epoch=1 intent=",
		"ataniq lead epoch=1 intent=a b",
		"ataniq lead epoch=1 intent=a\n",
		"ataniq lead epoch=1 intent=a\x00",
		"ataniq lead epoch=1 intent=a intent=b",
		"ataniq lead epoch=1  intent=x",
		"ataniq lead epoch=1 intent=x ",
		" ataniq lead epoch=1 intent=x",
		"Ataniq lead epoch=1 intent=x",
		"ataniq  lead epoch=1 intent=x",
		"ataniq lead epoch=1intent=x",
		"ataniq lead epoch=18446744073709551616 intent=x",
		"ataniq lead intent=x epoch=1",
		"ataniq lead epoch=1 intent=" + strings.Repeat("i", maxIntentLen+1),
		"ataniq supervisor epoch=1 intent=x",
		"some other unit",
	} {
		if e, i, ok := ParseDescription(d); ok || e != 0 || i != "" {
			t.Errorf("%q accepted as %d %q", d, e, i)
		}
	}
}
