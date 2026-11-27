package main

import (
	"fmt"
	"os"
	"strings"

	"ataniq/internal/leaddaemon"
	"ataniq/internal/leadregistry"
	"golang.org/x/sys/unix"
)

// Earlier initializers could leave a private replay copy after a crash.
// Remove only its known journal entry and empty directory, under the root lock.
func (a *app) cleanRegistryChecks(root *os.File) error {
	entries, err := root.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || !strings.HasPrefix(name, ".registry-check-") {
			continue
		}
		fd, err := unix.Openat(int(root.Fd()), name, unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_RDONLY|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		err = unix.Unlinkat(fd, leadregistry.FileName, 0)
		unix.Close(fd)
		if err != nil && err != unix.ENOENT {
			return err
		}
		if err := unix.Unlinkat(int(root.Fd()), name, unix.AT_REMOVEDIR); err != nil {
			return err
		}
		if err := root.Sync(); err != nil {
			return err
		}
		fmt.Fprintf(a.stderr, "ataniqd init: removed stale registry check copy %s\n", leaddaemon.SafeText(name))
	}
	return nil
}
