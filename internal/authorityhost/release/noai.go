package release

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// NoAICheckTimeout is how long the shipped no-AI check may run.
const NoAICheckTimeout = 60 * time.Second

// maxNoAIOutput bounds what is read of the check's output.
const maxNoAIOutput = 4 << 20

// runNoAICheck runs the release's own bin/ah-noaicheck on the release dir
// (an absolute path) in its first phase, with the flags a release check uses
// (see the README, "The switch", step 4), as a child of its own process
// group with the switch's fixed environment, the folder / and no standard
// input. What runs is a private copy of the checker Check verified (see
// privateCopy), not the file the folder's path names now. It adds a finding
// when the program does not start, does not finish in the timeout, exits
// non-zero or prints any line that starts "HIT ". Nothing it prints is shown.
//
// The checker reads the release's etc/ and share/ by dir, its path: those
// are read as they are when it runs, not as Check verified them.
func (c *checker) runNoAICheck(root *os.Root, dir string, o CheckOptions) {
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
	prog, done, ok := c.privateCopy(root, "bin/ah-noaicheck")
	if !ok {
		return
	}
	defer done()
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

// privateCopy copies the file p of root, a file Check verified, into a new
// folder of mode 0700 in the temporary folder, the copy of mode 0700, and
// returns the copy's path and what removes the folder. The bytes are read
// through root, the descriptor of the folder Check verified, hashed as they
// are copied and hashed again from the copy, each time against the SHA-256
// Check found: a file that changed since is noaicheck-changed, a copy that
// cannot be made noaicheck-not-started, and nothing is run (ok false).
func (c *checker) privateCopy(root *os.Root, p string) (copyPath string, done func(), ok bool) {
	want, known := c.digests[p]
	f, info, err := openRegular(root, p)
	if err != nil || !known {
		c.add(CodeNoAIChanged, "")
		return "", nil, false
	}
	defer f.Close()
	tmp, err := os.MkdirTemp("", "ah-release-check-")
	if err != nil {
		c.add(CodeNoAIStart, "")
		return "", nil, false
	}
	done = func() { os.RemoveAll(tmp) }
	copyPath = filepath.Join(tmp, path.Base(p))
	out, err := os.OpenFile(copyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o700)
	if err != nil {
		done()
		c.add(CodeNoAIStart, "")
		return "", nil, false
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(f, info.Size()+1))
	if cerr := out.Chmod(0o700); err == nil {
		err = cerr
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		done()
		c.add(CodeNoAIStart, "")
		return "", nil, false
	}
	if n != info.Size() || hex.EncodeToString(h.Sum(nil)) != want || hashPath(copyPath) != want {
		done()
		c.add(CodeNoAIChanged, "")
		return "", nil, false
	}
	return copyPath, done, true
}

// hashPath is the SHA-256 of the file p, or "" when it cannot be read.
func hashPath(p string) string {
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}
