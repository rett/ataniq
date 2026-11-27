package instanceid

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
)

func makeID(t *testing.T, path, label string) Identity {
	t.Helper()
	root, err := OpenRoot(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	id, err := InitAt(root, label)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRootIdentityLifetime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state")
	a := makeID(t, path, "desk one")
	b := makeID(t, filepath.Join(t.TempDir(), "state"), a.Label)
	if a.Authority == b.Authority || a.Incarnation == b.Incarnation {
		t.Fatal("independent roots have a common id")
	}
	got, err := Load(path)
	if err != nil || got != a || makeID(t, path, "another label") != a {
		t.Fatalf("identity changed on load or repeated initialization: %+v, %v", got, err)
	}
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); !errors.Is(err, ErrMissing) {
		t.Fatalf("removed root: %v", err)
	}
	fresh := makeID(t, path, a.Label)
	if fresh.Incarnation == a.Incarnation || fresh.Authority == a.Authority {
		t.Fatal("recreated root reused an id")
	}
}

func TestAuthorityEncodingAndDisplayLabel(t *testing.T) {
	// RFC 8032 test vector 1 (public test seed).
	seed, _ := hex.DecodeString("9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60")
	pub, _ := hex.DecodeString("d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a")
	fingerprint := sha256.Sum256(pub)
	path := filepath.Join(t.TempDir(), "state")
	makeID(t, path, "")
	for _, label := range []string{"", "North desk", "South desk"} {
		body := append([]byte(magic), seed...)
		body = append(body, bytes.Repeat([]byte{0xab}, incarnationSize)...)
		body = append(body, label...)
		sum := sha256.Sum256(body)
		if err := os.WriteFile(filepath.Join(path, FileName), append(body, sum[:]...), 0600); err != nil {
			t.Fatal(err)
		}
		id, err := Load(path)
		if err != nil || id.Authority != hex.EncodeToString(fingerprint[:]) || id.Incarnation != strings.Repeat("ab", 16) || id.Label != label {
			t.Fatalf("load with label %q: %+v, %v", label, id, err)
		}
	}
}

func TestIdentityRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(key string) error
		want   error
	}{
		{"mode", func(p string) error { return os.Chmod(p, 0640) }, ErrUnsafe},
		{"hard link", func(p string) error { return os.Link(p, p+".link") }, ErrUnsafe},
		{"symlink", func(p string) error {
			if err := os.Rename(p, p+".target"); err != nil {
				return err
			}
			return os.Symlink(p+".target", p)
		}, ErrUnsafe},
		{"truncated", func(p string) error { return os.Truncate(p, 3) }, ErrInvalid},
		{"bit flip", func(p string) error {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			b[len(magic)+ed25519.SeedSize] ^= 1
			return os.WriteFile(p, b, 0600)
		}, ErrInvalid},
		{"missing", os.Remove, ErrMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state")
			makeID(t, path, "")
			if err := tc.change(filepath.Join(path, FileName)); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); !errors.Is(err, tc.want) {
				t.Fatalf("load: %v", err)
			}
			if tc.want != ErrMissing {
				root, err := OpenRoot(path, false)
				if err != nil {
					t.Fatal(err)
				}
				defer root.Close()
				if _, err := InitAt(root, ""); !errors.Is(err, tc.want) {
					t.Fatalf("init: %v", err)
				}
			}
		})
	}
}

func TestRefuseOtherOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state")
	makeID(t, path, "")
	var st unix.Stat_t
	if err := unix.Stat(filepath.Join(path, FileName), &st); err != nil {
		t.Fatal(err)
	}
	// Exercise the descriptor check as a different effective UID. Tests
	// run unprivileged and cannot chown a file to another account.
	if err := checkFile(st, os.Geteuid()+1); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("foreign key owner: %v", err)
	}
}

func TestConcurrentInit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state")
	root, err := OpenRoot(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			first, err := InitAt(root, "")
			if err != nil {
				t.Error(err)
				return
			}
			got, err := Load(path)
			if err != nil || got != first {
				t.Errorf("init failed to converge: %v", err)
			}
		})
	}
	wg.Wait()
}
