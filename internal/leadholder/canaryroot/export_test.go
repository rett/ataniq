//go:build linux && ataniq_canary

package canaryroot

// EnvCandidates exposes the environment's default folders to the external test
// package, which compares them with internal/leaddaemon/paths.go.
func EnvCandidates(getenv func(string) string) (state, runtime string, stateOK, runtimeOK bool) {
	return envCandidates(getenv)
}
