package noaicheck

import (
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

func TestProviderWords(t *testing.T) {
	got := providerWords("{$OPENAI_API_KEY} {env.ANTHROPIC_API_KEY} X_OPENAI_ORG 1CODEX_HOME OPENAI_API_KEY\n# GEMINI_API_KEY")
	want := []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q want %q", got, want)
	}
}
