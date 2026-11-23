package catalog

import (
	"strconv"

	"ataniq/internal/genesis/brief"
	"ataniq/internal/genesis/internal/canon"
)

// Op is the operation of a [Predicate]. The set is closed: there is no
// expression language.
type Op string

// The operations of a predicate.
const (
	// OpEq holds when the field is set and its value is the choice, or
	// holds it among several choices. The choice "other" matches free
	// text given instead of a choice.
	OpEq Op = "eq"
	// OpIn holds when OpEq holds for one of the values.
	OpIn Op = "in"
	// OpAnd holds when every argument holds: 2 to 4 arguments.
	OpAnd Op = "and"
	// OpNot holds when its one argument does not.
	OpNot Op = "not"
	// OpSet holds when the field is set, whatever its value.
	OpSet Op = "set"
	// OpUnset holds when the field is not set.
	OpUnset Op = "unset"
)

// The bounds of a predicate.
const (
	// MaxPredicateDepth is how deep predicates nest; a predicate without
	// arguments has depth 1.
	MaxPredicateDepth = 4
	// MaxPredicateSize is the most predicates one condition holds, its
	// arguments included.
	MaxPredicateSize = 16
	// MaxInValues is the most values of an OpIn.
	MaxInValues = 8
)

// OtherChoice is the value a predicate uses to match free text given
// instead of a choice.
const OtherChoice = "other"

// Predicate is the condition of a node: when it holds for a brief, the
// node may be asked. Only the parts its Op names are used: Field and Value
// for OpEq, Field and Values for OpIn, Field for OpSet and OpUnset, Args
// for OpAnd and OpNot.
type Predicate struct {
	Op     Op
	Field  string
	Value  string
	Values []string
	Args   []Predicate
}

// Eval reports whether p holds for the brief.
func (p Predicate) Eval(b brief.Brief) bool {
	switch p.Op {
	case OpEq:
		f, ok := b.Get(p.Field)
		return ok && matches(f.Value, p.Value)
	case OpIn:
		f, ok := b.Get(p.Field)
		if !ok {
			return false
		}
		for _, v := range p.Values {
			if matches(f.Value, v) {
				return true
			}
		}
		return false
	case OpSet:
		_, ok := b.Get(p.Field)
		return ok
	case OpUnset:
		_, ok := b.Get(p.Field)
		return !ok
	case OpAnd:
		for _, a := range p.Args {
			if !a.Eval(b) {
				return false
			}
		}
		return len(p.Args) > 0
	case OpNot:
		return len(p.Args) == 1 && !p.Args[0].Eval(b)
	}
	return false
}

func matches(v brief.Value, choice string) bool {
	if _, other := v.Other(); other {
		return choice == OtherChoice
	}
	return v.Has(choice)
}

func (p Predicate) clone() Predicate {
	if p.Values != nil {
		p.Values = append([]string{}, p.Values...)
	}
	if p.Args != nil {
		args := make([]Predicate, len(p.Args))
		for i, a := range p.Args {
			args[i] = a.clone()
		}
		p.Args = args
	}
	return p
}

// fields appends every field the predicate refers to.
func (p Predicate) fields(out []string) []string {
	switch p.Op {
	case OpAnd, OpNot:
		for _, a := range p.Args {
			out = a.fields(out)
		}
	default:
		out = append(out, p.Field)
	}
	return out
}

// shape checks the form of p, its depth and its size, and returns its size.
func (p Predicate) shape(where string, depth int) (int, error) {
	if depth > MaxPredicateDepth {
		return 0, invalid(where + " nests deeper than 4")
	}
	switch p.Op {
	case OpEq, OpIn, OpSet, OpUnset:
		if !brief.ValidFieldName(p.Field) {
			return 0, invalid(where + ".field is not a field of the brief")
		}
		if len(p.Args) != 0 {
			return 0, invalid(where + " has arguments its op does not take")
		}
		switch p.Op {
		case OpEq:
			if p.Value == "" || p.Values != nil {
				return 0, invalid(where + " is an eq without exactly one value")
			}
		case OpIn:
			if p.Value != "" || len(p.Values) == 0 || len(p.Values) > MaxInValues {
				return 0, invalid(where + " is an in without 1 to 8 values")
			}
			for i, v := range p.Values {
				for _, w := range p.Values[:i] {
					if v == w {
						return 0, invalid(where + ".values repeats a value")
					}
				}
			}
		default:
			if p.Value != "" || p.Values != nil {
				return 0, invalid(where + " has a value its op does not take")
			}
		}
		return 1, nil
	case OpAnd, OpNot:
		if p.Field != "" || p.Value != "" || p.Values != nil {
			return 0, invalid(where + " has a field or a value its op does not take")
		}
		if p.Op == OpAnd && (len(p.Args) < 2 || len(p.Args) > 4) {
			return 0, invalid(where + " is an and without 2 to 4 arguments")
		}
		if p.Op == OpNot && len(p.Args) != 1 {
			return 0, invalid(where + " is a not without exactly one argument")
		}
		size := 1
		for i, a := range p.Args {
			n, err := a.shape(where+".args["+strconv.Itoa(i)+"]", depth+1)
			if err != nil {
				return 0, err
			}
			size += n
		}
		if size > MaxPredicateSize {
			return 0, invalid(where + " holds more than 16 predicates")
		}
		return size, nil
	}
	return 0, invalid(where + ".op is not one of the operations")
}

// values calls fn with the field and each value p compares it with.
func (p Predicate) values(fn func(field, value string) bool) bool {
	switch p.Op {
	case OpEq:
		return fn(p.Field, p.Value)
	case OpIn:
		for _, v := range p.Values {
			if !fn(p.Field, v) {
				return false
			}
		}
	case OpAnd, OpNot:
		for _, a := range p.Args {
			if !a.values(fn) {
				return false
			}
		}
	}
	return true
}

func (p Predicate) encode() canon.Value {
	ms := []canon.Member{canon.M("op", canon.Str(string(p.Op)))}
	switch p.Op {
	case OpAnd, OpNot:
		args := make([]canon.Value, len(p.Args))
		for i, a := range p.Args {
			args[i] = a.encode()
		}
		ms = append(ms, canon.M("args", canon.List(args...)))
	case OpEq:
		ms = append(ms, canon.M("field", canon.Str(p.Field)), canon.M("value", canon.Str(p.Value)))
	case OpIn:
		ms = append(ms, canon.M("field", canon.Str(p.Field)), canon.M("values", canon.Strs(p.Values)))
	default:
		ms = append(ms, canon.M("field", canon.Str(p.Field)))
	}
	return canon.Obj(ms...)
}

func decodePredicate(v canon.Value, where string, depth int) (Predicate, error) {
	if depth > MaxPredicateDepth {
		return Predicate{}, invalid(where + " nests deeper than 4")
	}
	if v.Kind != canon.Object {
		return Predicate{}, invalid(where + " is not an object")
	}
	var op string
	for _, m := range v.Obj {
		if m.Key == "op" {
			op = m.Val.Str
		}
	}
	var required []string
	switch Op(op) {
	case OpEq:
		required = []string{"op", "field", "value"}
	case OpIn:
		required = []string{"op", "field", "values"}
	case OpSet, OpUnset:
		required = []string{"op", "field"}
	case OpAnd, OpNot:
		required = []string{"op", "args"}
	default:
		return Predicate{}, invalid(where + ".op is not one of the operations")
	}
	m, err := canon.Fields(v, where, required, nil)
	if err != nil {
		return Predicate{}, invalid(err.Error())
	}
	p := Predicate{Op: Op(op)}
	if fv, ok := m["field"]; ok {
		if p.Field, err = canon.AsString(fv, where+".field"); err != nil {
			return Predicate{}, invalid(err.Error())
		}
	}
	if vv, ok := m["value"]; ok {
		if p.Value, err = canon.AsString(vv, where+".value"); err != nil {
			return Predicate{}, invalid(err.Error())
		}
	}
	if vv, ok := m["values"]; ok {
		if p.Values, err = canon.AsStrings(vv, where+".values", MaxInValues); err != nil {
			return Predicate{}, invalid(err.Error())
		}
	}
	if av, ok := m["args"]; ok {
		if av.Kind != canon.Array {
			return Predicate{}, invalid(where + ".args is not a list")
		}
		if len(av.Arr) > 4 {
			return Predicate{}, invalid(where + " has more than 4 arguments")
		}
		p.Args = make([]Predicate, len(av.Arr))
		for i, a := range av.Arr {
			if p.Args[i], err = decodePredicate(a, where+".args["+strconv.Itoa(i)+"]", depth+1); err != nil {
				return Predicate{}, err
			}
		}
	}
	return p, nil
}
