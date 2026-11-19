//go:build linux

package main

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"ataniq/internal/leadunit/systemdmgr"
)

// cgroupRoot is where the unified control group hierarchy is mounted.
const cgroupRoot = "/sys/fs/cgroup"

// realLife is the real manager together with the host access of the limit
// checks: systemctl through the manager's own runner (the tool resolved from
// the fixed directories, the user manager's environment, a bounded output and
// a deadline), and the files of the machine. Only realManager builds it, and no
// test does.
type realLife struct {
	*systemdmgr.Manager
	runner *systemdmgr.ExecRunner
}

var _ host = realLife{}

func (l realLife) Run(ctx context.Context, argv []string) (string, error) {
	if len(argv) == 0 || argv[0] != "systemctl" {
		return "", fmt.Errorf("the harness runs only systemctl, not %q", argv)
	}
	return l.runner.Run(ctx, argv)
}

func (realLife) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

func (realLife) ReadDir(path string) ([]string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	return names, nil
}

// userRuntimeDir is the runtime folder of the calling user's manager:
// $XDG_RUNTIME_DIR when it is a clean absolute path, else /run/user/<uid>.
func userRuntimeDir() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" && d[0] == '/' && len(d) > 1 && d[len(d)-1] != '/' {
		return d
	}
	return "/run/user/" + strconv.Itoa(os.Getuid())
}
