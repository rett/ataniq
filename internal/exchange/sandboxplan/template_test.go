package sandboxplan

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

const fullTemplate = `{
  "version": 1,
  "serve": {
    "upload_pack": ["/usr/bin/sandbox", "--ro", "{tree}", "--role={role}", "--", "/usr/bin/git-upload-pack"],
    "repository": "/work/{project}",
    "measure": ["/usr/bin/sandbox", "--ro", "{tree}", "/usr/bin/du", "-s", "-B1", "/work/{project}/.git/objects"],
    "measure_env": ["PATH=/usr/bin:/bin", "ROLE={role}"]
  },
  "verify": {
    "prefix": ["/usr/bin/sandbox", "--bind", "{mirror}", "/ataniq/mirror/{project}.git", "--bind", "{scratch}", "--bind", "{home}", "--"],
    "env": ["PATH=/usr/bin:/bin", "HOME=/home/verify"],
    "mirror": "/ataniq/mirror/{project}.git",
    "scratch": "/work/verify",
    "home": "/home/verify"
  }
}`

func mustParse(t *testing.T, s string) *Template {
	t.Helper()
	tm, err := ParseTemplate([]byte(s))
	if err != nil {
		t.Fatalf("ParseTemplate: %v", err)
	}
	return tm
}

func TestTemplateGolden(t *testing.T) {
	tm := mustParse(t, fullTemplate)

	sp, err := tm.Serve(ServeRequest{Role: "lead-1", Project: "ataniq", Tree: "/srv/leads/lead-1/tree"})
	if err != nil {
		t.Fatal(err)
	}
	wantServe := ServePlan{
		UploadPack: []string{"/usr/bin/sandbox", "--ro", "/srv/leads/lead-1/tree", "--role=lead-1", "--", "/usr/bin/git-upload-pack"},
		Repository: "/work/ataniq",
		Measure: Command{
			Argv: []string{"/usr/bin/sandbox", "--ro", "/srv/leads/lead-1/tree", "/usr/bin/du", "-s", "-B1", "/work/ataniq/.git/objects"},
			Env:  []string{"PATH=/usr/bin:/bin", "ROLE=lead-1"},
		},
	}
	if !reflect.DeepEqual(sp, wantServe) {
		t.Errorf("serve:\n got %#v\nwant %#v", sp, wantServe)
	}

	vp, err := tm.Verify(VerifyRequest{Project: "ataniq", Mirror: "/var/mirror/ataniq.git", Scratch: "/var/tmp/s1", Home: "/var/tmp/h1"})
	if err != nil {
		t.Fatal(err)
	}
	wantVerify := VerifyPlan{
		Prefix: []string{"/usr/bin/sandbox", "--bind", "/var/mirror/ataniq.git", "/ataniq/mirror/ataniq.git", "--bind", "/var/tmp/s1", "--bind", "/var/tmp/h1", "--"},
		Env:    []string{"PATH=/usr/bin:/bin", "HOME=/home/verify"},
		Mirror: "/ataniq/mirror/ataniq.git", Scratch: "/work/verify", Home: "/home/verify",
	}
	if !reflect.DeepEqual(vp, wantVerify) {
		t.Errorf("verify:\n got %#v\nwant %#v", vp, wantVerify)
	}
	cmd, err := vp.Command([]string{"/usr/bin/git", "clone", vp.Mirror, vp.Scratch})
	if err != nil {
		t.Fatal(err)
	}
	if got := cmd.Argv[len(cmd.Argv)-4:]; !reflect.DeepEqual(got, []string{"/usr/bin/git", "clone", "/ataniq/mirror/ataniq.git", "/work/verify"}) {
		t.Errorf("inner command not appended: %q", got)
	}
}

func TestTemplatePlansAreIndependent(t *testing.T) {
	tm := mustParse(t, fullTemplate)
	req := ServeRequest{Role: "a", Project: "p", Tree: "/t"}
	p1, err := tm.Serve(req)
	if err != nil {
		t.Fatal(err)
	}
	p1.UploadPack[0] = "/changed"
	p1.Measure.Env[0] = "X=1"
	p2, _ := tm.Serve(req)
	if p2.UploadPack[0] != "/usr/bin/sandbox" || p2.Measure.Env[0] != "PATH=/usr/bin:/bin" {
		t.Fatalf("second plan changed: %#v", p2)
	}
}

func TestTemplateAbsentSections(t *testing.T) {
	onlyServe := mustParse(t, `{"version":1,"serve":{"upload_pack":["/sb"],"repository":"/r","measure":["/du"]}}`)
	if _, err := onlyServe.Serve(ServeRequest{Role: "a", Project: "p", Tree: "/t"}); err != nil {
		t.Fatal(err)
	}
	_, err := onlyServe.Verify(VerifyRequest{Project: "p", Mirror: "/m", Scratch: "/s", Home: "/h"})
	if !errors.Is(err, ErrNotConfigured) || !strings.Contains(err.Error(), "no sandbox configured for verify") {
		t.Fatalf("verify without a section: %v", err)
	}

	onlyVerify := mustParse(t, `{"version":1,"verify":{"prefix":["/sb"],"mirror":"/m","scratch":"/s","home":"/h"}}`)
	if _, err := onlyVerify.Verify(VerifyRequest{Project: "p", Mirror: "/m", Scratch: "/s", Home: "/h"}); err != nil {
		t.Fatal(err)
	}
	_, err = onlyVerify.Serve(ServeRequest{Role: "a", Project: "p", Tree: "/t"})
	if !errors.Is(err, ErrNotConfigured) || !strings.Contains(err.Error(), "no sandbox configured for serve") {
		t.Fatalf("serve without a section: %v", err)
	}

	empty := mustParse(t, `{"version":1}`)
	if _, err := empty.Serve(ServeRequest{Role: "a", Project: "p", Tree: "/t"}); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("empty template serve: %v", err)
	}
	if _, err := empty.Verify(VerifyRequest{Project: "p", Mirror: "/m", Scratch: "/s", Home: "/h"}); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("empty template verify: %v", err)
	}
	null := mustParse(t, `{"version":1,"serve":null,"verify":null}`)
	if _, err := null.Serve(ServeRequest{}); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("null section: %v", err)
	}

	var nilTemplate *Template
	if _, err := nilTemplate.Serve(ServeRequest{}); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("nil template serve: %v", err)
	}
	if _, err := nilTemplate.Verify(VerifyRequest{}); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("nil template verify: %v", err)
	}
}

func TestParseTemplateRefusals(t *testing.T) {
	serve := func(field, value string) string {
		f := map[string]string{
			"upload_pack": `["/sb","{tree}"]`,
			"repository":  `"/r"`,
			"measure":     `["/du"]`,
			"measure_env": `["A=b"]`,
		}
		f[field] = value
		return `{"version":1,"serve":{"upload_pack":` + f["upload_pack"] + `,"repository":` + f["repository"] +
			`,"measure":` + f["measure"] + `,"measure_env":` + f["measure_env"] + `}}`
	}
	verify := func(field, value string) string {
		f := map[string]string{
			"prefix": `["/sb"]`, "env": `["A=b"]`, "mirror": `"/m"`, "scratch": `"/s"`, "home": `"/h"`,
		}
		f[field] = value
		return `{"version":1,"verify":{"prefix":` + f["prefix"] + `,"env":` + f["env"] + `,"mirror":` + f["mirror"] +
			`,"scratch":` + f["scratch"] + `,"home":` + f["home"] + `}}`
	}
	if _, err := ParseTemplate([]byte(serve("measure_env", `["A=b"]`))); err != nil {
		t.Fatalf("baseline serve template refused: %v", err)
	}
	if _, err := ParseTemplate([]byte(verify("env", `["A=b"]`))); err != nil {
		t.Fatalf("baseline verify template refused: %v", err)
	}

	big := `{"version":1,"serve":{"upload_pack":["/sb","` + strings.Repeat("a", MaxTemplateBytes) + `"],"repository":"/r","measure":["/du"]}}`
	cases := map[string]string{
		"empty file":             ``,
		"not json":               `version`,
		"null":                   `null`,
		"array":                  `[]`,
		"no version":             `{"serve":null}`,
		"wrong version":          `{"version":2}`,
		"zero version":           `{"version":0}`,
		"string version":         `{"version":"1"}`,
		"unknown top field":      `{"version":1,"extra":1}`,
		"unknown serve field":    `{"version":1,"serve":{"upload_pack":["/sb"],"repository":"/r","measure":["/du"],"bogus":1}}`,
		"unknown verify field":   `{"version":1,"verify":{"prefix":["/sb"],"mirror":"/m","scratch":"/s","home":"/h","bogus":1}}`,
		"trailing object":        `{"version":1}{"version":1}`,
		"trailing junk":          `{"version":1} x`,
		"trailing null":          `{"version":1} null`,
		"over 64 KiB":            big,
		"empty serve":            `{"version":1,"serve":{}}`,
		"empty verify":           `{"version":1,"verify":{}}`,
		"empty upload argv":      serve("upload_pack", `[]`),
		"null upload argv":       serve("upload_pack", `null`),
		"relative upload":        serve("upload_pack", `["sb","{tree}"]`),
		"placeholder first":      serve("upload_pack", `["{tree}/sb"]`),
		"empty first element":    serve("upload_pack", `[""]`),
		"empty measure argv":     serve("measure", `[]`),
		"relative measure":       serve("measure", `["du"]`),
		"nul in upload":          serve("upload_pack", `["/sb","a\u0000b"]`),
		"nul in first":           serve("upload_pack", `["/s\u0000b"]`),
		"nul in env":             serve("measure_env", `["A=b\u0000"]`),
		"env not key value":      serve("measure_env", `["PATH"]`),
		"relative repository":    serve("repository", `"r"`),
		"unclean repository":     serve("repository", `"/r/../x"`),
		"empty repository":       serve("repository", `""`),
		"unknown placeholder":    serve("upload_pack", `["/sb","{nope}"]`),
		"verify ph in serve":     serve("upload_pack", `["/sb","{mirror}"]`),
		"serve ph in verify":     verify("prefix", `["/sb","{tree}"]`),
		"role in verify":         verify("prefix", `["/sb","{role}"]`),
		"empty placeholder":      serve("upload_pack", `["/sb","{}"]`),
		"spaced placeholder":     serve("upload_pack", `["/sb","{ tree }"]`),
		"upper placeholder":      serve("upload_pack", `["/sb","{TREE}"]`),
		"literal open brace":     serve("upload_pack", `["/sb","a{b"]`),
		"literal close brace":    serve("upload_pack", `["/sb","a}b"]`),
		"brace pair":             serve("upload_pack", `["/sb","{{tree}}"]`),
		"brace in env":           serve("measure_env", `["A={x}"]`),
		"brace in repository":    serve("repository", `"/r/{"`),
		"unterminated":           serve("upload_pack", `["/sb","{tree"]`),
		"empty prefix":           verify("prefix", `[]`),
		"relative prefix":        verify("prefix", `["sb"]`),
		"relative mirror":        verify("mirror", `"m"`),
		"unclean scratch":        verify("scratch", `"/s/"`),
		"empty home":             verify("home", `""`),
		"missing mirror":         `{"version":1,"verify":{"prefix":["/sb"],"scratch":"/s","home":"/h"}}`,
		"missing repository":     `{"version":1,"serve":{"upload_pack":["/sb"],"measure":["/du"]}}`,
		"missing measure":        `{"version":1,"serve":{"upload_pack":["/sb"],"repository":"/r"}}`,
		"verify env no equals":   verify("env", `["A"]`),
		"verify nul":             verify("home", `"/h\u0000"`),
		"serve field wrong type": serve("repository", `["/r"]`),
	}
	for name, in := range cases {
		if tm, err := ParseTemplate([]byte(in)); err == nil {
			t.Errorf("%s: accepted (%v)", name, tm)
		}
	}

	// A file of exactly the limit is not refused for its size.
	pad := `{"version":1}`
	pad += strings.Repeat(" ", MaxTemplateBytes-len(pad))
	if _, err := ParseTemplate([]byte(pad)); err != nil {
		t.Errorf("file of %d bytes refused: %v", len(pad), err)
	}
	if _, err := ParseTemplate([]byte(pad + " ")); err == nil {
		t.Error("file over the limit accepted")
	}
	// Trailing white space is not trailing data.
	if _, err := ParseTemplate([]byte("{\"version\":1}\n\n")); err != nil {
		t.Errorf("trailing newline refused: %v", err)
	}
}

func TestTemplateRefusesBadValues(t *testing.T) {
	tm := mustParse(t, fullTemplate)
	okServe := ServeRequest{Role: "lead", Project: "proj", Tree: "/t/x"}
	if _, err := tm.Serve(okServe); err != nil {
		t.Fatal(err)
	}
	for name, mod := range map[string]func(*ServeRequest){
		"empty role":      func(r *ServeRequest) { r.Role = "" },
		"upper role":      func(r *ServeRequest) { r.Role = "Lead" },
		"role slash":      func(r *ServeRequest) { r.Role = "a/b" },
		"role brace":      func(r *ServeRequest) { r.Role = "{tree}" },
		"role long":       func(r *ServeRequest) { r.Role = strings.Repeat("a", 64) },
		"role newline":    func(r *ServeRequest) { r.Role = "a\n" },
		"empty project":   func(r *ServeRequest) { r.Project = "" },
		"project dash":    func(r *ServeRequest) { r.Project = "-p" },
		"project dots":    func(r *ServeRequest) { r.Project = ".." },
		"empty tree":      func(r *ServeRequest) { r.Tree = "" },
		"relative tree":   func(r *ServeRequest) { r.Tree = "t" },
		"unclean tree":    func(r *ServeRequest) { r.Tree = "/t/../u" },
		"trailing slash":  func(r *ServeRequest) { r.Tree = "/t/" },
		"tree newline":    func(r *ServeRequest) { r.Tree = "/t\nx" },
		"tree nul":        func(r *ServeRequest) { r.Tree = "/t\x00x" },
		"tree open brace": func(r *ServeRequest) { r.Tree = "/t/{x" },
		"tree brace pair": func(r *ServeRequest) { r.Tree = "/t/{role}" },
		"tree close":      func(r *ServeRequest) { r.Tree = "/t}" },
	} {
		r := okServe
		mod(&r)
		if _, err := tm.Serve(r); err == nil {
			t.Errorf("serve %s accepted", name)
		} else if errors.Is(err, ErrNotConfigured) {
			t.Errorf("serve %s: wrong error %v", name, err)
		}
	}
	okVerify := VerifyRequest{Project: "proj", Mirror: "/m", Scratch: "/s", Home: "/h"}
	if _, err := tm.Verify(okVerify); err != nil {
		t.Fatal(err)
	}
	for name, mod := range map[string]func(*VerifyRequest){
		"empty project":   func(r *VerifyRequest) { r.Project = "" },
		"bad project":     func(r *VerifyRequest) { r.Project = "P" },
		"empty mirror":    func(r *VerifyRequest) { r.Mirror = "" },
		"relative mirror": func(r *VerifyRequest) { r.Mirror = "m" },
		"unclean scratch": func(r *VerifyRequest) { r.Scratch = "/s//x" },
		"scratch newline": func(r *VerifyRequest) { r.Scratch = "/s\n" },
		"home nul":        func(r *VerifyRequest) { r.Home = "/h\x00" },
		"home brace":      func(r *VerifyRequest) { r.Home = "/h/{project}" },
		"mirror dot":      func(r *VerifyRequest) { r.Mirror = "/m/." },
	} {
		r := okVerify
		mod(&r)
		if _, err := tm.Verify(r); err == nil {
			t.Errorf("verify %s accepted", name)
		}
	}
}

func TestTemplateSubstitutionIsNotRescanned(t *testing.T) {
	// A value cannot bring in a placeholder: braces are refused in values.
	// A project name cannot either, as names have no braces. The rendered
	// argv is exactly the template with the values put in once.
	tm := mustParse(t, `{"version":1,"verify":{"prefix":["/sb","{project}-{project}"],"mirror":"/m/{project}","scratch":"/s","home":"/h"}}`)
	p, err := tm.Verify(VerifyRequest{Project: "ab", Mirror: "/m", Scratch: "/s", Home: "/h"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.Prefix, []string{"/sb", "ab-ab"}) || p.Mirror != "/m/ab" {
		t.Fatalf("got %#v", p)
	}
}
