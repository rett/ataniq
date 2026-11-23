package webpush

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestKeyFileWriteAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vapid.pem")
	k, _ := GenerateVAPIDKey()
	if err := WriteVAPIDKeyFile(path, k); err != nil {
		t.Fatal(err)
	}
	st, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 || !st.Mode().IsRegular() {
		t.Fatalf("mode %v", st.Mode())
	}
	back, err := LoadVAPIDKeyFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back.PublicKey(), k.PublicKey()) {
		t.Fatal("loaded key differs")
	}

	// Never overwrites, and a link in the file's place is not followed.
	other, _ := GenerateVAPIDKey()
	if err := WriteVAPIDKeyFile(path, other); !errors.Is(err, ErrExists) {
		t.Fatalf("second write: %v", err)
	}
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link.pem")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := WriteVAPIDKeyFile(link, other); !errors.Is(err, ErrExists) {
		t.Fatalf("write through a link: %v", err)
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the link was followed")
	}
	again, _ := LoadVAPIDKeyFile(path)
	if !bytes.Equal(again.PublicKey(), k.PublicKey()) {
		t.Fatal("the file was overwritten")
	}
}

func TestKeyFileRefusals(t *testing.T) {
	k, _ := GenerateVAPIDKey()
	data, _ := k.MarshalPEM()
	dir := t.TempDir()
	write := func(name string, mode os.FileMode, content []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, content, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
		return p
	}

	for _, mode := range []os.FileMode{0o640, 0o604, 0o620, 0o602, 0o610} {
		p := write("open.pem", mode, data)
		if _, err := LoadVAPIDKeyFile(p); !errors.Is(err, ErrUnsafe) {
			t.Errorf("mode %o: %v", mode, err)
		}
		must(t, os.Remove(p))
	}
	good := write("good.pem", 0o600, data)
	if _, err := LoadVAPIDKeyFile(good); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "sym.pem")
	must(t, os.Symlink(good, link))
	if _, err := LoadVAPIDKeyFile(link); !errors.Is(err, ErrUnsafe) {
		t.Errorf("symbolic link: %v", err)
	}
	hard := filepath.Join(dir, "hard.pem")
	if err := os.Link(good, hard); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadVAPIDKeyFile(good); !errors.Is(err, ErrUnsafe) {
		t.Errorf("hard link: %v", err)
	}
	must(t, os.Remove(hard))
	big := write("big.pem", 0o600, append(bytes.Repeat([]byte("\n"), maxKeyFile), data...))
	if _, err := LoadVAPIDKeyFile(big); !errors.Is(err, ErrUnsafe) {
		t.Errorf("over 16 KiB: %v", err)
	}
	notKey := write("text.pem", 0o600, []byte("hello\n"))
	if _, err := LoadVAPIDKeyFile(notKey); !errors.Is(err, ErrKey) {
		t.Errorf("not a key: %v", err)
	}
	fifo := filepath.Join(dir, "fifo.pem")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadVAPIDKeyFile(fifo); !errors.Is(err, ErrUnsafe) {
		t.Errorf("FIFO: %v", err)
	}
	if _, err := LoadVAPIDKeyFile(dir); !errors.Is(err, ErrUnsafe) {
		t.Errorf("a folder: %v", err)
	}
	if _, err := LoadVAPIDKeyFile(filepath.Join(dir, "missing.pem")); err == nil || errors.Is(err, ErrUnsafe) {
		t.Errorf("missing file: %v", err)
	}

	// The folder: writable by group or other, a link, or missing.
	open := filepath.Join(t.TempDir(), "open")
	must(t, os.Mkdir(open, 0o700))
	must(t, os.Chmod(open, 0o770))
	if err := WriteVAPIDKeyFile(filepath.Join(open, "k.pem"), k); !errors.Is(err, ErrUnsafe) {
		t.Errorf("group-writable folder, write: %v", err)
	}
	must(t, os.Chmod(open, 0o700))
	if err := WriteVAPIDKeyFile(filepath.Join(open, "k.pem"), k); err != nil {
		t.Fatal(err)
	}
	must(t, os.Chmod(open, 0o707))
	if _, err := LoadVAPIDKeyFile(filepath.Join(open, "k.pem")); !errors.Is(err, ErrUnsafe) {
		t.Errorf("other-writable folder, load: %v", err)
	}
	must(t, os.Chmod(open, 0o700))
	dirLink := filepath.Join(t.TempDir(), "dirlink")
	must(t, os.Symlink(open, dirLink))
	if _, err := LoadVAPIDKeyFile(filepath.Join(dirLink, "k.pem")); !errors.Is(err, ErrUnsafe) {
		t.Errorf("folder is a link: %v", err)
	}
	if err := WriteVAPIDKeyFile(filepath.Join(dir, "nope", "k.pem"), k); err == nil {
		t.Error("missing folder: written")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
