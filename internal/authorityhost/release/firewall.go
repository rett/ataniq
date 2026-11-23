package release

import "strings"

// The hooks a default-deny firewall drops by default, in the order they are
// reported.
var defaultDenyHooks = []string{"input", "output", "forward"}

// DefaultDenyWarning is the words baseline prints, and writes into the
// header of nft.digest, for a hook the enrolled ruleset does not deny by
// default.
func DefaultDenyWarning(hook string) string {
	return "WARNING: no-default-deny " + hook + ": the enrolled nft ruleset has no base chain of hook " +
		hook + " with policy drop for IPv4 and IPv6; nft.digest pins it as it is"
}

// missingDefaultDeny reads an "nft list ruleset" listing and returns, in the
// order of defaultDenyHooks, each hook that has no default deny: no base
// chain of type filter of that hook with "policy drop" in a table that is
// not dormant (a "flags" line naming dormant among its comma-separated
// flags), of family inet, or of both ip and ip6, so that IPv4 and IPv6 are
// both dropped by default. A base chain is a chain whose "type filter hook
// H ...; policy drop;" line stands directly in it. A chain of another type
// does not count: a nat chain sees only the first packet of a connection,
// and a route chain, which filters the output hook like a filter chain, is
// not counted either, so a ruleset that denies by one is reported (a
// warning too many rather than one too few). A listing it cannot read in that
// shape, or one that ends inside a block (a cut listing), has no default
// deny anywhere: the result never claims a policy it did not see.
//
// Of the rules it reads only the first rule of such a chain: a chain whose
// first rule accepts every packet (only "accept", with a counter or a
// comment; see acceptsAll) does not count. No other rule is read, so a
// chain whose rules accept everything in another way (a later accept of
// every packet, a jump to a chain that accepts it, an accept behind a
// match that every packet meets) still counts as a default deny: the
// result says that each hook has a base chain whose policy is drop, not
// that the rules drop anything.
func missingDefaultDeny(listing []byte) []string {
	type chain struct {
		table int
		hook  string // the hook of its "type filter ... policy drop" line, while it counts
		ruled bool   // its first rule after that line was read
	}
	type block struct {
		kind  string // "table", "chain" or "" for any other block
		table int    // index of its table in tables, or -1
		chain *chain // the chain of a "chain" block
	}
	type table struct {
		family  string
		dormant bool
		drops   map[string]bool // hook -> a base chain with policy drop
	}
	var tables []*table
	var chains []*chain
	var stack []block
	for _, line := range strings.Split(string(listing), "\n") {
		words, opens, closes := nftWords(line)
		top := block{table: -1}
		if len(stack) > 0 {
			top = stack[len(stack)-1]
		}
		switch {
		case len(words) > 0 && words[0] == "table" && opens == 1 && closes == 0 && len(stack) == 0:
			family := "ip" // nft's family when a table names none
			if len(words) >= 3 {
				family = words[1]
			}
			tables = append(tables, &table{family: family, drops: map[string]bool{}})
			stack = append(stack, block{kind: "table", table: len(tables) - 1})
			continue
		case len(words) > 0 && words[0] == "chain" && opens == 1 && closes == 0 && top.kind == "table":
			c := &chain{table: top.table}
			chains = append(chains, c)
			stack = append(stack, block{kind: "chain", table: top.table, chain: c})
			continue
		case top.kind == "table" && opens == 0 && closes == 0 && len(words) >= 2 && words[0] == "flags":
			for _, w := range words[1:] {
				for _, flag := range strings.Split(w, ",") {
					if flag == "dormant" {
						tables[top.table].dormant = true
					}
				}
			}
		case top.kind == "chain" && opens == 0 && closes == 0 && len(words) > 1 && words[0] == "type" && words[1] == "filter":
			hook, policy := "", ""
			for i := 0; i+1 < len(words); i++ {
				switch words[i] {
				case "hook":
					hook = words[i+1]
				case "policy":
					policy = words[i+1]
				}
			}
			if hook != "" && policy == "drop" && !top.chain.ruled {
				top.chain.hook = hook
			}
		case top.kind == "chain" && top.chain.hook != "" && !top.chain.ruled && len(words) > 0 && words[0] != "comment":
			top.chain.ruled = true
			if opens == 0 && closes == 0 && acceptsAll(words) {
				top.chain.hook = ""
			}
		}
		for ; opens > 0; opens-- {
			stack = append(stack, block{table: -1})
		}
		for ; closes > 0 && len(stack) > 0; closes-- {
			stack = stack[:len(stack)-1]
		}
	}
	if len(stack) > 0 {
		// The listing ends inside a block: it was cut, and a chain it shows
		// may have had a first rule that accepts everything. Nothing counts.
		return append([]string(nil), defaultDenyHooks...)
	}
	for _, c := range chains {
		if c.hook != "" {
			tables[c.table].drops[c.hook] = true
		}
	}
	var missing []string
	for _, hook := range defaultDenyHooks {
		v4, v6 := false, false
		for _, t := range tables {
			if t.dormant || !t.drops[hook] {
				continue
			}
			switch t.family {
			case "inet":
				v4, v6 = true, true
			case "ip":
				v4 = true
			case "ip6":
				v6 = true
			}
		}
		if !v4 || !v6 {
			missing = append(missing, hook)
		}
	}
	return missing
}

// acceptsAll reports whether the words of a rule are a verdict accept with
// nothing that matches: "accept", with at most a counter ("counter" or
// "counter packets N bytes N" or a named counter, "counter name "x"") and a
// quoted comment around it.
func acceptsAll(words []string) bool {
	accept := false
	for i := 0; i < len(words); i++ {
		switch words[i] {
		case "accept":
			if accept {
				return false
			}
			accept = true
		case "counter":
			if i+4 < len(words) && words[i+1] == "packets" && words[i+3] == "bytes" {
				i += 4
			} else if i+2 < len(words) && words[i+1] == "name" && words[i+2] == "\"" {
				// A named counter: nftWords reads its quoted name as one
				// word, the closing quote.
				i += 2
			}
		case "comment":
			if i+1 >= len(words) || words[i+1] != "\"" {
				return false
			}
			i++
		default:
			return false
		}
	}
	return accept
}

// nftWords splits a listing line into its words outside quoted text, with
// ";" a word of its own dropped, and counts its braces outside quotes. A
// quoted string ("a comment") is one word, never read for its content, and
// an unquoted # ends the line: what follows it is a comment, neither words
// nor braces.
func nftWords(line string) (words []string, opens, closes int) {
	var cur strings.Builder
	quoted := false
	flush := func() {
		if cur.Len() > 0 {
			words = append(words, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quoted:
			if c == '\\' && i+1 < len(line) {
				i++
			} else if c == '"' {
				quoted = false
				words = append(words, "\"")
			}
		case c == '#':
			// An unquoted # starts a comment, to the end of the line.
			flush()
			return words, opens, closes
		case c == '"':
			flush()
			quoted = true
		case c == '{':
			flush()
			opens++
		case c == '}':
			flush()
			closes++
		case c == ' ' || c == '\t' || c == ';' || c == '\r':
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return words, opens, closes
}
