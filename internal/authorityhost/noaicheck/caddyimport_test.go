package noaicheck

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

const importer = releaseDir + "/etc/caddy/Caddyfile"

// checkImports runs the release check over a fixture holding a release
// whose Caddyfile is caddyfile, after plant has put the host's files in
// place, and returns the hits and the report's text.
func checkImports(t *testing.T, caddyfile string, plant func(f *fixture)) ([]Hit, string) {
	t.Helper()
	f := cleanFixture(t)
	f.file(importer, caddyfile)
	if plant != nil {
		plant(f)
	}
	c := f.checker(testLists())
	var err error
	within(t, func() { err = c.CheckRelease(f.abs(releaseDir)) })
	if err != nil {
		t.Fatal(err)
	}
	r := c.Report()
	var text bytes.Buffer
	if err := r.WriteText(&text); err != nil {
		t.Fatal(err)
	}
	return r.Hits, text.String()
}

// A Caddyfile's import of an absolute path is followed and the file read
// on the host: a clean one adds no hit, one that names a provider key or
// calls out is a finding, a missing one is incomplete, never a pass.
func TestCheckReleaseFollowsCaddyImports(t *testing.T) {
	const site = "/etc/authority-host/seat/site.caddy"
	imp := "import " + site + "\n"
	cases := []struct {
		name  string
		caddy string
		plant func(f *fixture)
		want  []Hit
	}{
		{"a clean import", imp, func(f *fixture) {
			f.file("etc/authority-host/seat/site.caddy", "http://seat.example {\n\treverse_proxy unix//run/seat/http.sock\n\treverse_proxy localhost:8080 127.0.0.1:9 [::1]:7 http://127.0.0.2 :80\n}\n")
		}, nil},
		{"a provider key", imp, func(f *fixture) {
			f.file("etc/authority-host/seat/site.caddy", "http://seat.example {\n\theader X-Key {env.OPENAI_API_KEY}\n}\n")
		}, []Hit{{Kind: KindEnv, Path: site, Detail: "OPENAI_API_KEY"}}},
		{"an outbound target", imp, func(f *fixture) {
			f.file("etc/authority-host/seat/site.caddy", "http://seat.example {\n\treverse_proxy https://upstream.example:443\n}\n")
		}, []Hit{{Kind: KindProxy, Path: site, Detail: DetailOutbound}}},
		{"a missing file", imp, nil,
			[]Hit{{Kind: KindIncomplete, Path: PathCaddyImport, Detail: site}}},
		{"a folder in its place", imp, func(f *fixture) {
			f.dir("etc/authority-host/seat/site.caddy")
		}, []Hit{{Kind: KindUnreadable, Path: site, Detail: "not a regular file"}}},
		{"an import in a comment or a literal", "# " + imp + "respond \"import " + site + "\"\n", nil, nil},
		{"a snippet's name", "import seat\n", nil, nil},
		{"a placeholder in the path", "import {$SEAT_DIR}/site.caddy\n", nil,
			[]Hit{{Kind: KindIncomplete, Path: PathReleaseSyntax, Detail: "/" + importer}}},
		{"a relative path", "import ../site.caddy\n", nil,
			[]Hit{{Kind: KindIncomplete, Path: PathReleaseSyntax, Detail: "/" + importer}}},
		{"a glob matching nothing", "import /etc/authority-host/seat/*.caddy\n", func(f *fixture) {
			f.dir("etc/authority-host/seat")
		}, nil},
		{"a glob matching a bad file", "import /etc/authority-host/seat/*.caddy\n", func(f *fixture) {
			f.file("etc/authority-host/seat/a.caddy", "reverse_proxy 203.0.113.9:80\n")
			f.file("etc/authority-host/seat/b.txt", "reverse_proxy 203.0.113.8:80\n")
		}, []Hit{{Kind: KindProxy, Path: "/etc/authority-host/seat/a.caddy", Detail: DetailOutbound}}},
		{"an import of an import", imp, func(f *fixture) {
			f.file("etc/authority-host/seat/site.caddy", "import /etc/authority-host/seat/more.caddy\nimport extra.caddy\n")
			f.file("etc/authority-host/seat/more.caddy", "respond {env.CODEX_HOME_API_KEY}\n")
			f.file("etc/authority-host/seat/extra.caddy", "reverse_proxy {\n\tto 203.0.113.9:80\n}\n")
		}, []Hit{
			{Kind: KindEnv, Path: "/etc/authority-host/seat/more.caddy", Detail: "CODEX_HOME_API_KEY"},
			{Kind: KindProxy, Path: "/etc/authority-host/seat/extra.caddy", Detail: DetailOutbound},
		}},
		{"an import that imports itself", imp, func(f *fixture) {
			f.file("etc/authority-host/seat/site.caddy", imp)
		}, nil},
		{"a missing import of an imported file", imp, func(f *fixture) {
			f.file("etc/authority-host/seat/site.caddy", "import extra.caddy\n")
		}, []Hit{{Kind: KindIncomplete, Path: PathCaddyImport, Detail: "/etc/authority-host/seat/extra.caddy"}}},
		{"a syntax the check cannot read", imp, func(f *fixture) {
			f.file("etc/authority-host/seat/site.caddy", "header X {env."+secret+"}\n")
		}, []Hit{{Kind: KindIncomplete, Path: PathReleaseSyntax, Detail: site}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hits, text := checkImports(t, tc.caddy, tc.plant)
			if !reflect.DeepEqual(hits, tc.want) {
				t.Errorf("hits %v, want %v", hits, tc.want)
			}
			if strings.Contains(text, secret) || strings.Contains(text, "upstream.example") {
				t.Errorf("the report copies the file's text:\n%s", text)
			}
		})
	}
}

// A release whose imports are all clean is complete: the import is a
// pass only when the file was read.
func TestCheckReleaseImportCleanIsComplete(t *testing.T) {
	f := cleanFixture(t)
	f.file(importer, "import /etc/authority-host/seat/site.caddy\n")
	f.file("etc/authority-host/seat/site.caddy", seatSite)
	c := f.checker(testLists())
	within(t, func() {
		if err := c.CheckRelease(f.abs(releaseDir)); err != nil {
			t.Fatal(err)
		}
	})
	if r := c.Report(); len(r.Hits) != 0 || !r.Complete {
		t.Errorf("hits %v complete %v, want none and complete", r.Hits, r.Complete)
	}
}

// Imports of imports are followed to a bound; a file past it is
// incomplete, never passed.
func TestCheckReleaseImportDepthIsBounded(t *testing.T) {
	hits, _ := checkImports(t, "import /etc/c/0.caddy\n", func(f *fixture) {
		for i := 0; i < maxImportDepth+2; i++ {
			f.file("etc/c/"+string(rune('0'+i))+".caddy", "import /etc/c/"+string(rune('0'+i+1))+".caddy\n")
		}
	})
	if len(hits) != 1 || hits[0].Kind != KindIncomplete || hits[0].Path != PathCaddyImport {
		t.Errorf("hits %v, want one incomplete caddy-import", hits)
	}
}

// A JSON configuration has no import: nothing is followed.
func TestCheckReleaseJSONHasNoImports(t *testing.T) {
	f := cleanFixture(t)
	f.file(releaseDir+"/etc/caddy/c.json", `{"x":"import /etc/nowhere"}`)
	c := f.checker(testLists())
	within(t, func() {
		if err := c.CheckRelease(f.abs(releaseDir)); err != nil {
			t.Fatal(err)
		}
	})
	if got := c.Report().Hits; len(got) != 0 {
		t.Errorf("hits %v, want none", got)
	}
}

func TestCaddyImports(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		want     []string
	}{
		{"plain", "import /a/b\n", []string{"/a/b"}},
		{"quoted", "import \"/a b/c\" arg\nimport `/d`\n", []string{"/a b/c", "/d"}},
		{"with a block", "import /a {\n\tx y\n}\n", []string{"/a"}},
		{"indented in a block", "x {\n\timport /a\n}\n", []string{"/a"}},
		{"a comment", "# import /a\nimport /b # import /c\n", []string{"/b"}},
		{"not the first word", "respond import /a\nimportant /b\n", nil},
		{"no operand", "import\n", []string{""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := caddyImports(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCaddyProxyTargets(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		want     []string
	}{
		{"one", "reverse_proxy a:80\n", []string{"a:80"}},
		{"several, with a matcher", "reverse_proxy /api/* a:80 b:81\n", []string{"a:80", "b:81"}},
		{"a block", "reverse_proxy a:80 {\n\theader_up X y\n\tto c:82\n}\n", []string{"a:80", "c:82"}},
		{"a block only", "x {\n\treverse_proxy {\n\t\tto c:82 d:83\n\t}\n}\n", []string{"c:82", "d:83"}},
		{"to outside a proxy", "x {\n\tto c:82\n}\nreverse_proxy a:1 {\n}\nto e:5\n", []string{"a:1"}},
		{"dynamic", "reverse_proxy {\n\tdynamic a {\n\t\tx\n\t}\n}\n", []string{"{}"}},
		{"a comment", "# reverse_proxy a:80\nreverse_proxy b:81 # c:82\n", []string{"b:81"}},
		{"a placeholder", "reverse_proxy {$UP}\n", []string{"{$UP}"}},
		{"none", "respond ok\n", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := caddyProxyTargets(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLocalProxyTarget(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"unix//run/seat/http.sock", true},
		{"unix+h2c//run/x.sock", true},
		{"localhost", true},
		{"LOCALHOST:8080", true},
		{"http://localhost:8080/x", true},
		{"https://127.0.0.1", true},
		{"h2c://127.9.9.9:81", true},
		{"[::1]:80", true},
		{"::1", true},
		{"http://[::1]/", true},
		{":8080", true},
		{"203.0.113.9:80", false},
		{"0.0.0.0:80", false},
		{"https://upstream.example", false},
		{"localhost.example:80", false},
		{"{$UPSTREAM}", false},
		{"{http.request.host}:80", false},
		{"[2001:db8::1]:80", false},
		{"2001:db8::1", false},
		{"", false},
		{"unix", false},
	} {
		if got := localProxyTarget(tc.in); got != tc.want {
			t.Errorf("localProxyTarget(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
