//go:build linux && ataniq_canary

package leadcanary_test

import (
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

const injectTree = "ataniq/internal/inject"

// TestOnlyTheAdmissionImportsTheInjectionTree holds the rule of the plan: one
// file of the package, admission.go, imports from internal/inject, and it
// imports only the command package. No other file, tests included, imports
// anything under the injection tree.
func TestOnlyTheAdmissionImportsTheInjectionTree(t *testing.T) {
	var admissionImports []string
	for _, name := range packageFiles(t) {
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if path != injectTree && !strings.HasPrefix(path, injectTree+"/") {
				continue
			}
			if name != "admission.go" {
				t.Errorf("%s imports %s", name, path)
				continue
			}
			admissionImports = append(admissionImports, path)
		}
	}
	if len(admissionImports) != 1 || admissionImports[0] != injectTree+"/command" {
		t.Fatalf("admission.go imports %v from the injection tree, want only %s/command", admissionImports, injectTree)
	}
}
