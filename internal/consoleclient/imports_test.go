package consoleclient

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// ownerTag is the build tag of the daemon's owner channel. The client is
// not behind it: the console and its client are in every build, and a daemon
// that has no owner channel is found at run time, from its hello.
const ownerTag = "ataniq_ownerchannel"

// ownerAllowed are the packages of this repository that a file of the owner
// channel's client (named owner*.go) may import besides the client protocol: the wire package
// of the owner connection and the values the console's approvals backend
// returns.
var ownerAllowed = map[string]bool{
	"ataniq/internal/approval/ownerwire": true,
	"ataniq/internal/approval/record":    true,
	"ataniq/internal/approval/card":      true,
	"ataniq/internal/approval/limits":    true,
}

// namesTheTag reports whether a line of src is a build constraint that names
// the owner channel's tag.
func namesTheTag(src string) bool {
	for _, line := range strings.Split(src, "\n") {
		line = strings.TrimSpace(line)
		if (strings.HasPrefix(line, "//go:build") || strings.HasPrefix(line, "// +build")) && strings.Contains(line, ownerTag) {
			return true
		}
	}
	return false
}

// TestImportsOnlyTheClientProtocol holds the package's boundary: of this
// repository the files import only the client protocol, in the tests as well.
// A file of the owner channel's client, named owner*.go, may import also the
// wire package of the owner connection and the values of the console's
// backend, and no other: the console, the door, the service, the journal and
// the sample backend are never imported. No file of the package is behind the
// owner channel's build tag. The files are read as text whatever the build
// tags, so that the check holds in every build.
func TestImportsOnlyTheClientProtocol(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no files: %v", err)
	}
	fset := token.NewFileSet()
	sawClient, sawOwner := false, false
	for _, name := range files {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, parser.ImportsOnly|parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		if namesTheTag(string(src)) {
			t.Errorf("%s is behind the build tag %s: the console client is in every build", name, ownerTag)
		}
		owner := strings.HasPrefix(name, "owner")
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if path != "ataniq" && !strings.HasPrefix(path, "ataniq/") {
				continue
			}
			switch {
			case path == "ataniq/internal/clientapi":
				sawClient = true
			case owner && ownerAllowed[path]:
				sawOwner = true
			default:
				t.Errorf("%s imports %s", name, path)
			}
		}
	}
	if !sawClient {
		t.Error("no file imports ataniq/internal/clientapi: the check read nothing")
	}
	if !sawOwner {
		t.Error("no owner file imports the owner connection's wire package: the check read nothing")
	}
}

// TestTheTagRuleSeesAConstraint holds the rule against its own mistakes: a
// constraint line that names the tag is found, alone or with others, and
// prose that names it is not a constraint.
func TestTheTagRuleSeesAConstraint(t *testing.T) {
	for src, want := range map[string]bool{
		"//go:build linux && " + ownerTag + "\n\npackage x\n":       true,
		"//go:build " + ownerTag + "\npackage x\n":                  true,
		"// +build " + ownerTag + "\npackage x\n":                   true,
		"//go:build linux\n\npackage x\n":                           false,
		"// The tag " + ownerTag + " is the daemon's.\npackage x\n": false,
	} {
		if got := namesTheTag(src); got != want {
			t.Errorf("%q: %v, want %v", src, got, want)
		}
	}
}

// TestTheOwnerRulesRejectWhatTheyShould holds the rule above against its
// own mistakes: a console, a door or a service import in an owner file would
// be refused.
func TestTheOwnerRulesRejectWhatTheyShould(t *testing.T) {
	for _, path := range []string{
		"ataniq/internal/terminalconsole", "ataniq/internal/terminalconsole/approvals",
		"ataniq/internal/approval/demo", "ataniq/internal/approval/door",
		"ataniq/internal/approval/ownerchannel", "ataniq/internal/approval/ownerjournal",
		"ataniq/internal/approval/mergebuild", "ataniq/internal/approval/presence",
	} {
		if ownerAllowed[path] {
			t.Errorf("the owner files may import %s", path)
		}
	}
	for path := range ownerAllowed {
		if !strings.HasPrefix(path, "ataniq/internal/approval/") {
			t.Errorf("%s is not under internal/approval", path)
		}
	}
}

// TestOnlyAFileForOneSystemImportsSyscall holds the package building where
// the client side of the client protocol builds: the syscall package differs
// from system to system, so only a file named for one system may import it.
func TestOnlyAFileForOneSystemImportsSyscall(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no files: %v", err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		base := strings.TrimSuffix(strings.TrimSuffix(name, ".go"), "_test")
		if strings.HasSuffix(base, "_linux") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			if path, _ := strconv.Unquote(imp.Path.Value); path == "syscall" || strings.HasPrefix(path, "golang.org/x/sys/") {
				t.Errorf("%s builds on every system and imports %s", name, path)
			}
		}
	}
}
