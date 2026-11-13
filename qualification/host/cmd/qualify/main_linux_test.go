//go:build linux && amd64

package main

import (
	"bytes"
	"testing"
)

func TestNoImplicitHostOperations(t *testing.T) {
	for _, args := range [][]string{nil, {"run"}, {"run", "--config", "/absent"}, {"reconcile", "--campaign", "/absent", "--attempt", "0123456789abcdef0123456789abcdef"}, {"run", "--allow-host-operations", "--config", "relative"}, {"private-inject", "relative"}, {"inspect", "--allow-host-operations", "--campaign", "/absent"}} {
		var b bytes.Buffer
		if run(args, &b) == 0 {
			t.Fatalf("accepted %q", args)
		}
		if b.Len() != 0 {
			t.Fatal("rejected input echoed")
		}
	}
}
