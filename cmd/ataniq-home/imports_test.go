package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// Check the built Linux command's whole import graph, including dependencies
// introduced indirectly by a shared checkpoint package.
func forbiddenDependencies(t *testing.T, dir string) []string {
	t.Helper()
	cmd := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "list", "-deps", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOFLAGS=", "GOTOOLCHAIN=local", "GOWORK=off", "GOPROXY=off", "GOSUMDB=off")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	var forbidden []string
	for path := range strings.FieldsSeq(string(out)) {
		for _, p := range []string{"os/exec", "plugin", "ataniq/internal/leadregistry", "ataniq/internal/leaddaemon", "ataniq/internal/leadunit"} {
			if path == p || strings.HasPrefix(path, p+"/") {
				forbidden = append(forbidden, path)
			}
		}
	}
	return forbidden
}

func TestImports(t *testing.T) {
	if paths := forbiddenDependencies(t, "."); len(paths) != 0 {
		t.Errorf("home imports forbidden packages: %v", paths)
	}
}

func TestImportsDetectTransitiveLauncher(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "helper"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, src := range map[string]string{
		"go.mod":           "module example.test/home\n",
		"home.go":          "package home\nimport _ \"example.test/home/helper\"\n",
		"helper/helper.go": "package helper\nimport _ \"os/exec\"\nimport _ \"plugin\"\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0600); err != nil {
			t.Fatal(err)
		}
	}
	paths := forbiddenDependencies(t, dir)
	for _, want := range []string{"os/exec", "plugin"} {
		if !slices.Contains(paths, want) {
			t.Errorf("missed transitive import %s in %v", want, paths)
		}
	}
}
