package seatgate

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestGenerateIdentityShape(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	certPEM, keyPEM, err := GenerateIdentity(rand.Reader, now, "seat-one")
	if err != nil {
		t.Fatal(err)
	}
	blk, rest := pem.Decode(certPEM)
	if blk == nil || blk.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		t.Fatalf("the certificate is not one CERTIFICATE block")
	}
	c, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if c.Subject.CommonName != "seat-one" {
		t.Errorf("CN = %q", c.Subject.CommonName)
	}
	if !c.NotBefore.Equal(now) || !c.NotAfter.Equal(now.AddDate(10, 0, 0)) {
		t.Errorf("validity %v to %v", c.NotBefore, c.NotAfter)
	}
	if c.KeyUsage != x509.KeyUsageDigitalSignature {
		t.Errorf("key usage %v", c.KeyUsage)
	}
	if len(c.ExtKeyUsage) != 2 {
		t.Errorf("ext key usage %v", c.ExtKeyUsage)
	}
	pub, ok := c.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		t.Errorf("key is %T", c.PublicKey)
	}
	if kb, _ := pem.Decode(keyPEM); kb == nil || kb.Type != "PRIVATE KEY" {
		t.Errorf("the key is not a PKCS#8 PRIVATE KEY block")
	}
	id, err := ParseIdentity(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(c.RawSubjectPublicKeyInfo)
	if want := hex.EncodeToString(sum[:]); id.Fingerprint() != want {
		t.Errorf("fingerprint %s, want %s", id.Fingerprint(), want)
	}
	if !ValidFingerprint(id.Fingerprint()) {
		t.Errorf("own fingerprint is not valid")
	}
}

func TestTwoIdentitiesDiffer(t *testing.T) {
	a, b := newID(t, "a"), newID(t, "b")
	if a.Fingerprint() == b.Fingerprint() {
		t.Fatal("two identities share a fingerprint")
	}
}

func TestGenerateIdentityRefusesABadName(t *testing.T) {
	for _, name := range []string{"", strings.Repeat("a", 65), "a b", "a\x00b", "é", "a/b"} {
		if _, _, err := GenerateIdentity(rand.Reader, time.Now(), name); err == nil {
			t.Errorf("name %q was accepted", name)
		}
	}
}

func TestValidFingerprint(t *testing.T) {
	good := strings.Repeat("0123456789abcdef", 4)
	if !ValidFingerprint(good) {
		t.Fatal("a good fingerprint was refused")
	}
	for name, s := range map[string]string{
		"empty":     "",
		"short":     good[:63],
		"long":      good + "0",
		"upper":     strings.ToUpper(good),
		"colons":    strings.Repeat("ab:", 21) + "a",
		"non-hex":   good[:63] + "g",
		"space":     good[:63] + " ",
		"newline":   good[:63] + "\n",
		"prefixed":  "sha256:" + good,
		"multibyte": good[:62] + "é",
	} {
		if ValidFingerprint(s) {
			t.Errorf("%s: %q was accepted", name, s)
		}
	}
}

func TestParseIdentityRefusals(t *testing.T) {
	certPEM, keyPEM, err := GenerateIdentity(rand.Reader, time.Now(), "x")
	if err != nil {
		t.Fatal(err)
	}
	otherCert, otherKey, _ := GenerateIdentity(rand.Reader, time.Now(), "x")
	cases := map[string][2][]byte{
		"empty":        {nil, nil},
		"mismatch":     {certPEM, otherKey},
		"cert as key":  {certPEM, certPEM},
		"two certs":    {append(append([]byte(nil), certPEM...), otherCert...), keyPEM},
		"garbage cert": {[]byte("-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"), keyPEM},
	}
	for name, c := range cases {
		if _, err := ParseIdentity(c[0], c[1]); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestWriteAndLoadIdentityFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "gate")
	id, err := WriteIdentityFiles(dir, "gate-one")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{CertFile, KeyFile} {
		st, err := os.Lstat(filepath.Join(dir, n))
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Errorf("%s has mode %v", n, st.Mode().Perm())
		}
	}
	if st, _ := os.Stat(dir); st.Mode().Perm() != 0o700 {
		t.Errorf("folder has mode %v", st.Mode().Perm())
	}
	got, err := LoadIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Fingerprint() != id.Fingerprint() {
		t.Fatal("the loaded identity is another one")
	}
}

func TestWriteIdentityFilesNeverOverwrites(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "id")
	first, err := WriteIdentityFiles(dir, "one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := WriteIdentityFiles(dir, "two"); err != ErrExists {
		t.Fatalf("second write: %v, want ErrExists", err)
	}
	got, err := LoadIdentity(dir)
	if err != nil || got.Fingerprint() != first.Fingerprint() {
		t.Fatalf("the first identity was changed: %v", err)
	}
	// A lone key file: refused, and no certificate is made beside it.
	dir2 := filepath.Join(t.TempDir(), "id2")
	if err := os.Mkdir(dir2, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir2, KeyFile), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteIdentityFiles(dir2, "three"); err != ErrExists {
		t.Fatalf("with a key file there: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir2, CertFile)); err == nil {
		t.Fatal("a certificate was written beside a key that was kept")
	}
	if b, _ := os.ReadFile(filepath.Join(dir2, KeyFile)); string(b) != "keep" {
		t.Fatal("the key file was overwritten")
	}
	// A lone certificate file: refused after the key; the key is removed again.
	dir3 := filepath.Join(t.TempDir(), "id3")
	if err := os.Mkdir(dir3, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir3, CertFile), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteIdentityFiles(dir3, "four"); err != ErrExists {
		t.Fatalf("with a certificate file there: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir3, KeyFile)); err == nil {
		t.Fatal("a key file was left behind after a refused write")
	}
}

func TestLoadIdentityRefusesUnsafeFiles(t *testing.T) {
	mk := func(t *testing.T) string {
		dir := filepath.Join(t.TempDir(), "id")
		if _, err := WriteIdentityFiles(dir, "x"); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	t.Run("group-readable key", func(t *testing.T) {
		dir := mk(t)
		os.Chmod(filepath.Join(dir, KeyFile), 0o640)
		if _, err := LoadIdentity(dir); err != ErrUnsafe {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("other-readable key", func(t *testing.T) {
		dir := mk(t)
		os.Chmod(filepath.Join(dir, KeyFile), 0o604)
		if _, err := LoadIdentity(dir); err != ErrUnsafe {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("group-writable certificate", func(t *testing.T) {
		dir := mk(t)
		os.Chmod(filepath.Join(dir, CertFile), 0o620)
		if _, err := LoadIdentity(dir); err != ErrUnsafe {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("readable certificate is fine", func(t *testing.T) {
		dir := mk(t)
		os.Chmod(filepath.Join(dir, CertFile), 0o644)
		if _, err := LoadIdentity(dir); err != nil {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("key is a link", func(t *testing.T) {
		dir := mk(t)
		real := filepath.Join(t.TempDir(), "real.pem")
		b, _ := os.ReadFile(filepath.Join(dir, KeyFile))
		os.WriteFile(real, b, 0o600)
		os.Remove(filepath.Join(dir, KeyFile))
		os.Symlink(real, filepath.Join(dir, KeyFile))
		if _, err := LoadIdentity(dir); err != ErrUnsafe {
			t.Fatalf("got %v", err)
		}
	})
	// A second hard link to a file is a name for it that the folder's
	// permissions do not cover: it is refused as a link is.
	for _, name := range []string{KeyFile, CertFile} {
		t.Run(name+" has a second hard link", func(t *testing.T) {
			dir := mk(t)
			if err := os.Link(filepath.Join(dir, name), filepath.Join(t.TempDir(), "other.pem")); err != nil {
				t.Skipf("no hard link here: %v", err)
			}
			if _, err := LoadIdentity(dir); err != ErrUnsafe {
				t.Fatalf("got %v", err)
			}
		})
	}
	t.Run("folder is a link", func(t *testing.T) {
		dir := mk(t)
		link := filepath.Join(t.TempDir(), "link")
		os.Symlink(dir, link)
		if _, err := LoadIdentity(link); err != ErrUnsafe {
			t.Fatalf("got %v", err)
		}
		if _, err := WriteIdentityFiles(link, "y"); err != ErrUnsafe {
			t.Fatalf("write through a link: %v", err)
		}
	})
	t.Run("group-writable folder", func(t *testing.T) {
		dir := mk(t)
		os.Chmod(dir, 0o770)
		if _, err := LoadIdentity(dir); err != ErrUnsafe {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("key is a folder", func(t *testing.T) {
		dir := mk(t)
		os.Remove(filepath.Join(dir, KeyFile))
		os.Mkdir(filepath.Join(dir, KeyFile), 0o700)
		if _, err := LoadIdentity(dir); err != ErrUnsafe {
			t.Fatalf("got %v", err)
		}
	})
	// A FIFO with no writer would block a plain open for ever: the open does
	// not wait, and the file is then refused for not being a regular one.
	for _, name := range []string{KeyFile, CertFile} {
		t.Run(name+" is a FIFO with no writer", func(t *testing.T) {
			dir := mk(t)
			fifo := filepath.Join(dir, name)
			os.Remove(fifo)
			if err := syscall.Mkfifo(fifo, 0o600); err != nil {
				t.Skipf("no FIFO here: %v", err)
			}
			t.Cleanup(func() { // lets a reader that blocked go, so a failure ends
				if f, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
					f.Close()
				}
			})
			res := make(chan error, 1)
			go func() { _, err := LoadIdentity(dir); res <- err }()
			if err := within(t, "LoadIdentity to return", res, 5*time.Second); err != ErrUnsafe {
				t.Fatalf("got %v", err)
			}
		})
	}
	t.Run("missing", func(t *testing.T) {
		if _, err := LoadIdentity(filepath.Join(t.TempDir(), "nope")); err == nil {
			t.Fatal("a missing folder loaded")
		}
	})
	t.Run("an error text holds no path", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "private-name")
		_, err := LoadIdentity(dir)
		if err == nil || strings.Contains(err.Error(), "private-name") {
			t.Fatalf("error %v", err)
		}
	})
}
