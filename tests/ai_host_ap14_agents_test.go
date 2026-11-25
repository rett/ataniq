package tests

// The agent rule of deploy/ai-host/ap14-level1.sh in the instruction files.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The agent rule: AGENTS.md and CLAUDE.md say that agents run the script only
// with --self-test, and AGENTS.md links the README.
func TestAiHostAP14AgentRule(t *testing.T) {
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		data, err := os.ReadFile(filepath.Join(root(t), name))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			"`deploy/ai-host/ap14-level1.sh`",
			"run only by the owner's hand, as root",
			"an agent runs it only with `--self-test`, never with `--dry` or `--apply`",
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
	for _, want := range []string{"--dry --user", "--apply --user", "--confirm", "--self-test", "/etc/ataniq/grants", "/etc/sudoers.d/zz-ap14-level1", "/etc/ap14-level1/backup-", "Exit status"} {
		if !strings.Contains(string(readme), want) {
			t.Errorf("the README does not say %q", want)
		}
	}
}
