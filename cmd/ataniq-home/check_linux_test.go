package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"ataniq/internal/seatgate"
)

func fullConfig(t *testing.T) (string, map[string]any) {
	t.Helper()
	path, m, _, _ := witnessConfig(t)
	return path, m
}

func witnessConfig(t *testing.T) (string, map[string]any, ed25519.PrivateKey, seatgate.Identity) {
	t.Helper()
	path, m := fixture(t)
	pub, signingKey, err := ed25519.GenerateKey(rand.Reader)
	must(t, err)
	cert, key, err := seatgate.GenerateIdentity(rand.Reader, time.Now(), "witness")
	must(t, err)
	id, err := seatgate.ParseIdentity(cert, key)
	must(t, err)
	block, _ := pem.Decode(cert)
	certPath := filepath.Join(filepath.Dir(path), "witness.der")
	must(t, os.WriteFile(certPath, block.Bytes, 0644))
	m["witness_public_key"] = hex.EncodeToString(pub)
	m["witness_tls_pin"] = id.Fingerprint()
	m["witness_cert"] = certPath
	writeConfig(t, path, m)
	return path, m, signingKey, id
}

func TestCheckReadOnly(t *testing.T) {
	path, m := fullConfig(t)
	dir := filepath.Dir(path)
	m["binding"] = dir + "/binding"
	m["pending_journal"] = dir + "/journal"
	writeConfig(t, path, m)
	before := names(t, dir)
	refused(t, path, "check", "home_key")
	if !slices.Equal(before, names(t, dir)) {
		t.Fatal("check created a missing identity")
	}
	call(t, path, "identity-init")
	before = names(t, dir)
	files := []string{"config.json", "seed", "tls/cert.pem", "tls/key.pem", "witness.der"}
	type snapshot struct {
		sum      [32]byte
		mode     os.FileMode
		modified time.Time
	}
	snap := func(name string) snapshot {
		p := filepath.Join(dir, name)
		fi, err := os.Stat(p)
		must(t, err)
		return snapshot{sha256.Sum256(read(t, p)), fi.Mode(), fi.ModTime()}
	}
	saved := map[string]snapshot{}
	for _, name := range files {
		saved[name] = snap(name)
	}
	if got := call(t, path, "check"); got != "home check ok\n" {
		t.Fatal(got)
	}
	if got, _ := cli(t, 0, "serve", "--check", "--config", path); got != "home check ok\n" {
		t.Fatal(got)
	}
	if !slices.Equal(before, names(t, dir)) || !slices.Equal(names(t, dir+"/tls"), []string{"cert.pem", "key.pem"}) {
		t.Fatal("check changed directory entries")
	}
	for name, want := range saved {
		if snap(name) != want {
			t.Errorf("check changed %s", name)
		}
	}
}

func TestCheckKeyRolesAndCertificate(t *testing.T) {
	path, m := fullConfig(t)
	call(t, path, "identity-init")
	pins := strings.Split(strings.TrimSpace(call(t, path, "pins")), "\n")
	for _, tc := range []struct{ field, value string }{
		{"witness_public_key", strings.TrimPrefix(pins[1], "home_public_key=")},
		{"witness_public_key", strings.Repeat("ff", 32)},
		{"witness_tls_pin", strings.TrimPrefix(pins[0], "home_tls_pin=")},
		{"witness_tls_pin", strings.Repeat("ab", 32)},
	} {
		save := m[tc.field]
		m[tc.field] = tc.value
		writeConfig(t, path, m)
		refused(t, path, "check", tc.field)
		m[tc.field] = save
	}
	writeConfig(t, path, m)
	certPath := m["witness_cert"].(string)
	der := read(t, certPath)
	armored := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	for _, data := range [][]byte{der, armored} {
		must(t, os.WriteFile(certPath, data, 0600))
		call(t, path, "check")
	}
	for _, data := range [][]byte{nil, []byte("not DER"), bytes.Repeat(der, 2), append(bytes.Clone(der), 0),
		append([]byte("prefix\n"), armored...), append(bytes.Clone(armored), '\n'), bytes.Repeat(armored, 2),
		append([]byte("-----BEGIN CERTIFICATE-----\ntorn\n"), armored...), append(bytes.Clone(armored), 'x'),
		append(bytes.Clone(bytes.TrimSuffix(armored, []byte("\n"))), ' '),
		pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Headers: map[string]string{"Comment": "extra"}, Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: bytes.Repeat(der, 2)})} {
		must(t, os.WriteFile(certPath, data, 0600))
		refused(t, path, "check", "witness_cert")
	}
	must(t, os.Remove(certPath))
	refused(t, path, "check", "witness_cert")
	if _, err := os.Lstat(certPath); !os.IsNotExist(err) {
		t.Fatal("missing certificate created")
	}
}

func TestCheckRequiresWitness(t *testing.T) {
	path, m := fullConfig(t)
	call(t, path, "identity-init")
	for _, field := range []string{"witness_cert", "witness_public_key", "witness_tls_pin"} {
		save := m[field]
		delete(m, field)
		writeConfig(t, path, m)
		refused(t, path, "check", field)
		call(t, path, "pins")
		m[field] = save
	}
}

func TestHomeUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"bad"}, {"pins"}, {"check", "-config", "/missing"}, {"pins", "--config", "/missing", "extra"},
		{"enroll"}, {"enroll", "unknown"}, {"enroll", "prepare", "--config", "/missing"},
		{"enroll", "finalize", "--config", "/missing", "--out", "/out"},
		{"enroll", "resume", "--out", "/one", "--out", "/two"},
		{"serve", "--check", "--check", "--config", "/missing"}, {"status", "--check"}, {"retire"}, {"retire", "--events", "1", "--config", "/c"}, {"observe"}, {"observe", "--events", "-1", "--config", "/c"}, {"observe", "--config", "/c", "--events", "17"}, {"observe", "--config", "/c", "--archive", "/a", "--archive", "/b"}} {
		var diag, out bytes.Buffer
		if code := run(args, &out, &diag, 1000); code != 2 || diag.String() != usage+"\n" || out.Len() != 0 {
			t.Error(args, code, out.String(), diag.String())
		}
	}
}

func TestCheckAndServePreflight(t *testing.T) {
	for _, kind := range []string{"retired", "unsafe directory", "locked installation"} {
		t.Run(kind, func(t *testing.T) {
			path, fields, _, _ := ceremonyConfig(t)
			call(t, path, "identity-init")
			var want string
			switch kind {
			case "retired":
				call(t, path, "retire")
				want = "home retired"
			case "unsafe directory":
				dir := filepath.Join(filepath.Dir(path), "unsafe")
				must(t, os.Mkdir(dir, 0700))
				must(t, os.Chmod(dir, 0777))
				fields["binding"] = filepath.Join(dir, "binding")
				writeConfig(t, path, fields)
				want = "binding directory"
			case "locked installation":
				dir, err := lockInstallation(fields["binding"].(string), true)
				must(t, err)
				defer dir.Close()
				want = "home installation busy"
			}
			_, first := cli(t, 1, "check", "--config", path)
			_, second := cli(t, 1, "serve", "--check", "--config", path)
			if !strings.Contains(first, want) || first != second {
				t.Fatalf("check %q, serve --check %q", first, second)
			}
		})
	}
}
