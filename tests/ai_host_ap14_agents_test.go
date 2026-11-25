package tests

// The agent rule of deploy/ai-host/ap14-level1.sh in the instruction files.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The agent rule: AGENTS.md and CLAUDE.md say exactly what agents run: the
// script only with --self-test, and the Go tests, which run --apply and
// --verify only where they are refused; AGENTS.md links the README.
func TestAiHostAP14AgentRule(t *testing.T) {
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		data, err := os.ReadFile(filepath.Join(root(t), name))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			"`deploy/ai-host/ap14-level1.sh`",
			"run only by the owner's hand, as root",
			"an agent runs it only with `--self-test`, never with `--dry`, `--apply` or `--verify`",
			"the Go tests of `./tests/`",
			"run `--apply` and `--verify` only where they are refused on their arguments, never as root",
		} {
			if !strings.Contains(string(data), want) {
				t.Errorf("%s does not say: %s", name, want)
			}
		}
	}
	agents, err := os.ReadFile(filepath.Join(root(t), "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(agents), "(deploy/ai-host/README.md)") || !strings.Contains(string(agents), "-run AiHost") {
		t.Error("AGENTS.md does not link the README or name the targeted check")
	}
	readme, err := os.ReadFile(filepath.Join(root(t), ap14Dir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--dry --user", "--apply --user", "--confirm", "--verify --user", "--self-test", "/etc/ataniq/grants", "/etc/sudoers.d/zz-ap14-level1-NAME", "Exit status",
		"never moves, edits or removes an existing file", "install -m 0500 /PATH/TO/CHECKOUT/deploy/ai-host/ap14-level1.sh /root/ap14-new/ap14-level1.sh",
		"mkdir -m 0700 /root/ap14-new", "never copy the checkout's tree", "/usr/sbin:/usr/bin:/sbin:/bin", "remove only the files the run created"} {
		if !strings.Contains(string(readme), want) {
			t.Errorf("the README does not say %q", want)
		}
	}
	for _, bad := range []string{"MOVED", "backup-", "cp -R", "moves that file aside"} {
		if strings.Contains(string(readme), bad) {
			t.Errorf("the README still says %q", bad)
		}
	}
}
