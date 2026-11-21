package gitenv

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestBundleURIIsPinned: a repository whose own config names fetch.bundleURI
// does not make a fetch read a bundle from that address. The control starts
// the same fetch without the setting from gitenv and the bundle is read (git
// writes its refs under refs/bundles/); with the setting it is not.
//
// Proves: with this git, the pinned empty value turns the feature off and the
// fetch still works. Cannot prove: anything for another git version.
func TestBundleURIIsPinned(t *testing.T) {
	g := needGit(t)
	env := fixtureEnv(t)
	src := source(t, g)
	bundle := filepath.Join(t.TempDir(), "b.bundle")
	fixture(t, g, env, src, "bundle", "create", bundle, "main")

	fetch := func(t *testing.T, pinned bool) (refs, out string) {
		t.Helper()
		repo := bare(t, g)
		appendConfig(t, repo, "[fetch]\n\tbundleURI = file://"+bundle+"\n")
		c := Cmd{GitDir: repo, Protocols: []string{"file"}, Args: []string{"fetch", "--end-of-options", src, "+refs/heads/*:refs/heads/*"}}
		argv, err := g.Argv(c)
		if err != nil {
			t.Fatal(err)
		}
		if !pinned {
			i := slices.Index(argv, "fetch.bundleURI=")
			if i < 1 || argv[i-1] != "-c" {
				t.Fatalf("the pin is not in the command line: %q", argv)
			}
			argv = slices.Delete(slices.Clone(argv), i-1, i+1)
		}
		out, err = run(argv, g.Env(c), "", "")
		if err != nil {
			t.Fatalf("fetch: %v\n%s", err, out)
		}
		refs = mustOwned(t, g, Cmd{GitDir: repo, Args: []string{"for-each-ref", "--format=%(refname)"}})
		return refs, out
	}

	if refs, out := fetch(t, false); !strings.Contains(refs, "refs/bundles/") {
		t.Fatalf("control: the bundle of the config was not read, so the test shows nothing:\n%s\n%s", refs, out)
	}
	refs, out := fetch(t, true)
	if strings.Contains(refs, "refs/bundles/") {
		t.Errorf("the bundle of the repository's config was read with the pin:\n%s\n%s", refs, out)
	}
	if !strings.Contains(refs, "refs/heads/main") {
		t.Errorf("the fetch with the pin did not fetch main:\n%s\n%s", refs, out)
	}
}
