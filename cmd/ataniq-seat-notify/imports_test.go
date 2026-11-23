package main

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// The command is the standard library, package webpush and package seatnotice
// and nothing else, in its tests as well, and it starts no process but its
// own test's.
var moduleAllowed = map[string]bool{
	"ataniq/internal/seatnotice": true,
	"ataniq/internal/webpush":    true,
}

var never = map[string]bool{
	"os/exec": true, "unsafe": true, "plugin": true, "net/rpc": true, "runtime/cgo": true,
}

func TestImports(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	files := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		files++
		isTest := strings.HasSuffix(name, "_test.go")
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			first, _, _ := strings.Cut(p, "/")
			if first == "ataniq" {
				if !moduleAllowed[p] {
					t.Errorf("%s imports %s, which the notifier may not", name, p)
				}
				continue
			}
			if strings.Contains(first, ".") {
				t.Errorf("%s imports %s, which is not the standard library", name, p)
			}
			if never[p] && !(isTest && p == "os/exec") {
				t.Errorf("%s imports %s, which this command never uses", name, p)
			}
		}
	}
	if files < 8 {
		t.Errorf("read only %d files", files)
	}
}
