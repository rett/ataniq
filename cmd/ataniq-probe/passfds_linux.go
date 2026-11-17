//go:build linux

package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// passDeps is what the pass-through mode needs from the world.
type passDeps struct {
	exec        execFunc
	environ     func() []string
	closeAbove2 func(keep []int) error
}

func hostPassDeps() passDeps {
	return passDeps{exec: unix.Exec, environ: os.Environ, closeAbove2: hostCloseOnExecAbove2}
}

// runPassFDs is the pass-through mode. It opens each file, checks its type,
// places each at exactly its number without close-on-exec, makes every other
// descriptor above 2 close-on-exec (so the execve closes it) and executes the
// command with the environment unchanged. Any error is exit 98 with one line,
// and nothing is executed.
func runPassFDs(args []string, stderr io.Writer, d passDeps) int {
	a, err := parsePassFDs(args)
	if err != nil {
		return fail(stderr, exitSetup, "usage: pass-fds: %v", err)
	}
	files, err := openFiles(a.Files)
	if err != nil {
		return fail(stderr, exitSetup, "pass-fds: %v", err)
	}
	if err := placeFiles(files); err != nil {
		return fail(stderr, exitSetup, "pass-fds: %v", err)
	}
	keep := make([]int, len(files))
	for i, f := range files {
		keep[i] = f.target
	}
	return execProgram("command", a.Command, d.environ(), stderr, d.exec, func() error { return d.closeAbove2(keep) })
}

// openedFile is a file opened at fd that is to be placed at target.
type openedFile struct {
	fd, target int
	desc       string
}

// openFiles opens the files, in order, all close-on-exec, and checks what each
// is. A --read file is opened read-only, without following a link at the last
// component, without blocking (a FIFO would otherwise block the open) and
// without becoming a controlling terminal; it must be a regular file; the
// non-blocking flag is cleared afterwards. A --write file is opened
// write-only, without creating or truncating it, appending, without following
// a link and without becoming a controlling terminal; it must be a regular
// file or a FIFO. A write-only open of a FIFO blocks until a process has the
// reading end open: the holder opens its end first. On an error every file
// opened so far is closed.
func openFiles(files []fdFile) ([]openedFile, error) {
	var out []openedFile
	for _, f := range files {
		o, err := openOne(f)
		if err != nil {
			for _, p := range out {
				unix.Close(p.fd)
			}
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}

func openOne(f fdFile) (openedFile, error) {
	opt, flags := optRead, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NOCTTY|unix.O_NONBLOCK
	if f.Write {
		opt, flags = optWrite, unix.O_WRONLY|unix.O_APPEND|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NOCTTY
		// Look before opening: opening a device or a socket for writing is
		// not a check. The type of the opened file is checked again below.
		var st unix.Stat_t
		if err := unix.Lstat(f.Path, &st); err != nil {
			return openedFile{}, fmt.Errorf("%s %d=%s: %w", opt, f.FD, quote(f.Path), describeOpenError(err))
		}
		if err := writeType(st.Mode); err != nil {
			return openedFile{}, fmt.Errorf("%s %d=%s: %v", opt, f.FD, quote(f.Path), err)
		}
	}
	fd, err := openRetry(f.Path, flags)
	if err != nil {
		return openedFile{}, fmt.Errorf("%s %d=%s: %w", opt, f.FD, quote(f.Path), describeOpenError(err))
	}
	o := openedFile{fd: fd, target: f.FD, desc: fmt.Sprintf("%s %d=%s", opt, f.FD, quote(f.Path))}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return openedFile{}, fmt.Errorf("%s: %w", o.desc, err)
	}
	if f.Write {
		err = writeType(st.Mode)
	} else if st.Mode&unix.S_IFMT != unix.S_IFREG {
		err = errors.New("it is not a regular file")
	}
	if err != nil {
		unix.Close(fd)
		return openedFile{}, fmt.Errorf("%s: %v", o.desc, err)
	}
	if !f.Write {
		fl, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
		if err == nil {
			_, err = unix.FcntlInt(uintptr(fd), unix.F_SETFL, fl&^unix.O_NONBLOCK)
		}
		if err != nil {
			unix.Close(fd)
			return openedFile{}, fmt.Errorf("%s: cannot clear O_NONBLOCK: %w", o.desc, err)
		}
	}
	return o, nil
}

// writeType says a --write target may be a regular file or a FIFO.
func writeType(mode uint32) error {
	switch mode & unix.S_IFMT {
	case unix.S_IFREG, unix.S_IFIFO:
		return nil
	}
	return errors.New("it is neither a regular file nor a FIFO")
}

// describeOpenError names a refusal of a link: with O_NOFOLLOW the kernel says
// ELOOP, which reads as a mystery in a log.
func describeOpenError(err error) error {
	if errors.Is(err, unix.ELOOP) {
		return errors.New("it is a symbolic link (not followed)")
	}
	return err
}

// placeFiles puts each opened file at exactly its target number, without
// close-on-exec. An opened file that sits at the target of another is first
// moved to a number above every target (a number nothing else can want), so
// that no placement overwrites a file that is still to be placed; a file that
// is already at its target only loses close-on-exec. An inherited descriptor
// at a target is replaced. Every moved or duplicated number is closed.
func placeFiles(files []openedFile) error {
	targets := map[int]bool{}
	highest := 0
	for _, f := range files {
		targets[f.target] = true
		highest = max(highest, f.target)
	}
	for i := range files {
		f := &files[i]
		if f.fd == f.target || !targets[f.fd] {
			continue
		}
		moved, err := unix.FcntlInt(uintptr(f.fd), unix.F_DUPFD_CLOEXEC, highest+1)
		if err != nil {
			return fmt.Errorf("cannot move descriptor %d out of the way: %w", f.fd, err)
		}
		unix.Close(f.fd)
		f.fd = moved
	}
	for _, f := range files {
		if f.fd == f.target {
			if _, err := unix.FcntlInt(uintptr(f.fd), unix.F_SETFD, 0); err != nil {
				return fmt.Errorf("%s: cannot clear close-on-exec on descriptor %d: %w", f.desc, f.target, err)
			}
			continue
		}
		if err := unix.Dup3(f.fd, f.target, 0); err != nil {
			return fmt.Errorf("%s: cannot place it at descriptor %d: %w", f.desc, f.target, err)
		}
		unix.Close(f.fd)
	}
	return nil
}
