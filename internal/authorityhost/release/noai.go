package release

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
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

// beforeNoAIStart, when not nil, is called after the private copy of the
// checker is made and verified and before it is started. It is a hook for
// tests.
var beforeNoAIStart func()

// runNoAICheck runs the release's own bin/ah-noaicheck on the release dir
// (an absolute path) in its first phase, with the flags a release check uses
// (see the README, "The switch", step 4), as a child of its own process
// group with the switch's fixed environment, the folder / and no standard
// input. What runs is a private copy of the checker Check verified (see
// privateCopy), started as the descriptor that was hashed, not as a path. It
// is given the absolute path of --root, as of the release, since it runs
// from the folder /. It adds a finding
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
		// The checker runs from the folder /, so a root relative to the
		// caller's folder would name another tree there.
		abs, err := filepath.Abs(o.Root)
		if err != nil {
			c.add(CodeNoAIStart, "")
			return
		}
		args = append(args, "--root", abs)
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
	defer prog.Close()
	if beforeNoAIStart != nil {
		beforeNoAIStart()
	}
	// The copy is started as the descriptor Check hashed, the child's
	// descriptor 3, not as the path of its folder, which may name another
	// file by now.
	cmd := exec.CommandContext(ctx, "/proc/self/fd/3", args...)
	cmd.ExtraFiles = []*os.File{prog}
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
// folder of mode 0700 in the temporary folder (made absolute, so a relative
// TMPDIR names the same folder wherever the check runs), the copy of mode
// 0700, and returns the copy opened for reading and what removes it and its
// folder. The bytes are read through root, the descriptor of the folder
// Check verified, hashed as they are copied and hashed again from the copy,
// each time against the SHA-256 Check found: a file that changed since is
// noaicheck-changed, a copy that cannot be made noaicheck-not-started, and
// nothing is run (ok false).
//
// The copy is made, opened again and removed through the descriptor of its
// folder, and the hash of the second read is the one of the descriptor that
// is then started (see runNoAICheck), so an account that renames the folder
// or an entry above it cannot make what runs another file than what was
// hashed: the path of the folder is resolved once, when it is opened. The
// file opened again must be the copy's own kind: a regular file of this
// user's that no one else can write (ownPrivate), so a file put in its place
// is not run; the check then does not start.
func (c *checker) privateCopy(root *os.Root, p string) (prog *os.File, done func(), ok bool) {
	want, known := c.digests[p]
	f, info, err := openRegular(root, p)
	if err != nil || !known {
		c.add(CodeNoAIChanged, "")
		return nil, nil, false
	}
	defer f.Close()
	parent, err := filepath.Abs(os.TempDir())
	if err != nil {
		c.add(CodeNoAIStart, "")
		return nil, nil, false
	}
	tmp, err := os.MkdirTemp(parent, "ah-release-check-")
	if err != nil {
		c.add(CodeNoAIStart, "")
		return nil, nil, false
	}
	dir, err := os.OpenRoot(tmp)
	if err != nil {
		os.Remove(tmp)
		c.add(CodeNoAIStart, "")
		return nil, nil, false
	}
	name := path.Base(p)
	done = func() {
		// Only the copy is removed, through the folder's descriptor; the
		// folder by its path, which removes it only when it is empty.
		dir.Remove(name)
		dir.Close()
		os.Remove(tmp)
	}
	fail := func(code string) (*os.File, func(), bool) {
		done()
		c.add(code, "")
		return nil, nil, false
	}
	out, err := dir.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o700)
	if err != nil {
		return fail(CodeNoAIStart)
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
		return fail(CodeNoAIStart)
	}
	prog, err = dir.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return fail(CodeNoAIStart)
	}
	if st, err := prog.Stat(); err != nil || !ownPrivate(st) {
		prog.Close()
		return fail(CodeNoAIStart)
	}
	again := sha256.New()
	if _, err := io.Copy(again, prog); err != nil {
		prog.Close()
		return fail(CodeNoAIStart)
	}
	if n != info.Size() || hex.EncodeToString(h.Sum(nil)) != want || hex.EncodeToString(again.Sum(nil)) != want {
		prog.Close()
		return fail(CodeNoAIChanged)
	}
	return prog, done, true
}

// ownPrivate reports whether st is a regular file of this user that group
// and others cannot write.
func ownPrivate(st fs.FileInfo) bool {
	sys, ok := st.Sys().(*syscall.Stat_t)
	return ok && st.Mode().IsRegular() && st.Mode().Perm()&0o022 == 0 && sys.Uid == uint32(os.Geteuid())
}
