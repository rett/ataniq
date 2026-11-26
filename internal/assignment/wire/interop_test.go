package wire

import (
	"bytes"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func sshKeygen(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("ssh-keygen")
	if errors.Is(err, exec.ErrNotFound) {
		t.Skip("ssh-keygen is absent from PATH; OpenSSH interoperability requires it")
	}
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOpenSSHVerifiesGoldens(t *testing.T) {
	exe := sshKeygen(t)
	files, err := filepath.Glob("testdata/*.sig")
	if err != nil || len(files) < 12 {
		t.Fatalf("found %d golden signatures: %v", len(files), err)
	}
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			body := readFixture(t, strings.TrimSuffix(file, ".sig")+".json")
			message := append([]byte("ataniq.assignment.event.v1\x00"), body...)
			verify := func(namespace string, input []byte, accept bool) {
				t.Helper()
				cmd := exec.Command(exe, "-Y", "verify", "-f", "testdata/allowed_signers", "-I", identity, "-n", namespace, "-s", file)
				cmd.Stdin = bytes.NewReader(input)
				out, err := cmd.CombinedOutput()
				if (err == nil) != accept {
					t.Fatalf("verify %s: %v, %s", namespace, err, out)
				}
				if err != nil {
					var status *exec.ExitError
					if !errors.As(err, &status) || status.ExitCode() <= 0 {
						t.Fatalf("unexpected OpenSSH failure: %v, %s", err, out)
					}
				}
			}
			verify("ataniq.assignment.event.v1", message, true)
			verify("ataniq.assignment.event.v1", append(message, '!'), false)
			verify("other.namespace", message, false)
		})
	}
}

func TestVerifyOpenSSHSignature(t *testing.T) {
	exe := sshKeygen(t)
	keyfile := filepath.Join(t.TempDir(), "key")
	if out, err := exec.Command(exe, "-q", "-t", "ed25519", "-N", "", "-C", "fixture", "-f", keyfile).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	pin, id, err := LoadPublicKey(string(readFixture(t, keyfile+".pub")))
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(exe, "-E", "sha256", "-lf", keyfile+".pub").CombinedOutput()
	if err != nil || !strings.Contains(string(out), " "+id+" ") {
		t.Fatal("key fingerprint", err, string(out))
	}
	body := readFixture(t, "testdata/04-begin.json")
	message := append([]byte("ataniq.assignment.event.v1\x00"), body...)
	for _, tc := range []struct {
		namespace, hash string
		input           []byte
		accept          bool
	}{
		{"ataniq.assignment.event.v1", "sha512", message, true},
		{"other.namespace", "sha512", message, false},
		{"ataniq.assignment.event.v1", "sha256", message, false},
		{"ataniq.assignment.event.v1", "sha512", body, false},
	} {
		cmd := exec.Command(exe, "-Y", "sign", "-f", keyfile, "-n", tc.namespace, "-O", "hashalg="+tc.hash)
		cmd.Stdin = bytes.NewReader(tc.input)
		armor, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		if err := Verify(pin, body, string(armor)); (err == nil) != tc.accept {
			t.Fatal(tc.namespace, tc.hash, err)
		}
	}
}
