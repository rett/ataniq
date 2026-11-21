package main

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
// the tagged constraint (a test of both tags together also names the canary
// tag), and its release counterpart with the untagged one. Every file of the
// command that names the owner channel in its name is one of the two.
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

// TestNoUntaggedFileOfTheCommandReachesTheOwnerChannel: a file of the command
// that does not need the tag imports nothing under internal/approval, so a
// build without the tag cannot depend on the service, the journal or the
// stand-in through it.
func TestNoUntaggedFileOfTheCommandReachesTheOwnerChannel(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		if strings.Contains(ownerFirstLine(t, name), "ataniq_ownerchannel") && !strings.Contains(ownerFirstLine(t, name), "!ataniq_ownerchannel") {
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
			if strings.HasPrefix(p, modulePath+"/internal/approval") {
				t.Errorf("%s, which is built without the tag, imports %s", name, p)
			}
		}
	}
}
