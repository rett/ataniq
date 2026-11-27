package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMultiGateFlags(t *testing.T) {
	dir := t.TempDir()
	args := []string{"--origin", "https://seat.example", "--socket", "seat.sock", "--client-addr-header", "X-Client"}
	for _, name := range []string{"a", "b"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(strings.Repeat(name, 64)), 0600); err != nil {
			t.Fatal(err)
		}
		args = append(args, "--gate-entry", name+",gate.example:443,"+path)
	}
	valid := append(append([]string{}, args...), "--identity", dir)
	o, _, ok := parse(valid, io.Discard)
	if !ok || len(o.entries) != 2 || o.entries[0].pin != strings.Repeat("a", 64) || o.entries[1].name != "b" {
		t.Fatal("gate list lost its pin or order")
	}
	for _, extra := range [][]string{nil, {"--identity", dir, "--gate", "gate.example:443"}, {"--identity", dir, "--notify-socket", "/notifier"}, {"--gate-entry", "bad"}, {"--gate-entry", "x,bad,bad"}, {"--gate-entry", "x,gate.example:443,missing"}} {
		if _, _, ok := parse(append(append([]string{}, args...), extra...), io.Discard); ok {
			t.Errorf("accepted %v", extra)
		}
	}
}
