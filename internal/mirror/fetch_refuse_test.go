package mirror

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ataniq/internal/exchange/gitenv"
)

// hashObject writes a raw object into the repository at dir and returns its
// id. The content is not checked ("--literally"), so it can be a broken
// commit.
func hashObject(t *testing.T, g gitenv.Git, dir, typ, content string) string {
	t.Helper()
	cmd := exec.Command(g.Path, "hash-object", "-w", "--literally", "-t", typ, "--stdin")
	cmd.Dir = dir
	cmd.Env = fixtureEnv(t)
	cmd.Stdin = strings.NewReader(content)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("hash-object: %v: %s", err, errb.String())
	}
	return strings.TrimSpace(out.String())
}

func TestFetchRefusesOddLeadRefNames(t *testing.T) {
	// git takes these branch names; this package does not list them. One
	// such branch refuses the whole fetch, with the count and no name, so
	// that the mirror never holds a lead ref that the listings refuse.
	cases := []struct {
		name   string
		branch string
	}{
		{"not UTF-8", "x\xff"},
		{"not UTF-8 in a folder", "feature/\xc3\x28"},
		{"over 255 bytes", strings.Repeat("a", 200) + "/" + strings.Repeat("b", 100)},
		{"exactly 256 bytes", strings.Repeat("a", 128) + "/" + strings.Repeat("b", 127)},
		{"HEAD", "HEAD"},
		{"leading dash", "-dash"},
		{"refs prefix", "refs/x"},
		{"at sign", "@"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			state := stateOf(f.m)
			lead := newLeadClone(t, f.g, state, "coder")
			lead.git(t, "branch", "good")
			lead.git(t, "update-ref", "refs/heads/"+tc.branch, lead.git(t, "rev-parse", "HEAD"))
			if len(tc.branch) < 255 && tc.name == "exactly 256 bytes" {
				t.Fatalf("the case is not 256 bytes: %d", len(tc.branch))
			}
			e := &fetchEnv{g: f.g, m: f.m, state: state, lead: lead, planner: newPlanner(t)}
			was := e.state0(t)

			_, err := f.m.Fetch(ctx, e.req())
			if !errors.Is(err, ErrRefused) {
				t.Fatalf("Fetch = %v, want ErrRefused", err)
			}
			if !strings.Contains(err.Error(), "1 of 3 refs") {
				t.Errorf("the error does not give the count: %v", err)
			}
			for _, part := range []string{"good", "main", "dash", "xff", "aaaa"} {
				if strings.Contains(err.Error(), part) {
					t.Errorf("the error holds a name: %v", err)
				}
			}
			e.unchanged(t, was)
			// The listings of the other leads work.
			roles, err := f.m.Roles(ctx)
			if err != nil || len(roles) != 2 {
				t.Errorf("Roles = %v, %v", roles, err)
			}
			if refs, err := f.m.LeadBranches(ctx, "other"); err != nil || len(refs) != 1 {
				t.Errorf("LeadBranches(other) = %v, %v", refs, err)
			}
			if refs, err := f.m.LeadBranches(ctx, "coder"); err != nil || len(refs) != 2 {
				t.Errorf("LeadBranches(coder) = %v, %v", refs, err)
			}
		})
	}
}

func TestFetchRefusesLeadRefsThatAreNotCommits(t *testing.T) {
	for _, kind := range []string{"annotated tag", "tree", "blob"} {
		t.Run(kind, func(t *testing.T) {
			e := newFetchEnv(t)
			e.lead.git(t, "tag", "--no-sign", "-a", "-m", "t", "tagged")
			id := map[string]string{
				"annotated tag": e.lead.git(t, "rev-parse", "refs/tags/tagged"),
				"tree":          e.lead.git(t, "rev-parse", "HEAD^{tree}"),
				"blob":          e.lead.git(t, "rev-parse", "HEAD:file.txt"),
			}[kind]
			// update-ref refuses a non-commit for a branch, and the lead can
			// write the ref file itself.
			if err := os.WriteFile(filepath.Join(e.lead.gitDir(), "refs", "heads", "odd"), []byte(id+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			was := e.state0(t)
			_, err := e.m.Fetch(context.Background(), e.req())
			if err == nil {
				t.Fatal("a lead ref to a " + kind + " was fetched")
			}
			t.Logf("Fetch: %v", err)
			e.unchanged(t, was)
		})
	}
}

func TestCheckArrivedRefusesWhatIsNotACommit(t *testing.T) {
	// Whatever git does with a lead ref to a tag object, a tree or a blob,
	// the check of the staging repository refuses it. Here the refs are put
	// into a staging repository by hand.
	f := newFixture(t)
	ctx := context.Background()
	prefix, _ := LeadPrefix("coder")
	lim := DefaultFetchLimits()
	for _, tc := range []struct{ name, id string }{
		{"tag", f.src.tag}, {"tree", f.src.tree}, {"blob", f.src.blob},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := f.m.newStaging(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer s.remove()
			mirrorGit(t, f.g, s.path, nil, "update-ref", prefix+"good", f.first)
			if err := f.m.checkArrived(ctx, s, prefix, lim); err != nil {
				t.Fatalf("a commit was refused: %v", err)
			}
			mirrorGit(t, f.g, s.path, nil, "update-ref", prefix+"odd", tc.id)
			err = f.m.checkArrived(ctx, s, prefix, lim)
			if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "1 of 2 refs") {
				t.Errorf("checkArrived = %v", err)
			}
		})
	}
	t.Run("a ref outside the namespace", func(t *testing.T) {
		s, err := f.m.newStaging(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer s.remove()
		mirrorGit(t, f.g, s.path, nil, "update-ref", "refs/heads/develop", f.first)
		if err := f.m.checkArrived(ctx, s, prefix, lim); !errors.Is(err, ErrRefused) {
			t.Errorf("checkArrived = %v", err)
		}
	})
	t.Run("another lead's namespace", func(t *testing.T) {
		s, err := f.m.newStaging(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer s.remove()
		mirrorGit(t, f.g, s.path, nil, "update-ref", "refs/leads/other/heads/x", f.first)
		if err := f.m.checkArrived(ctx, s, prefix, lim); !errors.Is(err, ErrRefused) {
			t.Errorf("checkArrived = %v", err)
		}
	})
	t.Run("too many refs", func(t *testing.T) {
		s, err := f.m.newStaging(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer s.remove()
		for i := 0; i < 3; i++ {
			mirrorGit(t, f.g, s.path, nil, "update-ref", prefix+"b"+strconv.Itoa(i), f.first)
		}
		lim := lim
		lim.MaxRefs = 2
		var le *LimitError
		if err := f.m.checkArrived(ctx, s, prefix, lim); !errors.As(err, &le) || le.What != "refs" || le.Max != 2 {
			t.Errorf("checkArrived = %v", err)
		}
		lim.MaxRefs = 3
		if err := f.m.checkArrived(ctx, s, prefix, lim); err != nil {
			t.Errorf("exactly the cap was refused: %v", err)
		}
	})
	t.Run("the staging repository changed", func(t *testing.T) {
		s, err := f.m.newStaging(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer s.remove()
		if err := os.WriteFile(s.path+"/shallow", nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := f.m.checkArrived(ctx, s, prefix, lim); !errors.Is(err, ErrRefused) {
			t.Errorf("checkArrived = %v", err)
		}
	})
}

func TestFetchRefusesObjectsThatFailFsck(t *testing.T) {
	e := newFetchEnv(t)
	e.lead.git(t, "branch", "good")
	// A commit with an author line that has no date: git reads the commit
	// as it is, and fsck on the receiving side refuses it.
	tree := e.lead.git(t, "rev-parse", "HEAD^{tree}")
	bad := hashObject(t, e.g, e.lead.tree, "commit", "tree "+tree+"\nauthor A <a@example.invalid>\ncommitter C <c@example.invalid> 1767225600 +0000\n\nbroken\n")
	e.lead.git(t, "update-ref", "refs/heads/bad", bad)
	if out, err := exec.Command(e.g.Path, "--git-dir="+e.lead.gitDir(), "fsck", "--strict").CombinedOutput(); err == nil {
		t.Fatalf("the control commit passes fsck: %s", out)
	}
	// The control: plain git takes the commit, and takes it no more when it
	// checks objects. So it is fsck that stops the fetch below.
	scratch := t.TempDir()
	fixtureGit(t, e.g, scratch, "init", "--quiet", "--bare")
	fixtureGit(t, e.g, scratch, "fetch", "--quiet", e.lead.gitDir(), "+refs/heads/bad:refs/heads/bad")
	scratch2 := t.TempDir()
	fixtureGit(t, e.g, scratch2, "init", "--quiet", "--bare")
	cmd := exec.Command(e.g.Path, "-c", "transfer.fsckObjects=true", "fetch", "--quiet", e.lead.gitDir(), "+refs/heads/bad:refs/heads/bad")
	cmd.Dir, cmd.Env = scratch2, fixtureEnv(t)
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "fsck") && !strings.Contains(string(out), "missing") {
		t.Fatalf("the control: a checking fetch took the bad commit or failed for another reason: %v: %s", err, out)
	}

	was := e.state0(t)
	_, err := e.m.Fetch(context.Background(), e.req())
	if err == nil {
		t.Fatal("a commit that fails fsck was fetched")
	}
	t.Logf("Fetch: %v", err)
	e.unchanged(t, was)
	if hasObject(t, e.g, e.m.Path(), bad) {
		t.Error("the broken commit is in the mirror")
	}
}
