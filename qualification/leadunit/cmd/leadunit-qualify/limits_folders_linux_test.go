//go:build linux

package main

import (
	"context"
	"io/fs"
	"path/filepath"
	"testing"
)

// noUserControl is a host on which the user manager never created
// systemd/user.control, which is likely where a transient unit keeps its
// settings in systemd/transient.
type noUserControl struct{ *fakeHost }

func (a noUserControl) ReadDir(path string) ([]string, error) {
	if path == filepath.Join(testRuntimeDir, "systemd/user.control") {
		return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
	}
	return a.fakeHost.ReadDir(path)
}

// One runtime folder being absent is not a failure when the other holds the
// unit and its drop-in.
func TestRuntimeFoldersWithoutUserControl(t *testing.T) {
	r, h := newLimitRig(t)
	r.q.host = noUserControl{h}
	rep := r.q.qualify(context.Background())
	if rep.Verdict != resultPass {
		t.Fatalf("%+v", rep)
	}
	res := results(rep)
	if res["runtime-folders-hold-the-unit"] != resultPass || res["runtime-folders-clean-after-stop"] != resultPass {
		t.Fatalf("%+v", rep)
	}
}
