package launchpolicy

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"ataniq/internal/pack"
)

// Refusal identifies the first failed check. At contains a printable location.
// Sensitive input is discarded before the diagnostic is retained.
type Refusal struct {
	Code   string
	Stage  int
	At     string
	detail string
}

func (r *Refusal) Error() string {
	return fmt.Sprintf("launch refused [pack-launch-%s] stage=%d at=%s", r.Code, r.Stage, r.At) + r.detail
}
func refuse(code string, stage int, at string) *Refusal {
	return &Refusal{Code: code, Stage: stage, At: at}
}

var safeComponent = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)

func location(kind, name string, ordinal int) string {
	if !safeComponent.MatchString(name) {
		name = "#" + strconv.Itoa(ordinal)
	}
	return kind + "[" + name + "]"
}
func argvAt(index int) string { return "argv[" + strconv.Itoa(index) + "]" }
func tokenFault(code string, stage, index int, f *form, token string) *Refusal {
	r := refuse(code, stage, argvAt(index))
	if f == nil || f.Slot == pack.ResumeSlot || strings.HasPrefix(token, "--resume") {
		return r
	}
	switch code {
	case "literal-in-slot", "slot-mismatch", "form-position", "form-repeated", "value-outside-ceiling", "value-grammar", "value-unadmitted", "mode-class-refused", "mode-vector-pending":
		const label = " value="
		value := strings.TrimPrefix(token, string(f.Prefix))
		r.detail = label + quoteWithin(value, 200-len(r.Error())-len(label))
	}
	return r
}

func quoteWithin(value string, budget int) string {
	// Quote one complete rune (or one invalid byte) at a time. Reserve the
	// closing quote, and reserve the truncation marker only if more input follows.
	b := []byte{'"'}
	for len(value) > 0 {
		_, size := utf8.DecodeRuneInString(value)
		q := strconv.Quote(value[:size])
		piece := q[1 : len(q)-1]
		reserve := 1
		if size < len(value) {
			reserve++
		}
		if len(b)+len(piece)+reserve > budget {
			return string(append(b, '"', '+'))
		}
		b = append(b, piece...)
		value = value[size:]
	}
	return string(append(b, '"'))
}
