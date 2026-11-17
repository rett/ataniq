package execconfig

import "fmt"

// Rule is one entry of the set. Pattern follows the syntax in the package
// comment. Reason is a short text the owner reads. Source is one of the
// Source constants.
type Rule struct{ Pattern, Reason, Source string }

// Match is the rule that matched a path.
type Match struct{ Pattern, Reason string }

// Set is an ordered list of rules. The zero Set has no rule and matches
// nothing. A Set is read-only: With returns a new one.
type Set struct {
	rules []Rule
	comp  []compiled
}

// Default returns the built-in set: the design rules, the feature scope
// rules and the relatives, in that order.
func Default() Set {
	var s Set
	for _, list := range [][]Rule{Design(), Scope(), Relatives()} {
		for _, r := range list {
			c, err := compile(r.Pattern)
			if err != nil {
				panic(err) // a built-in rule is a bug; a test covers all of them
			}
			s.rules = append(s.rules, r)
			s.comp = append(s.comp, c)
		}
	}
	return s
}

// Rules returns a copy of the rules in matching order.
func (s Set) Rules() []Rule {
	return append([]Rule(nil), s.rules...)
}

// With returns a new Set with the patterns added after the existing rules,
// each with the reason "project addition". It refuses an empty pattern, one
// with a NUL, a line break, a backslash or a bracket, one with a "." or ".."
// element or an empty element, one over 255 bytes, and a result with more
// than 256 additions. It changes nothing when it fails.
func (s Set) With(patterns []string) (Set, error) {
	have := 0
	for _, r := range s.rules {
		if r.Source == SourceProject {
			have++
		}
	}
	if have+len(patterns) > MaxAdditions {
		return Set{}, fmt.Errorf("%w: %d additions, the limit is %d", ErrPattern, have+len(patterns), MaxAdditions)
	}
	out := Set{
		rules: make([]Rule, len(s.rules), len(s.rules)+len(patterns)),
		comp:  make([]compiled, len(s.comp), len(s.comp)+len(patterns)),
	}
	copy(out.rules, s.rules)
	copy(out.comp, s.comp)
	for i, p := range patterns {
		c, err := compile(p)
		if err != nil {
			return Set{}, fmt.Errorf("addition %d: %w", i+1, err)
		}
		out.rules = append(out.rules, Rule{Pattern: p, Reason: ProjectReason, Source: SourceProject})
		out.comp = append(out.comp, c)
	}
	return out, nil
}

// Match returns the first rule, in the order of Rules, that matches path.
// See the package comment for how a path is read.
func (s Set) Match(path string) (Match, bool) {
	t := prepare(path)
	if len(t.cands) == 0 {
		return Match{}, false
	}
	for i, c := range s.comp {
		if c.matches(t) {
			return Match{Pattern: s.rules[i].Pattern, Reason: s.rules[i].Reason}, true
		}
	}
	return Match{}, false
}
