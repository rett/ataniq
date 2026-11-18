package gitenv

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/sys/unix"
)

// runOnTerminal starts argv with its standard output on a pseudo-terminal,
// which is what makes git start a pager. It returns the error output and the
// run error. What the command writes to the terminal is discarded.
func runOnTerminal(argv, env []string, dir string) (string, error) {
	master, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", fmt.Errorf("open /dev/ptmx: %w", err)
	}
	masterFile := os.NewFile(uintptr(master), "ptmx")
	defer masterFile.Close()
	if err := unix.IoctlSetPointerInt(master, unix.TIOCSPTLCK, 0); err != nil {
		return "", fmt.Errorf("unlock the pseudo-terminal: %w", err)
	}
	n, err := unix.IoctlGetInt(master, unix.TIOCGPTN)
	if err != nil {
		return "", fmt.Errorf("name the pseudo-terminal: %w", err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		return "", fmt.Errorf("open the pseudo-terminal: %w", err)
	}
	defer slave.Close()

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = env
	cmd.Dir = dir
	cmd.Stdout = slave
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err = cmd.Run()
	return stderr.String(), err
}
