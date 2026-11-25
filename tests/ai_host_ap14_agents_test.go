package tests

// The agent rule of deploy/ai-host/ap14-level1.sh in the instruction files.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The agent rule: AGENTS.md and CLAUDE.md say exactly what agents run: the
// script only with --self-test, never --check, which runs sudo as root, and
// the Go tests, which run --check only where it is refused; AGENTS.md links
// the README, which has the owner's steps for every change.
func TestAiHostAP14AgentRule(t *testing.T) {
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		data, err := os.ReadFile(filepath.Join(root(t), name))
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		for _, want := range []string{
			"`deploy/ai-host/ap14-level1.sh`",
			"is a read-only check",
			"run only by the owner's hand, as root",
			"an agent runs it only with `--self-test`, never with `--check` (it runs sudo as root)",
			"the Go tests of `./tests/`",
			"run `--check` only where it is refused on its arguments",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("%s does not say: %s", name, want)
			}
		}
		// The rule, from its first naming of the script to the end of its
		// line, names none of the modes that wrote files.
		rule := text[strings.Index(text, "`deploy/ai-host/ap14-level1.sh`"):]
		rule, _, _ = strings.Cut(rule, "\n")
		for _, bad := range []string{"--apply", "--verify", "--dry", "writes a sudoers drop-in"} {
			if strings.Contains(rule, bad) {
				t.Errorf("%s still says %s of ap14-level1.sh", name, bad)
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
	flat := ahFlat(string(readme))
	for _, want := range []string{
		"/bin/sh /root/ap14-new/ap14-level1.sh --check --user NAME", "sh deploy/ai-host/ap14-level1.sh --self-test",
		"It changes nothing.", "never an edit command",
		"mkdir -m 0700 /root/ap14-new", "install -m 0500 /PATH/TO/CHECKOUT/deploy/ai-host/ap14-level1.sh /root/ap14-new/ap14-level1.sh",
		"never copy the checkout's tree", "/usr/sbin:/usr/bin:/sbin:/bin",
		// The owner's own changes, with their exact steps.
		"visudo -f /etc/sudoers.d/zz-ap14-level1-NAME", "Defaults:NAME timestamp_timeout=0", "visudo -c",
		"remove the NOPASSWD: tag (or change it to PASSWD:), and keep one password-asked grant for you",
		"never delete a rule because of this check",
		"mkdir -m 0755 /etc/ataniq/grants",
		"install -m 0644 -o root -g root /root/GRANT.txt /etc/ataniq/grants/.GRANT.new",
		"Never change the owner or the mode of an entry",
		"rename the new file over the old name, which retires the old file",
		// The check's verdicts.
		"a run with a stop is never clean (exit 3)", "--other-root-login", "Exit status",
		// Verification: the check again, and the owner's test with its limit.
		"sudo -k; sudo -n true", "sudo: a password is required",
		"the test shows only that sudo asks for a password", "Under targetpw, rootpw or runaspw it is another account's",
		// The known limit, and what closes it.
		"This is a known limit of level 1; only a separate account, AP14 level 2, closes it.",
		"## Other routes to root (by hand)",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("the README does not say %q", want)
		}
	}
	for _, bad := range []string{"--apply", "--dry", "--verify", "--confirm", "MOVED", "backup-", "cp -R", "chown root:root", "/run/ap14-level1.lock", "ln "} {
		if strings.Contains(string(readme), bad) {
			t.Errorf("the README still says %q", bad)
		}
	}
}

// ap14ReadmeSection returns the README's text from the heading start to the
// next heading next, flattened as ahFlat does.
func ap14ReadmeSection(t *testing.T, readme, start, next string) string {
	t.Helper()
	i := strings.Index(readme, "\n"+start+"\n")
	if i < 0 {
		t.Fatalf("the README has no heading %q", start)
	}
	rest := readme[i+1:]
	j := strings.Index(rest, "\n"+next+"\n")
	if j < 0 {
		t.Fatalf("the README has no heading %q after %q", next, start)
	}
	return ahFlat(rest[:j])
}

// The guard against a lockout: before NOPASSWD goes, the owner reads the
// user's password state in the root shell, sets a password or keeps
// NOPASSWD when there is none, and keeps the root shell open until sudo has
// asked for that password once in a session agents cannot reach. The README
// says it in "Before you start" and again in step 3, before the tag is
// removed; the script cannot see it, so the README is the guard.
func TestAiHostAP14PasswordGuard(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(root(t), ap14Dir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	readme := string(data)
	guard := []string{
		"In the root shell, passwd -S NAME must show P in its second field.",
		"L, LK or NP there mean the user has no usable password: set one first with passwd NAME, or do not remove NOPASSWD until root can log in another way.",
		"Keep the root shell open until sudo has been used once with that password, in a session agents cannot reach.",
	}
	before := ap14ReadmeSection(t, readme, "## Before you start", "## Steps")
	step3 := ap14ReadmeSection(t, readme, "### 3. Make the edits by hand", "### 4. Write the drop-in by hand")
	for _, want := range guard {
		if !strings.Contains(before, want) {
			t.Errorf("Before you start does not say %q", want)
		}
	}
	step3Guard := append([]string{"Before you remove a NOPASSWD: tag, read the user's password state in the root shell: passwd -S NAME must show P in its second field."}, guard[1:]...)
	for _, want := range step3Guard {
		if !strings.Contains(step3, want) {
			t.Errorf("step 3 does not say %q", want)
		}
	}
	// In step 3 the guard comes before the edit that removes the tag.
	if g, e := strings.Index(step3, "passwd -S NAME"), strings.Index(step3, "remove the NOPASSWD: tag"); g < 0 || e < 0 || g > e {
		t.Errorf("step 3 does not read the password state before it removes the tag:\n%s", step3)
	}
}
