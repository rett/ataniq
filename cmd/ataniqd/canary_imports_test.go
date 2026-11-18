package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoFileImportsTheInjectionTree: no file of the command, whatever its build
// constraint, imports anything under internal/inject. The same test holds in a
// build with the tag and in one without it.
func TestNoFileImportsTheInjectionTree(t *testing.T) {
	const tree = modulePath + "/internal/inject"
	for _, p := range importsOf(t, ".") {
		if p == tree || strings.HasPrefix(p, tree+"/") {
			t.Errorf("the command imports %s", p)
		}
	}
}

// TestCanaryFilesCarryTheirConstraint: a file of canary mode starts with the
// tagged constraint, and its release counterpart with the untagged one.
func TestCanaryFilesCarryTheirConstraint(t *testing.T) {
	files, err := filepath.Glob("canary_*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no canary files: %v", err)
	}
	for _, name := range files {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		first, _, _ := strings.Cut(string(data), "\n")
		switch {
		case strings.HasSuffix(name, "_imports_test.go"):
			// This file: both builds.
		case strings.HasPrefix(name, "canary_off_"):
			if first != "//go:build linux && !ataniq_canary" {
				t.Errorf("%s starts with %q", name, first)
			}
		default:
			if first != "//go:build linux && ataniq_canary" {
				t.Errorf("%s starts with %q", name, first)
			}
		}
	}
}
