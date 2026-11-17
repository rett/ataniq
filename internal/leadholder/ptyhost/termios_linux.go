//go:build linux

package ptyhost

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Termios is the part of the terminal's current settings the holder acts on
// (the lead-hosting design, section 1.2).
type Termios struct {
	// ISIG is set when the line discipline turns the signal characters into
	// signals. While it is set the holder holds those bytes back.
	ISIG bool
	// ICANON and ECHO are for the status: cooked mode has both set.
	ICANON bool
	ECHO   bool
	// VINTR, VQUIT and VSUSP are the control characters in c_cc. Zero means
	// the character is disabled.
	VINTR, VQUIT, VSUSP byte
}

// HeldBack returns the bytes the holder must not forward while ISIG is set:
// the VINTR, VQUIT and VSUSP characters, without duplicates and without a
// disabled (zero) one. With ISIG clear it returns nil, because the bytes are
// then ordinary input.
func (t Termios) HeldBack() []byte {
	if !t.ISIG {
		return nil
	}
	var out []byte
	for _, c := range []byte{t.VINTR, t.VQUIT, t.VSUSP} {
		if c == 0 {
			continue
		}
		dup := false
		for _, o := range out {
			dup = dup || o == c
		}
		if !dup {
			out = append(out, c)
		}
	}
	return out
}

// Termios reads the terminal's current settings from the master. The kernel
// reports the settings of the slave side, so this follows the child as it
// switches between raw and cooked mode.
func (h *Host) Termios() (Termios, error) {
	var t *unix.Termios
	err := h.ctl(func(fd int) error {
		var e error
		t, e = unix.IoctlGetTermios(fd, unix.TCGETS)
		return e
	})
	if err != nil {
		return Termios{}, fmt.Errorf("ptyhost: termios: %w", err)
	}
	return Termios{
		ISIG:   t.Lflag&unix.ISIG != 0,
		ICANON: t.Lflag&unix.ICANON != 0,
		ECHO:   t.Lflag&unix.ECHO != 0,
		VINTR:  t.Cc[unix.VINTR],
		VQUIT:  t.Cc[unix.VQUIT],
		VSUSP:  t.Cc[unix.VSUSP],
	}, nil
}

// LegacyTIOCSTI reads the sysctl dev.tty.legacy_tiocsti under procRoot
// (empty means "/proc"). With it set, a process on a terminal can push bytes
// into that terminal's input, which the holder must not allow (the
// lead-hosting design, section 1.1: it refuses to start unless the value is
// 0). The holder's refusal is the caller's: this only reads.
//
// known is false when the value cannot be read or is not "0" or "1" (the
// sysctl does not exist before Linux 6.2, and the ioctl is then always
// available). When known is false, enabled is reported true: the safe
// reading is that the ioctl cannot be shown to be off, so the check
// "refuse unless !enabled" needs no second branch.
func LegacyTIOCSTI(procRoot string) (enabled bool, known bool) {
	if procRoot == "" {
		procRoot = "/proc"
	}
	f, err := os.Open(filepath.Join(procRoot, "sys", "dev", "tty", "legacy_tiocsti"))
	if err != nil {
		return true, false
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 16))
	if err != nil {
		return true, false
	}
	switch strings.TrimSpace(string(b)) {
	case "0":
		return false, true
	case "1":
		return true, true
	}
	return true, false
}
