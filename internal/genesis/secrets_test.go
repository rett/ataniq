package genesis

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The shapes of a secret that a scanner knows: a private key block, the
// prefixes of well-known token formats followed by a body, and an assignment
// such as a name, an equals sign and a value. The regular expressions are built here from pieces
// so that this file does not hold a literal that matches one of them.

// tokenPrefixes are the prefixes of well-known credential formats, the same
// the brief's detector refuses. Each counts at the start of a word and with a
// body of ten or more token characters after it.
var tokenPrefixes = []string{
	"sk-", "sk_live_", "sk_test_", "rk_live_", "pk_live_",
	"ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_", "glpat-",
	"xoxa-", "xoxb-", "xoxp-", "xoxr-", "xoxs-", "xoxe-",
	"AKIA", "ASIA", "AIza", "npm_", "hf_",
}

// assignmentNames are the names that, with an equals sign and a value,
// put a credential in a line.
var assignmentNames = []string{
	"password", "passwd", "pwd", "pass", "token", "secret",
	"api_key", "apikey", "api-key", "access_key", "private_key",
	"client_secret", "auth", "bearer",
}

func secretShapes() map[string]*regexp.Regexp {
	shapes := map[string]*regexp.Regexp{
		"a private key block":     regexp.MustCompile("-----" + "BEGIN|PRIVATE" + " KEY"),
		"a credential assignment": regexp.MustCompile(`(?i)\b(?:` + strings.Join(assignmentNames, "|") + `)=\S`),
	}
	quoted := make([]string, len(tokenPrefixes))
	for i, p := range tokenPrefixes {
		quoted[i] = regexp.QuoteMeta(p)
	}
	shapes["a token with a known prefix"] = regexp.MustCompile(`(?:^|[^A-Za-z0-9])(?:` + strings.Join(quoted, "|") + `)[A-Za-z0-9_-]{10,}`)
	return shapes
}

// scanned reports whether a file of the family is one the scan reads: a test
// file, anything under a testdata directory, and the data of the catalog and
// the plan templates.
func scanned(path string) bool {
	slashed := filepath.ToSlash(path)
	return strings.HasSuffix(slashed, "_test.go") || strings.Contains(slashed, "/testdata/") || strings.HasSuffix(slashed, ".json")
}

// No test file and no test data of the genesis packages holds a literal that
// a secret scanner would take for a key. A sample a test needs is built from
// pieces at run time.
func TestNoKeyShapedLiteral(t *testing.T) {
	shapes := secretShapes()
	files := 0
	dirs := map[string]bool{}
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !scanned(path) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files++
		dirs[filepath.ToSlash(filepath.Dir(path))] = true
		for n, line := range strings.Split(string(data), "\n") {
			for name, re := range shapes {
				if re.MatchString(line) {
					t.Errorf("%s:%d holds %s", path, n+1, name)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// The scan read the family: the tests of brief, catalog, canon and plan,
	// the testdata, the catalog and the plan templates.
	if files < 8 {
		t.Fatalf("the scan read %d files, want the whole family", files)
	}
	// The plan's tests, its golden plans and its templates among them.
	for _, dir := range []string{"brief", "catalog", "plan", "plan/templates", "plan/testdata"} {
		if !dirs[dir] {
			t.Errorf("the scan read nothing in %s", dir)
		}
	}
}

// The scan is not empty: it finds each shape in a line made of pieces.
func TestTheScanFindsEachShape(t *testing.T) {
	shapes := secretShapes()
	cases := map[string]string{
		"a private key block":         "-----" + "BEGIN RSA PRIV" + "ATE KEY-----",
		"a credential assignment":     "note: pass" + "word=hunter2",
		"a token with a known prefix": "my key is gh" + "p_" + strings.Repeat("aB3dE5", 6),
	}
	for name, line := range cases {
		if !shapes[name].MatchString(line) {
			t.Errorf("the scan does not find %s in %q", name, line)
		}
	}
	for _, p := range tokenPrefixes {
		line := "use " + p + strings.Repeat("a1", 6)
		if !shapes["a token with a known prefix"].MatchString(line) {
			t.Errorf("the scan does not find the prefix %q", p)
		}
	}
	for _, name := range assignmentNames {
		if !shapes["a credential assignment"].MatchString(strings.ToUpper(name) + "=x") {
			t.Errorf("the scan does not find %s=", name)
		}
	}
	// What an ordinary line holds is not found.
	for _, line := range []string{
		"risk-free trial for the first month", "ask-me-anything sessions", "bypass=on", "the password is shared at the desk",
		"a token of thanks", "sk-", "ghp_", "AKIA", "a hash 5d41402abc4b2a76b9719d911017c592", `"prefix": "ghp_"`,
	} {
		for name, re := range shapes {
			if re.MatchString(line) {
				t.Errorf("the scan finds %s in the ordinary line %q", name, line)
			}
		}
	}
}
