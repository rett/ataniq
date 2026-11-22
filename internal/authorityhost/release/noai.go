package release

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// NoAICheckTimeout is how long the shipped no-AI check may run.
const NoAICheckTimeout = 60 * time.Second

// maxNoAIOutput bounds what is read of the check's output.
const maxNoAIOutput = 4 << 20

// runNoAICheck runs the release's own bin/ah-noaicheck on the release in
// its first phase, with the flags a release check uses (see the README, "The
// switch", step 4), as a child of its own process group with the switch's
// fixed environment, the folder / and no standard input. It adds a finding
// when the program does not start, does not finish in the timeout, exits
// non-zero or prints any line that starts "HIT ". Nothing it prints is shown.
func (c *checker) runNoAICheck(dir string, o CheckOptions) {
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = NoAICheckTimeout
	}
	args := []string{"--phase", "pre", "--release", dir}
	if o.Root != "" {
		args = append(args, "--root", o.Root)
	}
	if o.RequirePolicy {
		args = append(args, "--require-policy")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	prog := filepath.Join(dir, "bin", "ah-noaicheck")
	cmd := exec.CommandContext(ctx, prog, args...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	cmd.Dir = "/"
	cmd.SysProcAttr = procAttr()
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		c.add(CodeNoAIStart, "")
		return
	}
	if err := cmd.Start(); err != nil {
		c.add(CodeNoAIStart, "")
		return
	}
	hits := 0
	sc := bufio.NewScanner(io.LimitReader(stdout, maxNoAIOutput))
	sc.Buffer(make([]byte, 0, 4096), 1<<20)
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "HIT ") {
			hits++
		}
	}
	if sc.Err() != nil {
		// A line too long to read is output no check prints: not a pass.
		c.add(CodeNoAIExit, "")
	}
	// What was not read is drained so the program never blocks on a full pipe.
	io.Copy(io.Discard, stdout)
	werr := cmd.Wait()
	var ee *exec.ExitError
	switch {
	case ctx.Err() != nil:
		c.add(CodeNoAITimeout, "")
	case werr != nil && !errors.As(werr, &ee):
		c.add(CodeNoAIStart, "")
	case werr != nil:
		c.add(CodeNoAIExit, "")
	}
	if hits > 0 {
		c.add(CodeNoAIHit, "")
	}
}
