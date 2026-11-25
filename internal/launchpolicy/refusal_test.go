package launchpolicy

import (
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"ataniq/internal/pack"
)

func TestT54RefusalLines(t *testing.T) {
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
			if got := tokenFault(code, stage, 1, f, "--model="+v).Error(); got != prefix+want {
				t.Fatalf("%q, want %q", got, prefix+want)
			}
		}
	}
	for _, name := range []string{"]", "\n", "\x7f", "\xff", strings.Repeat("x", 65)} {
		for _, kind := range []string{"env", "file", "form"} {
			if got := location(kind, name, 2); got != kind+"[#2]" {
				t.Fatal(got)
			}
		}
	}
	for _, code := range []string{"unknown-token", "alias-spelling", "refused-option"} {
		for _, value := range []string{"--resume:r731905", "r731905"} {
			want := "launch refused [pack-launch-" + code + "] stage=2 at=argv[2]"
			if got := tokenFault(code, 2, 2, nil, value).Error(); got != want {
				t.Fatal(got)
			}
		}
	}
	for _, f := range []*form{{Slot: pack.ResumeSlot}, {Slot: pack.ModelSlot}} {
		if got := tokenFault("form-repeated", 2, 4, f, "--resume:r731905").Error(); got != "launch refused [pack-launch-form-repeated] stage=2 at=argv[4]" {
			t.Fatal(got)
		}
	}
}
