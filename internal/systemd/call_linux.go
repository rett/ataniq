package systemd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// MaxCall is the longest a command call may run, whatever deadline the caller
// gives. A call ends one second after its deadline at the latest, which is the
// grace given to the command's output pipes.
const MaxCall = 5 * time.Second

// ErrOutputExcessive is returned by Run, with no output, when the command
// wrote more than the cap.
var ErrOutputExcessive = errors.New("command output excessive")

// cappedBuffer collects output up to max bytes. It does not embed bytes.Buffer:
// that would promote ReadFrom, which io.Copy prefers over Write, and the cap
// would never be checked.
type cappedBuffer struct {
	buf      bytes.Buffer
	max      int
	overflow bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len()+len(p) > b.max {
		b.overflow = true
		return 0, errors.New("command output cap")
	}
	return b.buf.Write(p)
}

// Run runs the executable at path with args, with exactly the environment env
// and no shell, and returns its combined standard output and error. Callers
// pass an absolute path. The call is cut off at deadline or after MaxCall, whichever is first.
// When the command writes more than max bytes Run returns ErrOutputExcessive
// and no output. A returned error does not mean the command had no effect: if
// it is a start request the caller inspects the unit and never retries. Run
// does not check what the executable is; the caller is trusted to have
// established that.
func Run(path string, args []string, deadline time.Time, env []string, max int) (string, error) {
	if limit := time.Now().Add(MaxCall); limit.Before(deadline) {
		deadline = limit
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = env
	cmd.WaitDelay = time.Second
	out := cappedBuffer{max: max}
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if out.overflow {
		return "", ErrOutputExcessive
	}
	return out.buf.String(), err
}

// ProcessEnvironment is the environment for a call that uses the caller's own
// manager connection: a fixed locale and the HOME, XDG_RUNTIME_DIR and
// DBUS_SESSION_BUS_ADDRESS the calling process has now, and nothing else.
func ProcessEnvironment() []string {
	return []string{"LANG=C", "LC_ALL=C", "HOME=" + os.Getenv("HOME"), "XDG_RUNTIME_DIR=" + os.Getenv("XDG_RUNTIME_DIR"), "DBUS_SESSION_BUS_ADDRESS=" + os.Getenv("DBUS_SESSION_BUS_ADDRESS")}
}

// UserManagerEnvironment is the environment for a call that must reach this
// user's own systemd user manager and nothing the caller inherited: the
// runtime directory and bus socket are derived from the real user id, so a
// private bus address in the caller's environment is never forwarded.
func UserManagerEnvironment() []string {
	runtime := "/run/user/" + strconv.Itoa(os.Getuid())
	return []string{"LANG=C", "LC_ALL=C", "HOME=" + os.Getenv("HOME"), "XDG_RUNTIME_DIR=" + runtime, "DBUS_SESSION_BUS_ADDRESS=unix:path=" + runtime + "/bus"}
}
