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

// ownerBuildLine is the first line of every file that belongs to the owner
// channel's client: the file is built only on Linux with the tag.
const ownerBuildLine = "//go:build linux && ataniq_ownerchannel"

// ownerAllowed are the packages of this repository that a file of the owner
// channel's client may import besides the client protocol: the wire package
// of the owner connection and the values the console's approvals backend
// returns.
var ownerAllowed = map[string]bool{
	"ataniq/internal/approval/ownerwire": true,
	"ataniq/internal/approval/record":    true,
	"ataniq/internal/approval/card":      true,
	"ataniq/internal/approval/limits":    true,
}

// TestImportsOnlyTheClientProtocol holds the package's boundary: of this
// repository the files without the owner tag import only the client protocol,
// in the tests as well. A file of the owner channel's client, which starts
// with the tag's build line, may import also the wire package of the owner
// connection and the values of the console's backend, and no other: the
// console, the door, the service, the journal and the sample backend are
// never imported. The files are read as text whatever the build tags, so that
// the check holds in every build.
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
		f, err := parser.ParseFile(fset, name, src, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		tagged := strings.HasPrefix(string(src), ownerBuildLine+"\n")
		if strings.HasPrefix(name, "owner") && !tagged {
			t.Errorf("%s belongs to the owner channel's client and does not start with %q", name, ownerBuildLine)
		}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if path != "ataniq" && !strings.HasPrefix(path, "ataniq/") {
				continue
			}
			switch {
			case path == "ataniq/internal/clientapi":
				sawClient = true
			case tagged && ownerAllowed[path]:
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
		t.Error("no tagged file imports the owner connection's wire package: the check read nothing")
	}
}

// TestTheOwnerRulesRejectWhatTheyShould holds the rule above against its
// own mistakes: a console, a door or a service import in a tagged file, and a
// wire import in an untagged one, would each be refused.
func TestTheOwnerRulesRejectWhatTheyShould(t *testing.T) {
	for _, path := range []string{
		"ataniq/internal/terminalconsole", "ataniq/internal/terminalconsole/approvals",
		"ataniq/internal/approval/demo", "ataniq/internal/approval/door",
		"ataniq/internal/approval/ownerchannel", "ataniq/internal/approval/ownerjournal",
		"ataniq/internal/approval/mergebuild", "ataniq/internal/approval/presence",
	} {
		if ownerAllowed[path] {
			t.Errorf("the tagged files may import %s", path)
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
