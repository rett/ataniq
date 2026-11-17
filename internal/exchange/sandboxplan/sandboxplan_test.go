package sandboxplan

import (
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestCommandValidate(t *testing.T) {
	ok := Command{Argv: []string{"/bin/true", "a b", ""}, Env: []string{"PATH=/bin", "EMPTY=", "X=a=b"}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid command refused: %v", err)
	}
	if err := (Command{Argv: []string{"/bin/true"}}).Validate(); err != nil {
		t.Fatalf("empty environment refused: %v", err)
	}
	bad := map[string]Command{
		"nil argv":        {},
		"empty argv":      {Argv: []string{}},
		"relative":        {Argv: []string{"bin/true"}},
		"empty first":     {Argv: []string{""}},
		"nul in argv":     {Argv: []string{"/bin/true", "a\x00b"}},
		"nul in first":    {Argv: []string{"/bin/tr\x00ue"}},
		"env no equals":   {Argv: []string{"/bin/true"}, Env: []string{"PATH"}},
		"env empty key":   {Argv: []string{"/bin/true"}, Env: []string{"=x"}},
		"env nul":         {Argv: []string{"/bin/true"}, Env: []string{"A=b\x00"}},
		"env empty entry": {Argv: []string{"/bin/true"}, Env: []string{""}},
	}
	for name, c := range bad {
		if err := c.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestVerifyPlanCommand(t *testing.T) {
	p := VerifyPlan{
		Prefix: []string{"/usr/bin/env", "-i"},
		Env:    []string{"PATH=/usr/bin"},
		Mirror: "/m", Scratch: "/s", Home: "/h",
	}
	c, err := p.Command([]string{"/usr/bin/git", "clone", "/m", "/s"})
	if err != nil {
		t.Fatal(err)
	}
	want := Command{
		Argv: []string{"/usr/bin/env", "-i", "/usr/bin/git", "clone", "/m", "/s"},
		Env:  []string{"PATH=/usr/bin"},
	}
	if !reflect.DeepEqual(c, want) {
		t.Fatalf("got %#v, want %#v", c, want)
	}
	// The result shares no memory with the plan.
	c.Argv[0] = "/changed"
	c.Env[0] = "CHANGED=1"
	if p.Prefix[0] != "/usr/bin/env" || p.Env[0] != "PATH=/usr/bin" {
		t.Fatalf("plan changed through the command: %#v", p)
	}
	// A prefix with spare capacity is not written into.
	spare := make([]string, 2, 8)
	copy(spare, p.Prefix)
	p.Prefix = spare
	if _, err := p.Command([]string{"/a"}); err != nil {
		t.Fatal(err)
	}
	if got := spare[:3][2]; got != "" {
		t.Fatalf("prefix backing array written: %q", got)
	}

	for name, inner := range map[string][]string{
		"nil": nil, "empty": {}, "relative": {"git"}, "nul": {"/usr/bin/git", "a\x00"},
	} {
		if _, err := p.Command(inner); err == nil {
			t.Errorf("inner %s accepted", name)
		}
	}
	if _, err := (VerifyPlan{}).Command([]string{"/bin/true"}); err == nil {
		t.Error("empty prefix accepted")
	}
	if _, err := (VerifyPlan{Prefix: []string{"sandbox"}}).Command([]string{"/bin/true"}); err == nil {
		t.Error("relative prefix accepted")
	}
	if _, err := (VerifyPlan{Prefix: []string{"/sb"}, Env: []string{"BAD"}}).Command([]string{"/bin/true"}); err == nil {
		t.Error("bad env accepted")
	}
}

func TestPlanValidate(t *testing.T) {
	good := ServePlan{
		UploadPack: []string{"/sb", "/usr/bin/git-upload-pack"},
		Repository: "/work/p",
		Measure:    Command{Argv: []string{"/usr/bin/du"}},
	}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mod := range map[string]func(*ServePlan){
		"no upload pack":    func(p *ServePlan) { p.UploadPack = nil },
		"relative repo":     func(p *ServePlan) { p.Repository = "work" },
		"unclean repo":      func(p *ServePlan) { p.Repository = "/work/../p" },
		"newline repo":      func(p *ServePlan) { p.Repository = "/work/a\nb" },
		"measure not abs":   func(p *ServePlan) { p.Measure.Argv = []string{"du"} },
		"measure bad env":   func(p *ServePlan) { p.Measure.Env = []string{"X"} },
		"empty repo":        func(p *ServePlan) { p.Repository = "" },
		"upload pack nul":   func(p *ServePlan) { p.UploadPack = []string{"/sb", "a\x00"} },
		"upload not abs":    func(p *ServePlan) { p.UploadPack = []string{"sb"} },
		"repo brace":        func(p *ServePlan) { p.Repository = "/work/{x}" },
		"repo trailing dot": func(p *ServePlan) { p.Repository = "/work/./p" },
	} {
		p := good
		mod(&p)
		if err := p.Validate(); err == nil {
			t.Errorf("serve %s accepted", name)
		}
	}
	vgood := VerifyPlan{Prefix: []string{"/sb"}, Env: []string{"A=b"}, Mirror: "/m", Scratch: "/s", Home: "/h"}
	if err := vgood.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mod := range map[string]func(*VerifyPlan){
		"no prefix":  func(p *VerifyPlan) { p.Prefix = nil },
		"bad env":    func(p *VerifyPlan) { p.Env = []string{"A"} },
		"no mirror":  func(p *VerifyPlan) { p.Mirror = "" },
		"rel scr":    func(p *VerifyPlan) { p.Scratch = "s" },
		"unclean":    func(p *VerifyPlan) { p.Home = "/h/" },
		"root slash": func(p *VerifyPlan) { p.Home = "//h" },
	} {
		p := vgood
		mod(&p)
		if err := p.Validate(); err == nil {
			t.Errorf("verify %s accepted", name)
		}
	}
}

func TestShellQuote(t *testing.T) {
	golden := []struct {
		argv []string
		want string
	}{
		{[]string{"/usr/bin/git-upload-pack"}, `'/usr/bin/git-upload-pack'`},
		{[]string{"/a", "b c"}, `'/a' 'b c'`},
		{[]string{"it's"}, `'it'\''s'`},
		{[]string{"'"}, `''\'''`},
		{[]string{""}, `''`},
		{[]string{"a\nb"}, "'a\nb'"},
	}
	for _, g := range golden {
		got, err := ShellQuote(g.argv)
		if err != nil || got != g.want {
			t.Errorf("ShellQuote(%q) = %q, %v; want %q", g.argv, got, err, g.want)
		}
	}
	if _, err := ShellQuote(nil); err == nil {
		t.Error("nil argv accepted")
	}
	if _, err := ShellQuote([]string{}); err == nil {
		t.Error("empty argv accepted")
	}
	if _, err := ShellQuote([]string{"/a", "b\x00"}); err == nil {
		t.Error("NUL accepted")
	}
}

func TestShellQuoteSurvivesSh(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	words := []string{
		"/usr/bin/x", "plain", "two words", "it's", "''", "a'b'c", "line\nbreak",
		"$HOME", "`id`", "$(id)", `back\slash`, `"dq"`, "*", "?", "~", "a;b", "a&b", "a|b",
		"<x>", "#c", "!x", "tab\there", "  lead and trail  ", "", "é", "-n", "--",
	}
	q, err := ShellQuote(words)
	if err != nil {
		t.Fatal(err)
	}
	// printf prints each word followed by a NUL, so empty words and
	// newlines are told apart.
	cmd := exec.Command(sh, "-c", `printf '%s\0' `+q)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent"}
	cmd.Dir = t.TempDir()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("sh: %v", err)
	}
	got := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	if !reflect.DeepEqual(got, words) {
		t.Fatalf("words changed through sh:\n got %q\nwant %q", got, words)
	}
}

func TestParseMeasure(t *testing.T) {
	good := map[string]int64{
		"0":                            0,
		"1234\t/work/p/.git/objects\n": 1234,
		"  42  x\n":                    42,
		"7":                            7,
		"7 \r\n":                       7,
		"5\nnot a number\n":            5,
		"999999999999999999\n":         999999999999999999,
		"007\n":                        7,
		"12 34\n":                      12,
	}
	for in, want := range good {
		got, err := ParseMeasure([]byte(in))
		if err != nil || got != want {
			t.Errorf("ParseMeasure(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	bad := map[string]string{
		"empty":       "",
		"newline":     "\n",
		"blank line":  "   \n5\n",
		"garbage":     "abc\n",
		"mixed":       "12abc\n",
		"negative":    "-5\n",
		"plus":        "+5\n",
		"overflow":    "9223372036854775808\n",
		"19 digits":   "1000000000000000000\n",
		"many digits": strings.Repeat("9", 40) + "\n",
		"hex":         "0x10\n",
		"decimal":     "1.5\n",
		"comma":       "1,000\n",
		"nul":         "5\x00\n",
		"nul later":   "5\nx\x00",
		"unicode":     "٣\n",
		"error first": "du: cannot read\n5\n",
	}
	for name, in := range bad {
		if n, err := ParseMeasure([]byte(in)); err == nil {
			t.Errorf("%s: ParseMeasure(%q) = %d, want an error", name, in, n)
		}
	}
	if _, err := ParseMeasure(nil); err == nil {
		t.Error("nil accepted")
	}
	// Eighteen zeros then a digit is nineteen digits: refused by the length rule.
	if _, err := ParseMeasure([]byte(strings.Repeat("0", 18) + "1\n")); err == nil {
		t.Error("19 digits with leading zeros accepted")
	}
}

func TestValidName(t *testing.T) {
	for _, s := range []string{"a", "0", "a-b", "9z", "lead", "p1-x-2", strings.Repeat("a", 63)} {
		if !ValidName(s) {
			t.Errorf("%q refused", s)
		}
	}
	for _, s := range []string{
		"", "-a", "A", "aB", "a_b", "a.b", "a b", "a/b", "../x", "a\n", "a\x00", "é",
		strings.Repeat("a", 64), "a{b}", "a:b", "a\nb",
	} {
		if ValidName(s) {
			t.Errorf("%q accepted", s)
		}
	}
}
