//go:build linux

package main

import (
	"bytes"
	"errors"
	"io"
	"math"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestGaps(t *testing.T) {
	max := uint(math.MaxUint32)
	for _, c := range []struct {
		keep []int
		want [][2]uint
	}{
		{nil, [][2]uint{{3, max}}},
		{[]int{3}, [][2]uint{{4, max}}},
		{[]int{4}, [][2]uint{{3, 3}, {5, max}}},
		{[]int{9, 5, 5, 3, 0, 2}, [][2]uint{{4, 4}, {6, 8}, {10, max}}},
		{[]int{1023}, [][2]uint{{3, 1022}, {1024, max}}},
	} {
		if got := gaps(c.keep); !reflect.DeepEqual(got, c.want) {
			t.Errorf("gaps(%v) = %v, want %v", c.keep, got, c.want)
		}
	}
}

func TestCloseOnExecAbove2(t *testing.T) {
	list := func() ([]int, error) { return []int{0, 1, 2, 3, 4, 7, 9}, nil }
	t.Run("close_range does it", func(t *testing.T) {
		var calls [][3]uint
		err := closeOnExecAbove2([]int{4}, func(a, b, fl uint) error { calls = append(calls, [3]uint{a, b, fl}); return nil },
			func() ([]int, error) { t.Error("listed"); return nil, nil }, func(int) error { t.Error("marked"); return nil })
		if err != nil {
			t.Fatal(err)
		}
		want := [][3]uint{{3, 3, closeRangeCloexec}, {5, math.MaxUint32, closeRangeCloexec}}
		if !reflect.DeepEqual(calls, want) {
			t.Errorf("calls %v, want %v", calls, want)
		}
	})
	t.Run("fallback marks each listed descriptor above 2 but the kept ones", func(t *testing.T) {
		var marked []int
		err := closeOnExecAbove2([]int{4}, func(a, b, fl uint) error { return unix.ENOSYS }, list,
			func(fd int) error {
				marked = append(marked, fd)
				if fd == 7 {
					return unix.EBADF // closed meanwhile: not an error
				}
				return nil
			})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(marked, []int{3, 7, 9}) {
			t.Errorf("marked %v", marked)
		}
	})
	t.Run("fallback fails on a real error", func(t *testing.T) {
		err := closeOnExecAbove2(nil, func(a, b, fl uint) error { return unix.EINVAL }, list, func(int) error { return unix.EPERM })
		if err == nil || !strings.Contains(err.Error(), "descriptor 3") {
			t.Errorf("got %v", err)
		}
	})
	t.Run("nothing works", func(t *testing.T) {
		err := closeOnExecAbove2(nil, func(a, b, fl uint) error { return unix.ENOSYS }, func() ([]int, error) { return nil, errors.New("no /proc") }, nil)
		if err == nil || !strings.Contains(err.Error(), "close_range") || !strings.Contains(err.Error(), "no /proc") {
			t.Errorf("got %v", err)
		}
	})
}

func TestWriteAll(t *testing.T) {
	var got bytes.Buffer
	step := 0
	err := writeAll(func(p []byte) (int, error) {
		step++
		switch step {
		case 1:
			return 0, unix.EINTR
		case 2:
			got.Write(p[:3])
			return 3, nil // a partial write is continued
		}
		got.Write(p)
		return len(p), nil
	}, []byte("0123456789"))
	if err != nil || got.String() != "0123456789" {
		t.Errorf("got %q, %v", got.String(), err)
	}
	if err := writeAll(func(p []byte) (int, error) { return 0, nil }, []byte("x")); !errors.Is(err, io.ErrShortWrite) {
		t.Errorf("no progress: %v", err)
	}
	if err := writeAll(func(p []byte) (int, error) { return 1, unix.EPIPE }, []byte("xy")); !errors.Is(err, unix.EPIPE) {
		t.Errorf("failed write: %v", err)
	}
	if err := writeAll(func(p []byte) (int, error) { t.Error("called"); return 0, nil }, nil); err != nil {
		t.Errorf("nothing to write: %v", err)
	}
}

func TestSetCloexecAndList(t *testing.T) {
	// A descriptor far above the others, marked by the real call.
	fd, err := unix.Open("/dev/null", unix.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	hi, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD, 500)
	if err != nil {
		t.Skipf("no high descriptor: %v", err)
	}
	defer unix.Close(hi)
	// Only the one descriptor is marked, through the fallback's own call, so
	// that the test does not mark the descriptors of the test process.
	if err := setCloexec(hi); err != nil {
		t.Fatal(err)
	}
	if fl, _ := unix.FcntlInt(uintptr(hi), unix.F_GETFD, 0); fl&unix.FD_CLOEXEC == 0 {
		t.Error("not close-on-exec")
	}
	if err := setCloexec(1019); !errors.Is(err, unix.EBADF) {
		t.Errorf("a closed descriptor: %v", err)
	}
	fds, err := hostListFDs()
	if err != nil || !containsInt(fds, hi) {
		t.Errorf("listing %v %v", fds, err)
	}
}

func containsInt(s []int, n int) bool {
	for _, v := range s {
		if v == n {
			return true
		}
	}
	return false
}
