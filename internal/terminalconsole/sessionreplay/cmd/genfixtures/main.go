// Command genfixtures writes the synthetic session recordings to a directory.
// It is the only code of the package that touches files. Build it with the
// selected toolchain and run it from the repository root; the package
// documentation gives the commands:
//
//	build/genfixtures -out internal/terminalconsole/sessionreplay/testdata
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"ataniq/internal/terminalconsole/sessionreplay/fixturegen"
)

func main() {
	out := flag.String("out", "testdata", "directory to write the recordings to")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "genfixtures: unexpected argument")
		os.Exit(2)
	}
	if err := run(*out); err != nil {
		fmt.Fprintln(os.Stderr, "genfixtures:", err)
		os.Exit(1)
	}
}

// run writes every recording to dir and removes the .jsonl files of dir that
// the generator no longer makes.
func run(dir string) error {
	files, err := fixturegen.Files()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	keep := make(map[string]bool, len(files))
	for _, f := range files {
		keep[f.Name] = true
		if err := os.WriteFile(filepath.Join(dir, f.Name), f.Data, 0o644); err != nil {
			return err
		}
	}
	old, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return err
	}
	for _, p := range old {
		if !keep[filepath.Base(p)] {
			if err := os.Remove(p); err != nil {
				return err
			}
		}
	}
	return nil
}
