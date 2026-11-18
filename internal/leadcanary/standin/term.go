//go:build linux && ataniq_canary

package standin

import "golang.org/x/sys/unix"

// MakeRaw puts the terminal on fd in raw mode: no echo, no line editing, no
// signal keys, no output processing, one byte at a time. The function it
// returns puts the settings back as they were.
func MakeRaw(fd int) (restore func() error, err error) {
	old, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return nil, err
	}
	raw := *old
	raw.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	raw.Oflag &^= unix.OPOST
	raw.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	raw.Cflag &^= unix.CSIZE | unix.PARENB
	raw.Cflag |= unix.CS8
	raw.Cc[unix.VMIN] = 1
	raw.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &raw); err != nil {
		return nil, err
	}
	return func() error { return unix.IoctlSetTermios(fd, unix.TCSETS, old) }, nil
}
