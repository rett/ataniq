package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Check the built Linux command's whole import graph, including dependencies
// introduced indirectly by a shared checkpoint package.
func TestImports(t *testing.T) {
	cmd := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "list", "-deps", ".")
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOFLAGS=", "GOTOOLCHAIN=local", "GOWORK=off", "GOPROXY=off", "GOSUMDB=off")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	for path := range strings.FieldsSeq(string(out)) {
		for _, p := range []string{"ataniq/internal/leadregistry", "ataniq/internal/leaddaemon", "ataniq/internal/leadunit"} {
			if path == p || strings.HasPrefix(path, p+"/") {
				t.Errorf("home imports %s", path)
			}
		}
	}
}
