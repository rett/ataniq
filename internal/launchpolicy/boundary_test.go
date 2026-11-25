package launchpolicy

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestT55ProductionBoundary(t *testing.T) {
	const self = "ataniq/internal/launchpolicy"
	cmd := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "list", "-deps", "-json=ImportPath,Standard,Deps,GoFiles,Dir", ".")
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOPROXY=off", "GOWORK=off", "GOFLAGS=")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(out))
	seen := map[string]bool{}
	for {
		var p struct {
			ImportPath, Dir string
			Standard        bool
			Deps, GoFiles   []string
		}
		if err := decoder.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		seen[p.ImportPath] = p.Standard
		switch {
		case p.Standard:
		case p.ImportPath == "ataniq/internal/pack":
		case p.ImportPath == "ataniq/internal/leadrole":
			for _, dep := range p.Deps {
				if !seen[dep] {
					t.Errorf("nonstandard role dependency %s", dep)
				}
			}
		case p.ImportPath == self:
			for _, file := range p.GoFiles {
				b, err := os.ReadFile(filepath.Join(p.Dir, file))
				if err != nil {
					t.Fatal(err)
				}
				src := strings.ToLower(string(b))
				for _, word := range []string{"alpha", "beta", "claude", "codex", "//go:build"} {
					if strings.Contains(src, word) {
						t.Errorf("%s contains %q", file, word)
					}
				}
			}
		default:
			t.Errorf("nonstandard matcher dependency %s", p.ImportPath)
		}
	}
	if _, ok := seen[self]; !ok {
		t.Fatal("missing matcher in dependency listing")
	}
}
