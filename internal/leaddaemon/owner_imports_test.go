package leaddaemon

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// ownerFirstLine returns the first line of a file.
func ownerFirstLine(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	first, _, _ := strings.Cut(string(data), "\n")
	return first
}

// TestOwnerFilesCarryTheirConstraint: a file of the owner channel starts with
// the tagged constraint (the tests of both tags together name the canary tag
// as well), and a release counterpart with the untagged one. Every file of the
// package that names the owner channel in its name is one of the two.
func TestOwnerFilesCarryTheirConstraint(t *testing.T) {
	files, err := filepath.Glob("owner_*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no owner files: %v", err)
	}
	for _, name := range files {
		first := ownerFirstLine(t, name)
		switch {
		case strings.HasSuffix(name, "_imports_test.go"):
			// This file: both builds.
		case strings.HasPrefix(name, "owner_off_"):
			if first != "//go:build linux && !ataniq_ownerchannel" {
				t.Errorf("%s starts with %q", name, first)
			}
		case strings.HasPrefix(name, "owner_canary_"):
			if first != "//go:build linux && ataniq_ownerchannel && ataniq_canary" {
				t.Errorf("%s starts with %q", name, first)
			}
		default:
			if first != "//go:build linux && ataniq_ownerchannel" {
				t.Errorf("%s starts with %q", name, first)
			}
		}
	}
}

// TestNoUntaggedFileOfTheDaemonReachesTheOwnerChannel: a file of the package
// that does not need the tag imports nothing under internal/approval, so a
// build without the tag does not depend on the service, the journal or the
// stand-in for the door through the daemon. (The import guard of this package
// reads every file, and the tag guard in tests/ reads the build.)
//
// The one exception is the scratch leads' chat (chat_*_linux.go and its
// tests, behind the canary tag): it takes the wire's types of seat.chat from
// ownerwire, and nothing else of internal/approval (see chatImportsAllowed).
func TestNoUntaggedFileOfTheDaemonReachesTheOwnerChannel(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		first := ownerFirstLine(t, name)
		if strings.Contains(first, "ataniq_ownerchannel") && !strings.Contains(first, "!ataniq_ownerchannel") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if chatFile(name) && p == modulePath+"/internal/approval/ownerwire" {
				continue
			}
			if strings.HasPrefix(p, modulePath+"/internal/approval") {
				t.Errorf("%s, which is built without the tag, imports %s", name, p)
			}
		}
	}
}
