package noaicheck

import (
	"bytes"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const releaseDir = "opt/authority-host/releases/0008-cd34ef5"

// plantCandidate plants a candidate release beneath the root, not linked
// into the host: a unit that sets a provider key, one whose EnvironmentFile
// on the host does, a user unit with a home-relative one, a masked unit,
// and a Caddyfile that names a provider key.
func plantCandidate(f *fixture) {
	f.file(releaseDir+"/etc/systemd/app.service", "[Service]\nEnvironment=OPENAI_API_KEY="+secret+"\n")
	f.file(releaseDir+"/etc/systemd/app.service.d/env.conf", "[Service]\nEnvironmentFile=/etc/app/env\nEnvironmentFile=/etc/app/missing\n")
	f.file(releaseDir+"/etc/systemd/user/u.service", "[Service]\nEnvironmentFile=-%h/.config/u.env\n")
	f.link(releaseDir+"/etc/systemd/off.service", "/dev/null")
	f.file(releaseDir+"/etc/caddy/Caddyfile", "example.invalid {\n\treverse_proxy {$UPSTREAM}\n\theader X-Key {env.ANTHROPIC_API_KEY}\n}\n")
	f.file(releaseDir+"/etc/nftables/rules.nft", "table inet filter {\n\tchain input { type filter hook input priority 0; policy drop; }\n}\n")
	f.file("etc/app/env", "CODEX_HOME=x\n")
	f.file("home/user/.config/u.env", "GEMINI_API_KEY=x\n")
}

func TestRunPhase(t *testing.T) {
	rel := "/" + releaseDir
	env := func(p, n string) Hit { return Hit{Kind: KindEnv, Path: p, Detail: n} }
	releaseHits := []Hit{
		env(rel+"/etc/systemd/app.service", "OPENAI_API_KEY"),
		{Kind: KindIncomplete, Path: PathEnvFile, Detail: "/etc/app/missing"},
		env(rel+"/etc/caddy/Caddyfile", "ANTHROPIC_API_KEY"),
		env("/etc/app/env", "CODEX_HOME"),
		env("/home/user/.config/u.env", "GEMINI_API_KEY"),
	}
	port := Hit{Kind: KindPort, Path: "tcp:0.0.0.0:8080"}
	nft := Hit{Kind: KindIncomplete, Path: PathNFT, Detail: DetailListingMissing}
	cases := []struct {
		phase string
		want  []Hit
	}{
		{PhasePre, releaseHits},
		{PhasePost, append(append([]Hit{}, releaseHits...), port, nft)},
		{PhaseFull, []Hit{port, nft}},
	}
	for _, tc := range cases {
		t.Run(tc.phase, func(t *testing.T) {
			f := cleanFixture(t)
			plantCandidate(f)
			f.file("proc/net/tcp", tcpHeader+
				"   0: 00000000:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1 1 0 100 0 0 10 0\n")
			l := testLists()
			l.NFTDigest = NFTDigest([]byte("table inet filter {\n}\n"))
			c := f.checker(l)
			var r Report
			var err error
			within(t, func() { r, err = c.RunPhase(tc.phase, f.abs(releaseDir), nil) })
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(r.Hits, tc.want) || r.Phase != tc.phase {
				t.Errorf("phase %q hits\n got %v\nwant %v", r.Phase, r.Hits, tc.want)
			}
		})
	}
}

// The pre phase still requires every policy list with RequirePolicy, though
// it does not check the ports or the digest; a clean candidate passes it
// complete.
func TestRunPhasePrePolicy(t *testing.T) {
	f := cleanFixture(t)
	f.file(releaseDir+"/etc/systemd/app.service", "[Service]\nExecStart=/usr/bin/app\n")
	l := testLists()
	l.PackagesAllow = []string{"bash"}
	l.NFTDigest = NFTDigest([]byte("x"))
	c := f.checker(l)
	c.RequirePolicy = true
	r, err := c.RunPhase(PhasePre, f.abs(releaseDir), nil)
	if err != nil || len(r.Hits) != 0 || !r.Complete {
		t.Fatalf("clean candidate: %v %v complete %v", err, r.Hits, r.Complete)
	}

	l = testLists()
	l.PackagesAllow = []string{"bash"}
	l.PortsAllow = nil
	c = cleanFixture(t).checker(l)
	c.RequirePolicy = true
	r, _ = c.RunPhase(PhasePre, f.abs(releaseDir), nil)
	want := []Hit{{Kind: KindPolicy, Path: FilePortsAllow, Detail: DetailEmpty}, {Kind: KindPolicy, Path: FileNFTDigest, Detail: DetailMissing}}
	if !reflect.DeepEqual(r.Hits, want) || r.Complete {
		t.Errorf("hits %v", r.Hits)
	}
}

// A release outside the root is read where it is and named by its own
// path; one that cannot be opened is an error; an unknown phase too.
func TestRunPhaseReleaseOutsideTheRoot(t *testing.T) {
	f := cleanFixture(t)
	out := newFixture(t)
	out.file("etc/systemd/app.service", "[Service]\nPassEnvironment=CLAUDE_CONFIG_DIR\n")
	c := f.checker(testLists())
	r, err := c.RunPhase(PhasePre, out.root, nil)
	want := []Hit{{Kind: KindEnv, Path: filepath.ToSlash(out.root) + "/etc/systemd/app.service", Detail: "CLAUDE_CONFIG_DIR"}}
	if err != nil || !reflect.DeepEqual(r.Hits, want) {
		t.Errorf("%v %v", r.Hits, err)
	}
	if _, err := f.checker(testLists()).RunPhase(PhasePre, filepath.Join(out.root, "missing"), nil); err == nil {
		t.Error("a missing release folder was read")
	}
	if _, err := f.checker(testLists()).RunPhase("later", out.root, nil); err == nil || !strings.Contains(err.Error(), "unknown phase") {
		t.Errorf("an unknown phase: %v", err)
	}
}

// Only recognised references name a variable in the front end's
// configuration, by the file's format. In a Caddyfile they are Caddy's
// placeholders {env.NAME} and {$NAME}; in Caddy's native JSON (a ".json"
// file), read as JSON, only {env.NAME} in a string. Shell-style $NAME and
// ${NAME} mean nothing to Caddy and are literal text in either, never
// matched. A comment, a quoted string's text and a literal operand named
// like a key are not references, and a reference the check cannot read
// (or JSON it cannot parse) makes the file incomplete without copying its
// text.
func TestReleaseRefs(t *testing.T) {
	cases := []struct {
		name, file, in string
		want           []string
		bad            bool
	}{
		{"placeholders", "Caddyfile", "{$OPENAI_API_KEY} {env.ANTHROPIC_API_KEY} {$CODEX_HOME:/srv/x}\n", []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "CODEX_HOME"}, false},
		{"shell-style is literal text", "Caddyfile", "reverse_proxy $GEMINI_API_KEY ${CLAUDE_X}/y\nheader X \"sk" + "$OPENAI_PART${CODEX_REST}\"\n", nil, false},
		{"an unreadable shell-style brace is literal text", "site.caddy", "x ${" + secret + "}\n", nil, false},
		{"inside a quoted string", "Caddyfile", "header X-Key \"Bearer {env.X_API_KEY}\"\n", []string{"X_API_KEY"}, false},
		{"literal operands", "Caddyfile", "header X-Name OPENAI_API_KEY\nrespond \"CODEX_HOME\" 200\nX_OPENAI_ORG 1CODEX_HOME\n", nil, false},
		{"comments", "Caddyfile", "# {env.OPENAI_API_KEY} GEMINI_API_KEY\nexample.invalid { # $CODEX_HOME\n\trespond ok\n}\n", nil, false},
		{"a '#' inside a word or quotes is not a comment", "Caddyfile", "respond \"#{env.OPENAI_ORG}\"\nredir /a#{$CODEX_HOME}\n", []string{"OPENAI_ORG", "CODEX_HOME"}, false},
		{"other placeholders and dollars", "Caddyfile", "respond \"{http.request.uri} costs $5 or $$\"\npath_regexp ^/x$\n", nil, false},
		{"names not like a key are read and not reported", "Caddyfile", "{env.LANG} {$PORT}\n", nil, false},
		{"an unreadable env placeholder", "Caddyfile", "header X {env." + secret + "}\n", nil, true},
		{"an unclosed env placeholder", "Caddyfile", "header X {env.OPENAI_API_KEY", nil, true},
		{"an unreadable braced placeholder", "Caddyfile", "{$ X}\n", nil, true},
		{"in JSON", "caddy.json", `{"apps":{"http":{"servers":{"a":{"listen":["{env.OPENAI_ORG}"]}}}}}`, []string{"OPENAI_ORG"}, false},
		{"JSON: a literal dollar stays private", "caddy.json",
			`{"handle":[{"handler":"headers","response":{"set":{"Authorization":["Bearer sk$OPENAI_PART${CODEX_REST}"],"X":["{$GEMINI_API_KEY}"]}}}]}`, nil, false},
		{"JSON: an escaped placeholder is literal", "caddy.json", `{"body":"\\{env.OPENAI_ORG}"}`, nil, false},
		{"JSON: a placeholder spelled with escapes", "caddy.json", `{"body":"{env.OPENAI_ORG}"}`, []string{"OPENAI_ORG"}, false},
		{"JSON: a placeholder in a key", "caddy.json", `{"{env.CODEX_HOME}":1}`, []string{"CODEX_HOME"}, false},
		{"JSON: an unreadable placeholder", "caddy.json", `{"body":"{env.` + secret + `}"}`, nil, true},
		{"JSON that does not parse", "caddy.json", `{"body":"x"` + "\n", nil, true},
		{"JSON nested too deep", "caddy.json", strings.Repeat("[", 100000) + strings.Repeat("]", 100000), nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, bad := releaseRefs(tc.file, tc.in)
			if !reflect.DeepEqual(got, tc.want) || bad != tc.bad {
				t.Errorf("got %q bad=%v, want %q bad=%v", got, bad, tc.want, tc.bad)
			}
		})
	}
}

// A comment or a literal in the candidate's configuration is not reported,
// the firewall's files are not read for names, and an unreadable reference
// is reported by file with a fixed reason, never by its text.
func TestCheckReleaseNamesOnlyReferences(t *testing.T) {
	f := cleanFixture(t)
	f.file(releaseDir+"/etc/caddy/Caddyfile", "# reverse_proxy {env.OPENAI_API_KEY}\nexample.invalid {\n\theader X-Note CODEX_HOME\n}\n")
	f.file(releaseDir+"/etc/caddy/odd.caddy", "header X {env."+secret+"}\n")
	f.file(releaseDir+"/etc/caddy/caddy.json", `{"apps":{"http":{"servers":{"a":{"routes":[{"handle":[{"handler":"headers",`+
		`"response":{"set":{"Authorization":["Bearer sk-planted$OPENAI_PART${CODEX_REST}"]}}}]}]}}}}}`)
	f.file(releaseDir+"/etc/caddy/shell.caddy", "header X \"Bearer sk-planted$GEMINI_PART\"\n")
	f.file(releaseDir+"/etc/nftables/rules.nft", "define GEMINI_API_KEY = 1\ntable inet filter { comment \"ANTHROPIC_API_KEY\" }\n")
	c := f.checker(testLists())
	var r Report
	var err error
	within(t, func() { r, err = c.RunPhase(PhasePre, f.abs(releaseDir), nil) })
	if err != nil {
		t.Fatal(err)
	}
	want := []Hit{{Kind: KindIncomplete, Path: PathReleaseSyntax, Detail: "/" + releaseDir + "/etc/caddy/odd.caddy"}}
	if !reflect.DeepEqual(r.Hits, want) || r.Complete {
		t.Errorf("hits %v complete %v, want %v", r.Hits, r.Complete, want)
	}
	var text, js bytes.Buffer
	if err := r.WriteText(&text); err != nil {
		t.Fatal(err)
	}
	if err := r.WriteJSON(&js); err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{text.String(), js.String()} {
		if strings.Contains(out, "sk-") || strings.Contains(out, secret) || strings.Contains(out, "_PART") || strings.Contains(out, "_REST") {
			t.Errorf("a report copies the file's text:\n%s", out)
		}
	}
}
