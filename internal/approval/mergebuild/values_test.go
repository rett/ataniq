package mergebuild

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ataniq/internal/exchange/gitenv"
)

// Every value is checked before any text is written, and before git runs:
// the repository of this test does not exist and neither does its git, so a
// refusal with ErrValue shows that nothing was started.
func TestValuesAreCheckedBeforeAnythingRuns(t *testing.T) {
	nowhere := Repo{
		Git:    gitenv.Git{Path: filepath.Join(t.TempDir(), "no-such-git")},
		GitDir: filepath.Join(t.TempDir(), "no-such-repository"),
	}
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	long := strings.Repeat("x", 300)

	type change func(*Inputs)
	cases := []struct {
		name string
		// secret is a piece of the refused value that the error must not
		// repeat; "" when the value has nothing to repeat.
		secret string
		change change
	}{
		{"an abbreviated target tip", "abcdef1", func(in *Inputs) { in.TargetTip = "abcdef1" }},
		{"a ref as the target tip", "refs/heads", func(in *Inputs) { in.TargetTip = "refs/heads/develop" }},
		{"an empty target tip", "", func(in *Inputs) { in.TargetTip = "" }},
		{"an upper-case feature tip", "", func(in *Inputs) { in.FeatureTip = strings.Repeat("B", 40) }},
		{"a feature tip with a newline", "", func(in *Inputs) { in.FeatureTip = b[:39] + "\n" }},
		{"a feature tip of 41 digits", "", func(in *Inputs) { in.FeatureTip = b + "b" }},
		{"a feature tip that starts with a dash", "--all", func(in *Inputs) { in.FeatureTip = "--all" + b[5:] }},
		{"the two tips equal", "", func(in *Inputs) { in.FeatureTip = in.TargetTip }},

		{"an empty target", "", func(in *Inputs) { in.Target = "" }},
		{"a target of 201 bytes", "", func(in *Inputs) { in.Target = strings.Repeat("d", 201) }},
		{"a target with two dots", "a..b", func(in *Inputs) { in.Target = "a..b" }},
		{"a target with a space", "dev elop", func(in *Inputs) { in.Target = "dev elop" }},
		{"a target with a newline", "Injected", func(in *Inputs) { in.Target = "develop\n\nInjected: yes" }},
		{"a target with an escape", "[31m", func(in *Inputs) { in.Target = "develop\x1b[31m" }},
		{"a target with a colon", "a:b", func(in *Inputs) { in.Target = "a:b" }},
		{"a target with a tilde", "a~1", func(in *Inputs) { in.Target = "a~1" }},
		{"a target that is not ASCII", "dévelop", func(in *Inputs) { in.Target = "dévelop" }},
		{"a target with a NUL", "", func(in *Inputs) { in.Target = "develop\x00" }},

		{"an empty feature name", "", func(in *Inputs) { in.Feature = "" }},
		{"a feature name with a slash", "a/b", func(in *Inputs) { in.Feature = "a/b" }},
		{"a feature name in upper case", "Sample", func(in *Inputs) { in.Feature = "Sample" }},
		{"a feature name of 49 bytes", "", func(in *Inputs) { in.Feature = strings.Repeat("f", 49) }},
		{"a feature name with a newline", "Request-Id", func(in *Inputs) { in.Feature = "work\n\nRequest-Id: rq-9999" }},
		{"a feature name with the prefix", "feature/", func(in *Inputs) { in.Feature = "feature/work" }},

		{"an empty request id", "", func(in *Inputs) { in.RequestID = "" }},
		{"a request id without rq-", "ap-0001", func(in *Inputs) { in.RequestID = "ap-0001" }},
		{"a request id with only rq-", "", func(in *Inputs) { in.RequestID = "rq-" }},
		{"a request id with a newline", "Approved", func(in *Inputs) { in.RequestID = "rq-1\nApproved-By: owner" }},
		{"a request id in upper case", "RQ-1", func(in *Inputs) { in.RequestID = "RQ-1" }},

		{"an empty name", "", func(in *Inputs) { in.Identity.Name = "" }},
		{"a name with a newline", "committer Mallory", func(in *Inputs) { in.Identity.Name = "Host\ncommitter Mallory" }},
		{"a name with a NUL", "", func(in *Inputs) { in.Identity.Name = "Host\x00Lead" }},
		{"a name with <", "Host <", func(in *Inputs) { in.Identity.Name = "Host <mallory@example.invalid" }},
		{"a name with >", "Host >", func(in *Inputs) { in.Identity.Name = "Host > Lead" }},
		{"a name with an escape", "[2J", func(in *Inputs) { in.Identity.Name = "Host\x1b[2J" }},
		{"a name with a carriage return", "", func(in *Inputs) { in.Identity.Name = "Host\rLead" }},
		{"a name of 257 bytes", "", func(in *Inputs) { in.Identity.Name = long[:257] }},
		{"an empty address", "", func(in *Inputs) { in.Identity.Email = "" }},
		{"an address with a newline", "author Mallory", func(in *Inputs) { in.Identity.Email = "a@b\nauthor Mallory" }},
		{"an address with a NUL", "", func(in *Inputs) { in.Identity.Email = "a@b\x00" }},
		{"an address with <", "<a@b", func(in *Inputs) { in.Identity.Email = "<a@b" }},
		{"an address with >", "a@b>", func(in *Inputs) { in.Identity.Email = "a@b> 0 +0000" }},
		{"an address of 257 bytes", "", func(in *Inputs) { in.Identity.Email = long[:257] }},

		{"no time", "", func(in *Inputs) { in.At = time.Time{} }},
		{"a time before 1970", "", func(in *Inputs) { in.At = time.Unix(-1, 0) }},
		{"a time after the year 9999", "", func(in *Inputs) { in.At = time.Unix(253402300800, 0) }},
	}
	for _, tc := range cases {
		in := inputs(a, b)
		tc.change(&in)
		_, buildErr := Build(ctx, nowhere, in, "k")
		checkErr := Check(ctx, nowhere, a, in)
		for what, err := range map[string]error{"Build": buildErr, "Check": checkErr} {
			if !errors.Is(err, ErrValue) {
				t.Errorf("%s: %s returned %v, want ErrValue", tc.name, what, err)
				continue
			}
			if tc.secret != "" && strings.Contains(err.Error(), tc.secret) {
				t.Errorf("%s: the refusal repeats the value: %q", tc.name, err)
			}
			if strings.ContainsAny(err.Error(), "\n\x00\x1b") {
				t.Errorf("%s: the refusal holds a control character: %q", tc.name, err)
			}
		}
	}

	// The control: the same inputs unchanged pass the value checks, and
	// fail only where the repository is first looked at.
	if _, err := Build(ctx, nowhere, inputs(a, b), "k"); err == nil || errors.Is(err, ErrValue) {
		t.Errorf("valid values and no repository: Build returned %v", err)
	}
	if err := Check(ctx, nowhere, a, inputs(a, b)); err == nil || errors.Is(err, ErrValue) {
		t.Errorf("valid values and no repository: Check returned %v", err)
	}

	// Values at the edge of each rule are accepted.
	for name, change := range map[string]change{
		"a target of 200 bytes":      func(in *Inputs) { in.Target = strings.Repeat("d", 200) },
		"a target with every sign":   func(in *Inputs) { in.Target = "Release_1.2/next-step" },
		"a target with single dots":  func(in *Inputs) { in.Target = ".a.b." },
		"a feature name of 48 bytes": func(in *Inputs) { in.Feature = strings.Repeat("f", 48) },
		"a name of 256 bytes":        func(in *Inputs) { in.Identity.Name = long[:256] },
		"a name that is not ASCII":   func(in *Inputs) { in.Identity.Name = "Zoë Lead-Smith, Jr." },
		"the first second":           func(in *Inputs) { in.At = time.Unix(0, 0) },
		"the last second":            func(in *Inputs) { in.At = time.Unix(253402300799, 999) },
		"tips of 64 digits":          func(in *Inputs) { in.TargetTip, in.FeatureTip = a+a[:24], b+b[:24] },
	} {
		in := inputs(a, b)
		change(&in)
		if err := in.check(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The key of a pin is 1 to 64 lower-case letters, digits and dashes, and is
// checked before anything else.
func TestKeyIsChecked(t *testing.T) {
	nowhere := Repo{
		Git:    gitenv.Git{Path: filepath.Join(t.TempDir(), "no-such-git")},
		GitDir: filepath.Join(t.TempDir(), "no-such-repository"),
	}
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	for _, key := range []string{
		"", strings.Repeat("k", 65), "Key", "a/b", "a.b", "a_b", "a b", "../heads/develop", "k\n", "k\x00",
		"refs/heads/develop", "k.lock", "@", "é", "-k\t",
	} {
		if ValidKey(key) {
			t.Errorf("ValidKey(%q) is true", key)
		}
		if ref, err := PinRef(key); !errors.Is(err, ErrValue) || ref != "" {
			t.Errorf("PinRef(%q) = %q, %v", key, ref, err)
		}
		if _, err := Build(ctx, nowhere, inputs(a, b), key); !errors.Is(err, ErrValue) {
			t.Errorf("Build with the key %q returned %v", key, err)
		}
		if err := Unpin(ctx, nowhere, key, a); !errors.Is(err, ErrValue) {
			t.Errorf("Unpin with the key %q returned %v", key, err)
		}
	}
	for _, key := range []string{"k", "0", "-", strings.Repeat("k", 64), "a-1-b", "rq-0001"} {
		if !ValidKey(key) {
			t.Errorf("ValidKey(%q) is false", key)
		}
		if ref, err := PinRef(key); err != nil || ref != PinPrefix+key {
			t.Errorf("PinRef(%q) = %q, %v", key, ref, err)
		}
	}
	if PinPrefix != "refs/ataniq/pending/" {
		t.Errorf("PinPrefix = %q", PinPrefix)
	}
}

// The repository's description is checked too.
func TestRepoIsChecked(t *testing.T) {
	g := needGit(t)
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	for name, repo := range map[string]Repo{
		"no path":            {Git: g},
		"a relative path":    {Git: g, GitDir: "r.git"},
		"a path with a NUL":  {Git: g, GitDir: "/srv/r\x00.git"},
		"a negative timeout": {Git: g, GitDir: "/srv/r.git", Timeout: -time.Second},
	} {
		if _, err := Build(ctx, repo, inputs(a, b), "k"); !errors.Is(err, ErrValue) {
			t.Errorf("%s: Build returned %v", name, err)
		}
		if err := Check(ctx, repo, a, inputs(a, b)); !errors.Is(err, ErrValue) {
			t.Errorf("%s: Check returned %v", name, err)
		}
		if err := Unpin(ctx, repo, "k", a); !errors.Is(err, ErrValue) {
			t.Errorf("%s: Unpin returned %v", name, err)
		}
	}
}

// The commit text is laid out exactly as the package comment says, and its
// id is the one git computes.
func TestCommitText(t *testing.T) {
	a, b, tree := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	got := string(commitText(tree, inputs(a, b)))
	want := "tree cccccccccccccccccccccccccccccccccccccccc\n" +
		"parent aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n" +
		"parent bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n" +
		"author Host Lead <lead@host.invalid> 981155106 +0000\n" +
		"committer Host Lead <lead@host.invalid> 981155106 +0000\n" +
		"\n" +
		"Merge feature/sample-work into develop\n" +
		"\n" +
		"Request-Id: rq-0001\n"
	if got != want {
		t.Errorf("the text is\n%q\nwant\n%q", got, want)
	}
	// The digests of the empty commit text, which do not depend on git.
	if id := commitID(40, nil); id != "dcf5b16e76cce7425d0beaef62d79a7d10fce1f5" {
		t.Errorf("the SHA-1 id of an empty commit text is %s", id)
	}
	if id := commitID(64, nil); len(id) != 64 || id == commitID(64, []byte("x")) {
		t.Errorf("the SHA-256 id of an empty commit text is %s", id)
	}
}
