//go:build linux && ataniq_canary

package sandboxstart

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadsandbox/assert"
)

// The bounds of a version question.
const (
	versionTimeout   = 10 * time.Second
	maxVersionBytes  = 4096
	versionWaitDelay = time.Second
)

// ToolVersions implements System. The kernel release comes from the uname(2)
// call, not from a program found in PATH; each tool is asked once for
// "--version", by its absolute path.
func (s *realSystem) ToolVersions(ctx context.Context, tools ToolPaths) ToolVersions {
	var v ToolVersions
	var u unix.Utsname
	if err := unix.Uname(&u); err != nil {
		v.Errors = append(v.Errors, "kernel release: "+err.Error())
	} else {
		v.Kernel = assert.EscapeDetail(unix.ByteSliceToString(u.Release[:]))
	}
	run := s.cfg.RunVersion
	if run == nil {
		run = runVersion
	}
	for _, t := range []struct {
		name, path string
		dst        *string
	}{{"bwrap", tools.Bwrap, &v.Bwrap}, {"pasta", tools.Pasta, &v.Pasta}} {
		out, err := run(ctx, t.path)
		*t.dst = assert.EscapeDetail(out)
		if err != nil {
			v.Errors = append(v.Errors, t.name+" --version: "+assert.EscapeDetail(err.Error()))
		}
	}
	return v
}

// runVersion runs the tool once with "--version", with an empty environment, no
// input and a time limit, and returns the first line it printed.
func runVersion(ctx context.Context, path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("not an absolute path")
	}
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Env = []string{}
	cmd.Dir = "/"
	// The output is read through a pipe, which a child of the tool can keep
	// open: without a delay Run would wait for it for good, past the limit.
	cmd.WaitDelay = versionWaitDelay
	buf := &boundedBuffer{max: maxVersionBytes}
	cmd.Stdout, cmd.Stderr = buf, buf
	err := cmd.Run()
	switch {
	case err == nil:
		return firstLine(buf.bytes()), nil
	case errors.Is(err, exec.ErrWaitDelay):
		// The tool ended well and printed its version, but something it started
		// kept the output open: the version is a fact, and so is this.
		return firstLine(buf.bytes()), err
	}
	return "", fmt.Errorf("run %s: %w", filepath.Base(path), err)
}

// boundedBuffer keeps the first max bytes written to it and drops the rest.
type boundedBuffer struct {
	mu   sync.Mutex
	max  int
	data []byte
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := b.max - len(b.data); room > 0 {
		b.data = append(b.data, p[:min(len(p), room)]...)
	}
	return len(p), nil
}

func (b *boundedBuffer) bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.data...)
}

func firstLine(b []byte) string {
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		b = b[:i]
	}
	return string(b)
}
