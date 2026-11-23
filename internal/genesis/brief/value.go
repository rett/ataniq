package brief

import (
	"sort"
	"strconv"
)

// ValueKind is the form of a [Value].
type ValueKind int

// The forms of a value. The zero ValueKind is no value and is refused.
const (
	// KindOne is one choice: an option id, "yes" or "no", or a scale step
	// "1" to "5". In JSON it is a string.
	KindOne ValueKind = iota + 1
	// KindMany is several choices, sorted and distinct. In JSON it is a
	// list of strings.
	KindMany
	// KindOther is free text given instead of a choice. In JSON it is
	// {"other": "..."}.
	KindOther
)

// MaxChoices is the most choices one value holds.
const MaxChoices = 8

// MaxChoiceLen is the longest choice id, in bytes.
const MaxChoiceLen = 32

// Value is the value of one field. Build it with [One], [Many] or
// [OtherText]; the zero Value is no value.
type Value struct {
	kind  ValueKind
	one   string
	many  []string
	other Untrusted
}

// One returns a single choice.
func One(id string) Value { return Value{kind: KindOne, one: id} }

// Many returns several choices. It sorts a copy of ids; a repeated id is
// refused when the value is set.
func Many(ids ...string) Value {
	c := append([]string(nil), ids...)
	sort.Strings(c)
	return Value{kind: KindMany, many: c}
}

// OtherText returns free text given instead of a choice.
func OtherText(s Untrusted) Value { return Value{kind: KindOther, other: s} }

// Kind returns the form of the value.
func (v Value) Kind() ValueKind { return v.kind }

// One returns the choice of a single choice, and "" for another form.
func (v Value) One() string { return v.one }

// Many returns a copy of the choices of a multiple choice, and nil for
// another form.
func (v Value) Many() []string { return append([]string(nil), v.many...) }

// Other returns the free text of an "other" value, and false for another
// form.
func (v Value) Other() (Untrusted, bool) { return v.other, v.kind == KindOther }

// Has reports whether the value is the choice id, or holds it among its
// choices. Free text holds no choice.
func (v Value) Has(id string) bool {
	switch v.kind {
	case KindOne:
		return v.one == id
	case KindMany:
		for _, c := range v.many {
			if c == id {
				return true
			}
		}
	}
	return false
}

// Equal reports whether v and w are the same value.
func (v Value) Equal(w Value) bool {
	if v.kind != w.kind || v.one != w.one || v.other != w.other || len(v.many) != len(w.many) {
		return false
	}
	for i := range v.many {
		if v.many[i] != w.many[i] {
			return false
		}
	}
	return true
}

func (v Value) clone() Value {
	v.many = append([]string(nil), v.many...)
	if v.kind != KindMany {
		v.many = nil
	}
	return v
}

// ValidChoice reports whether id is a choice id: 1 to 32 bytes of
// lower-case letters, digits, "_" and "-", starting with a letter or a
// digit.
func ValidChoice(id string) bool {
	if len(id) == 0 || len(id) > MaxChoiceLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case (c == '_' || c == '-') && i > 0:
		default:
			return false
		}
	}
	return true
}

// check checks the value's form and content.
func (v Value) check(where string) error {
	switch v.kind {
	case KindOne:
		if !ValidChoice(v.one) {
			return fail(CodeValueInvalid, where+" is not a choice id")
		}
		if SecretShaped(v.one) {
			return fail(CodeSecretShaped, where+" looks like a key, a token or a password")
		}
	case KindMany:
		if len(v.many) == 0 {
			return fail(CodeValueInvalid, where+" holds no choice")
		}
		if len(v.many) > MaxChoices {
			return fail(CodeTooLarge, where+" holds more than 8 choices")
		}
		for i, c := range v.many {
			at := where + "[" + strconv.Itoa(i) + "]"
			if !ValidChoice(c) {
				return fail(CodeValueInvalid, at+" is not a choice id")
			}
			if i > 0 && v.many[i-1] >= c {
				return fail(CodeValueInvalid, where+" is not sorted, or repeats a choice")
			}
			if SecretShaped(c) {
				return fail(CodeSecretShaped, at+" looks like a key, a token or a password")
			}
		}
	case KindOther:
		return checkFree(string(v.other), where+".other")
	default:
		return fail(CodeValueInvalid, where+" has no value")
	}
	return nil
}
