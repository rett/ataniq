//go:build linux && ataniq_canary

package canaryroot_test

import (
	"fmt"
	"testing"

	"ataniq/internal/leaddaemon"
	"ataniq/internal/leadholder/canaryroot"
)

// The candidates the root check takes from the environment must be the
// folders the daemon itself would use, for any environment. If paths.go
// changes its rule, this test fails and the check must follow.
func TestEnvCandidatesAgreeWithDaemonDefaults(t *testing.T) {
	envs := []map[string]string{
		{},
		{"HOME": "/h"},
		{"XDG_STATE_HOME": "/s"},
		{"HOME": "/h", "XDG_STATE_HOME": "/s"},
		{"HOME": "/h", "XDG_STATE_HOME": "s"},
		{"HOME": "/h", "XDG_STATE_HOME": ""},
		{"HOME": "h"},
		{"HOME": ""},
		{"HOME": "h", "XDG_STATE_HOME": "/s"},
		{"XDG_RUNTIME_DIR": "/r"},
		{"XDG_RUNTIME_DIR": "r"},
		{"XDG_RUNTIME_DIR": ""},
		{"HOME": "/h", "XDG_STATE_HOME": "/s", "XDG_RUNTIME_DIR": "/r"},
		{"HOME": "/h/", "XDG_STATE_HOME": "/s/../t", "XDG_RUNTIME_DIR": "/r//u/"},
		{"HOME": "/h/../k"},
		{"XDG_STATE_HOME": "relative", "XDG_RUNTIME_DIR": "relative"},
		{"PATH": "/bin", "USER": "someone"},
	}
	for _, m := range envs {
		m := m
		t.Run(fmt.Sprint(m), func(t *testing.T) {
			get := func(k string) string { return m[k] }
			wantState, stateErr := leaddaemon.DefaultStateDir(get)
			wantRuntime, runtimeErr := leaddaemon.DefaultRuntimeDir(get)
			gotState, gotRuntime, stateOK, runtimeOK := canaryroot.EnvCandidates(get)
			if (stateErr == nil) != stateOK {
				t.Fatalf("state: daemon error %v, candidate ok %v", stateErr, stateOK)
			}
			if stateOK && gotState != wantState {
				t.Errorf("state candidate %q, daemon %q", gotState, wantState)
			}
			if (runtimeErr == nil) != runtimeOK {
				t.Fatalf("runtime: daemon error %v, candidate ok %v", runtimeErr, runtimeOK)
			}
			if runtimeOK && gotRuntime != wantRuntime {
				t.Errorf("runtime candidate %q, daemon %q", gotRuntime, wantRuntime)
			}
		})
	}
}

func TestEnvCandidatesNilGetenv(t *testing.T) {
	if _, _, s, r := canaryroot.EnvCandidates(nil); s || r {
		t.Fatalf("a nil getenv gave candidates: state %v runtime %v", s, r)
	}
}
