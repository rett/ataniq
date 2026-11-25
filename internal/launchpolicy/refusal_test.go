package launchpolicy

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"ataniq/internal/pack"
)

func TestResumeRedactionAcrossDiagnostics(t *testing.T) {
	x := baseline(t, "BC")
	x.s.Resume = rid
	spec := x.expand(t)
	t.Run("classified value", func(t *testing.T) {
		for _, value := range []string{rid, "prefix-" + rid + "-suffix"} {
			got := x.expand(t)
			got.Argv[2] = "--agent=" + value
			line(t, x.recheck(t, got), "value-unadmitted", "2", "argv[2]")
		}
	})
	t.Run("template literal", func(t *testing.T) {
		x := baseline(t, "BC")
		x.s.Resume = rid
		x.l.Argv[4] = "--agent=" + rid
		_, r := Expand(x.p, x.l, x.s)
		line(t, r, "literal-in-slot", "1", "argv[4]")
	})
	t.Run("environment", func(t *testing.T) {
		got := x.expand(t)
		got.Env["z-"+rid] = "1"
		line(t, x.recheck(t, got), "env-refused", "2", "env[#1]")
		for i := range 31 {
			got.Env[fmt.Sprintf("K%02d", i)] = "1"
		}
		line(t, x.recheck(t, got), "bound", "2", "env[#32]")
	})
	t.Run("environment difference", func(t *testing.T) {
		x := x
		x.policy(t, func(d obj) {
			d["env"] = append(d["env"].([]any), obj{"name": rid, "values": []string{"1"}})
		})
		got := x.expand(t)
		got.Env[rid] = "1"
		line(t, x.judge(got), "template-expansion-differs", "2", "env[#1]")
	})
	t.Run("file", func(t *testing.T) {
		f := cleanFiles()
		f.inv[1].Dest = "z-" + rid
		line(t, JudgeFiles(x.p, x.s, f.inv, f.files[:1]), "file-refused", "2", "file[#1]")
	})
	t.Run("required form", func(t *testing.T) {
		x := x
		x.policy(t, func(d obj) {
			f := formRow(d, "agent")
			dropForm(d, "agent")
			f["id"] = rid
			addForm(d, f)
		})
		got := spec
		got.Argv = spec.Argv[:2]
		line(t, x.recheck(t, got), "required-missing", "2", "form[#5]")
	})
}

func TestT54RefusalLines(t *testing.T) {
	var s Snapshot
	f := &form{Prefix: "--model=", Slot: pack.ModelSlot}
	for _, code := range []string{"literal-in-slot", "slot-mismatch", "form-position", "form-repeated", "value-outside-ceiling", "value-grammar", "value-unadmitted", "mode-class-refused", "mode-vector-pending"} {
		stage := 2
		if code == "literal-in-slot" || code == "slot-mismatch" {
			stage = 1
		}
		prefix := "launch refused [pack-launch-" + code + "] stage=" + strconv.Itoa(stage) + " at=argv[1] value="
		for _, v := range []string{"x\xff\n\x7f]", "", strings.Repeat("\x00", 40), strings.Repeat("界", 100)} {
			want := strconv.Quote(v)
			if len(prefix)+len(want) > 200 {
				// Independent oracle: shorten the original value until the complete
				// standard-library quotation and its marker fit.
				short := v
				for len(prefix)+len(strconv.Quote(short))+1 > 200 {
					_, n := utf8.DecodeLastRuneInString(short)
					short = short[:len(short)-n]
				}
				want = strconv.Quote(short) + "+"
			}
			if got := s.tokenFault(code, stage, 1, f, "--model="+v).Error(); got != prefix+want {
				t.Fatalf("%q, want %q", got, prefix+want)
			}
		}
	}
	for _, name := range []string{"]", "\n", "\x7f", "\xff", strings.Repeat("x", 65)} {
		for _, kind := range []string{"env", "file", "form"} {
			if got := s.location(kind, name, 2); got != kind+"[#2]" {
				t.Fatal(got)
			}
		}
	}
	for _, code := range []string{"unknown-token", "alias-spelling", "refused-option"} {
		for _, value := range []string{"--resume:r731905", "r731905"} {
			want := "launch refused [pack-launch-" + code + "] stage=2 at=argv[2]"
			if got := s.tokenFault(code, 2, 2, nil, value).Error(); got != want {
				t.Fatal(got)
			}
		}
	}
	for _, f := range []*form{{Slot: pack.ResumeSlot}, {Slot: pack.ModelSlot}} {
		if got := s.tokenFault("form-repeated", 2, 4, f, "--resume:r731905").Error(); got != "launch refused [pack-launch-form-repeated] stage=2 at=argv[4]" {
			t.Fatal(got)
		}
	}
}

func TestT54MatcherRedaction(t *testing.T) {
	x := baseline(t, "BL")
	for _, token := range []string{rid, "--resume:" + rid, "-m=" + rid, "--approve-all=" + rid} {
		spec := x.expand(t)
		spec.Argv = append(spec.Argv, token)
		code := "unknown-token"
		switch token {
		case "-m=" + rid:
			code = "alias-spelling"
		case "--approve-all=" + rid:
			code = "refused-option"
		}
		line(t, x.recheck(t, spec), code, "2", "argv[2]")
	}
	spec := x.expand(t)
	spec.Env = map[string]string{"]bad": rid}
	line(t, x.recheck(t, spec), "env-refused", "2", "env[#0]")
	// An invalid byte cannot survive a JSON round trip. Check the daemon's
	// in-memory input directly, where it still requires escaped output.
	spec = x.expand(t)
	spec.Argv = append(spec.Argv, "--model=\xff\n\x7f]")
	line(t, x.judge(spec), "value-outside-ceiling", "2", "argv[2]", strconv.Quote("\xff\n\x7f]"))
}
