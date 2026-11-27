package profile

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"testing"
)

func TestQualifiedLeadIdentity(t *testing.T) {
	const tag = "0123456789abcdef"
	authority := strings.Repeat(tag, 4)
	long := strings.Repeat("p", 63)
	sum := sha256.Sum256([]byte(long))
	qargiID := regexp.MustCompile(`^[A-Za-z0-9._@:+-]{1,128}$`)
	for project, want := range map[string]string{
		"demo":                  "demo-lead-" + tag,
		strings.Repeat("p", 42): strings.Repeat("p", 42) + "-lead-" + tag,
		long:                    long[:25] + "-" + hex.EncodeToString(sum[:8]) + "-lead-" + tag,
	} {
		got, err := QualifiedLeadIdentity(project, authority)
		if err != nil || got != want || !validLeadIdentity(got) || !qargiID.MatchString(got) {
			t.Errorf("%q: got %q, %v, want %q", project, got, err, want)
		}
	}
	for _, bad := range []string{"", tag, strings.ToUpper(authority), authority[:63] + "z"} {
		if id, err := QualifiedLeadIdentity("demo", bad); err == nil || id != "" || refusalOf(t, err).Field != "instance authority" {
			t.Errorf("invalid authority produced %q, %v", id, err)
		}
	}
}

func TestCoordinatorActorUsesTheQualifiedLead(t *testing.T) {
	in := fixture(Claude)
	for _, suffix := range []string{":coord:one:1", "@e7:coord:feature-two:13"} {
		for _, prefix := range []string{in.Project + "-lead", in.LeadIdentity} {
			in.ExtraEnv = []EnvVar{{EnvQargiActor, prefix + suffix}}
			env := envMap(t, in)
			if env[EnvQargiActor] != env[EnvQargiLead]+suffix {
				t.Errorf("actor = %q, lead = %q", env[EnvQargiActor], env[EnvQargiLead])
			}
		}
	}
	in.LeadIdentity = strings.Repeat("p", 64)
	for _, bad := range []string{
		"other-lead:coord:one:1", "ataniq-lead:coord:one:0",
		"ataniq-lead:coord:a--b:1", "ataniq-lead:coord:" + strings.Repeat("a", 41) + ":1",
		"ataniq-lead@e1234567890:coord:" + strings.Repeat("a", 40) + ":1234567890",
	} {
		in.ExtraEnv = []EnvVar{{EnvQargiActor, bad}}
		if _, err := in.Environment(); err == nil || refusalOf(t, err).Field != EnvQargiActor {
			t.Errorf("invalid actor: %v", err)
		}
	}
	in.LeadIdentity = ""
	in.ExtraEnv = []EnvVar{{EnvQargiActor, "ataniq-lead:coord:one:1"}}
	if _, err := in.Environment(); err == nil || refusalOf(t, err).Field != EnvQargiActor {
		t.Errorf("coordinator without a parent identity: %v", err)
	}
}
