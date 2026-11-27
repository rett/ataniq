package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T) (string, map[string]any) {
	t.Helper()
	dir := t.TempDir()
	must(t, os.Chmod(dir, 0700))
	m := sampleConfig(dir)
	path := filepath.Join(dir, "config.json")
	writeConfig(t, path, m)
	return path, m
}

func invoke(path, op string, uid int) (int, string, string) {
	var out, diag bytes.Buffer
	code := run([]string{op, "--config", path}, &out, &diag, uid)
	return code, out.String(), diag.String()
}

func call(t *testing.T, path, op string) string {
	t.Helper()
	code, out, diag := invoke(path, op, 1000)
	if code != 0 || diag != "" {
		t.Fatalf("%s: exit %d, %s", op, code, diag)
	}
	return out
}

func refused(t *testing.T, path, op, field string) {
	t.Helper()
	code, out, diag := invoke(path, op, 1000)
	if code != 1 || out != "" || !strings.Contains(diag, field) {
		t.Fatalf("%s: exit %d, out %q, diagnostic %q (want %s)", op, code, out, diag, field)
	}
}

func read(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	must(t, err)
	return b
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	must(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestIdentityInitAndPins(t *testing.T) {
	path, _ := fixture(t)
	dir := filepath.Dir(path)
	if got := call(t, path, "identity-init"); got != "" {
		t.Fatalf("unexpected output %q", got)
	}
	if got := names(t, dir); !slices.Equal(got, []string{"config.json", "seed", "tls"}) {
		t.Fatal(got)
	}
	if got := names(t, dir+"/tls"); !slices.Equal(got, []string{"cert.pem", "key.pem"}) {
		t.Fatal(got)
	}
	for name, mode := range map[string]os.FileMode{"seed": 0600, "tls": os.ModeDir | 0700, "tls/key.pem": 0600, "tls/cert.pem": 0600} {
		fi, err := os.Lstat(filepath.Join(dir, name))
		must(t, err)
		if fi.Mode() != mode {
			t.Errorf("%s mode %s want %s", name, fi.Mode(), mode)
		}
	}
	block, rest := pem.Decode(read(t, dir+"/tls/cert.pem"))
	if block == nil || len(rest) != 0 {
		t.Fatal("bad PEM certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	must(t, err)
	seed := read(t, dir+"/seed")
	if len(seed) != 32 {
		t.Fatalf("seed length %d", len(seed))
	}
	publicKey := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	want := fmt.Sprintf("home_tls_pin=%x\nhome_public_key=%x\n", sha256.Sum256(cert.RawSubjectPublicKeyInfo), publicKey)
	if got := call(t, path, "pins"); got != want {
		t.Fatalf("pins: %q, want %q", got, want)
	}
	refused(t, path, "identity-init", "home_key")
	if got := call(t, path, "pins"); got != want {
		t.Fatal("second init changed identity")
	}
}

func TestIdentityInitRefusesBeforeCreating(t *testing.T) {
	for _, existing := range []string{"seed", "tls", "tls-link"} {
		t.Run(existing, func(t *testing.T) {
			path, _ := fixture(t)
			dir := filepath.Dir(path)
			switch existing {
			case "seed":
				must(t, os.WriteFile(dir+"/seed", []byte("original"), 0600))
			case "tls":
				must(t, os.Mkdir(dir+"/tls", 0700))
			case "tls-link":
				must(t, os.Symlink("missing", dir+"/tls"))
			}
			before := names(t, dir)
			field := "tls_dir"
			if existing == "seed" {
				field = "home_key"
			}
			refused(t, path, "identity-init", field)
			if !slices.Equal(names(t, dir), before) {
				t.Fatal("destinations changed")
			}
			if existing == "seed" && string(read(t, dir+"/seed")) != "original" {
				t.Fatal("existing seed changed")
			}
			if existing == "tls" && len(names(t, dir+"/tls")) != 0 {
				t.Fatal("wrote in existing TLS dir")
			}
		})
	}
	path, _ := fixture(t)
	code, out, diag := invoke(path, "identity-init", 0)
	if code != 1 || out != "" || !strings.Contains(diag, "home account") || !strings.Contains(diag, "root") {
		t.Fatal(code, out, diag)
	}
	if len(names(t, filepath.Dir(path))) != 1 {
		t.Fatal("root refusal created files")
	}
}

func TestIdentityInitUnsafeParent(t *testing.T) {
	for _, field := range []string{"home_key", "tls_dir"} {
		for _, kind := range []string{"symlink", "group-write", "sticky-group-write"} {
			t.Run(field+"/"+kind, func(t *testing.T) {
				path, m := fixture(t)
				dir := filepath.Dir(path)
				parent := dir + "/parent"
				must(t, os.Mkdir(parent, 0700))
				switch kind {
				case "symlink":
					must(t, os.Symlink(parent, dir+"/alias"))
					parent = dir + "/alias"
				case "group-write":
					must(t, os.Chmod(parent, 0770))
				case "sticky-group-write":
					must(t, os.Chmod(parent, os.ModeSticky|0770))
				}
				m[field] = parent + "/identity"
				writeConfig(t, path, m)
				before := names(t, dir)
				refused(t, path, "identity-init", field)
				if !slices.Equal(names(t, dir), before) || len(names(t, parent)) != 0 {
					t.Fatal("refusal changed files")
				}
			})
		}
	}
}

func TestPinsEnforcesIdentityModesAndLinks(t *testing.T) {
	for _, tc := range []struct{ name, action, field string }{
		{"tls", "sticky", "tls_dir"}, {"tls/key.pem", "readable", "tls_dir"}, {"tls/cert.pem", "link", "tls_dir"},
		{"seed", "link", "home_key"}, {"seed", "symlink", "home_key"}, {"seed", "readable", "home_key"}, {"seed", "short", "home_key"},
	} {
		t.Run(tc.name+"/"+tc.action, func(t *testing.T) {
			path, _ := fixture(t)
			call(t, path, "identity-init")
			p := filepath.Join(filepath.Dir(path), tc.name)
			switch tc.action {
			case "readable":
				must(t, os.Chmod(p, 0640))
			case "sticky":
				must(t, os.Chmod(p, os.ModeSticky|0770))
			case "link":
				must(t, os.Link(p, p+".link"))
			case "symlink":
				must(t, os.Rename(p, p+".original"))
				must(t, os.Symlink(p+".original", p))
			case "short":
				must(t, os.Truncate(p, 31))
			}
			refused(t, path, "pins", tc.field)
		})
	}
}

func TestIdentityInitConcurrent(t *testing.T) {
	path, _ := fixture(t)
	var wg sync.WaitGroup
	var codes [2]int
	for i := range codes {
		wg.Go(func() { codes[i], _, _ = invoke(path, "identity-init", 1000) })
	}
	wg.Wait()
	slices.Sort(codes[:])
	if codes != [2]int{0, 1} {
		t.Fatalf("concurrent init exit codes %v", codes)
	}
	call(t, path, "pins")
}
