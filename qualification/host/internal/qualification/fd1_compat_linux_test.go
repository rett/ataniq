//go:build linux && amd64

package qualification

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func capturedFD1LegacyPlan(role, policy string) *Attempt {
	c := HostConfig{Version: 1, Campaign: "/owned/campaign", Driver: "/owned/driver", Probe: "/owned/probe", Bwrap: "/owned/bwrap", BPF: "/owned/bpf", Systemctl: "/owned/systemctl", SystemdRun: "/owned/systemd-run", Role: role, RoleID: "0011223344556677", RunID: "0123456789abcdef", RequestID: "fedcba9876543210", Case: "inventory", ReviewedBPF: true}
	a := &Attempt{ID: strings.Repeat("a", 32), Config: c, Profile: HostProfile, DescriptorPolicy: policy, Boot: "00000000-0000-0000-0000-000000000000", FixtureNonce: strings.Repeat("e", 32), Subjects: map[string]Subject{}, Snapshots: map[string]Subject{}, Streams: map[string]FileIdentity{"request": {1, 31, 1000}, "output": {1, 32, 1000}, "stderr": {1, 33, 1000}}}
	path := filepath.Join(c.Campaign, a.ID)
	for j, n := range []string{"driver", "probe", "bwrap", "bpf", "systemctl", "systemd-run"} {
		s := Subject{Path: selected(c)[n], Identity: FileIdentity{1, uint64(j + 10), 1000}, SHA256: syntheticDigest([]byte(n))}
		a.Subjects[n] = s
		if j < 4 {
			s.Path = filepath.Join(path, n)
			s.Identity.Inode += 100
			a.Snapshots[n] = s
		}
	}
	a.Fixture = Subject{Path: filepath.Join(path, "fixtures", "fake-file"), Identity: FileIdentity{1, 50, 1000}, SHA256: syntheticDigest(fixtureBytes(a.FixtureNonce))}
	a.Request = hostRequest(a)
	a.Command = command(a, path)
	a.Launch = launch(a, path)
	a.Cleanup = []string{"stop aq" + a.ID + ".service", "stop aq" + a.ID + ".slice"}
	if policy != "" {
		a.TargetCommand = targetCommand(a, path)
	}
	return a
}

// Values were captured by executing the accepted 1235fa1 implementation in an
// isolated local checkout, before applying this integration. They are not a
// second implementation of the new hash or command builder.
func TestFD1CapturedLegacyEncodings(t *testing.T) {
	for _, tc := range []struct{ role, policy, plan, injection, receipt string }{
		{"direct", "", "d41ced0ce57323e5a21678840251e1e0f2f29eb6b5c3e46820728537fd8c6074", "7492c34d08a1ad8d9bcb17dda1daf4322ce3af5fc68af6e03017d4e5cd07c410", ""},
		{"candidate", "", "4b4a04070dee09ff41b718fa59f342447b7cfd60840803ff1359fb185632c48c", "7492c34d08a1ad8d9bcb17dda1daf4322ce3af5fc68af6e03017d4e5cd07c410", ""},
		{"direct", FinalDescriptorPolicy, "c7070da4a0655d0f58149fb9800053fe4f82950facee65be841c8ef05eccd16b", "5f8de72d64981350b5f268363aa3c817c0deeedc0578e9d09ae6b65105364169", "80a59c6e9c0374b5276626fc0c8740d9aa7ffd4a45581bb7d982a90e02c2d3e0"},
		{"candidate", FinalDescriptorPolicy, "a3d8eac54b9c81f8ed6339219127ad15578930d63717c7b7a5d9f84e4c419f74", "5f8de72d64981350b5f268363aa3c817c0deeedc0578e9d09ae6b65105364169", "9f94afecc7075393ba15ead1ecfdceda11769b9f2aa29b95ee116c671658027d"},
	} {
		a := capturedFD1LegacyPlan(tc.role, tc.policy)
		if got := planDigest(a); got != tc.plan {
			t.Fatal("captured plan changed", tc.role, tc.policy, got)
		}
		wantConfig := `{"version":1,"campaign":"/owned/campaign","driver":"/owned/driver","probe":"/owned/probe","bwrap":"/owned/bwrap","bpf":"/owned/bpf","systemctl":"/owned/systemctl","systemd_run":"/owned/systemd-run","role":"direct","role_id":"0011223344556677","run_id":"0123456789abcdef","request_id":"fedcba9876543210","case":"inventory","reviewed_bpf":true}`
		if tc.role == "candidate" {
			wantConfig = strings.Replace(wantConfig, `"role":"direct"`, `"role":"candidate"`, 1)
		}
		b, _ := json.Marshal(a.Config)
		if string(b) != wantConfig {
			t.Fatal("old config encoding changed", string(b))
		}
		b, _ = json.Marshal(a.Request)
		if string(b) != `{"version":1,"run_id":"0123456789abcdef","request_id":"fedcba9876543210","case":"inventory","max_duration_ms":1500,"max_bytes":4096}` {
			t.Fatal("old request encoding changed", string(b))
		}
		i := Injection{Version: 1, Attempt: a.ID, Invocation: strings.Repeat("b", 32), Process: fixtureProcess(100, a.Snapshots["driver"].Identity, "/owned/service"), Boundary: Boundary{"/owned/service", FileIdentity{1, 90, 1000}}, Fixture: a.Fixture, Descriptors: expectedDescriptors(a), Profile: a.Profile, RunID: a.Config.RunID, RequestID: a.Config.RequestID, RoleID: a.Config.RoleID, DescriptorPolicy: tc.policy}
		a.Injector = &i
		a.PlanSHA256 = tc.plan
		b, _ = json.Marshal(i)
		if syntheticDigest(b) != tc.injection {
			t.Fatal("old injection encoding changed", string(b))
		}
		if tc.policy != "" {
			b, _ = json.Marshal(descriptorReceipt(a))
			if syntheticDigest(b) != tc.receipt {
				t.Fatal("old receipt encoding changed", string(b))
			}
		}
		b, _ = json.Marshal(a)
		if strings.Contains(string(b), "fd1_static") || strings.Contains(string(b), "fd1_runtime") || strings.Contains(string(b), "descriptor_profile") || strings.Contains(string(b), "direct_created") {
			t.Fatal("typed-only fields serialized for historical record")
		}
	}
}
