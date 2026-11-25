//go:build linux && ataniq_ownerchannel && ataniq_canary

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/leaddaemon"
)

// Tests of run --check --keys-prefix and --keys-since (scratch_keys_linux.go):
// the key list of the state folder and its head, judged by the journal's own
// checks read only (ownerjournal.VerifyKeys), and the records appended since
// a recorded prefix.

// keysRun runs the key check with the world's config and returns the exit
// code, the output and the error output. TMPDIR is a folder of the test's,
// so that a temporary file would be seen.
func keysRun(t *testing.T, w *settingsWorld, more ...string) (int, string, string) {
	t.Helper()
	return w.run(t, append([]string{"run", "--scratch", w.config, "--owner-channel", "--check"}, more...)...)
}

// keysPrefixOf is the prefix of the world's key list as it is now.
func keysPrefixOf(t *testing.T, w *settingsWorld) string {
	t.Helper()
	b := fileBytes(t, keysPathOf(w.state))
	sum := sha256.Sum256(b)
	return strconv.Itoa(len(b)) + ":" + hex.EncodeToString(sum[:])
}

// keysSnapshotOf is what --keys-prefix prints of the world's key list as it
// is now: the prefix, a space and the SHA-256 of the head.
func keysSnapshotOf(t *testing.T, w *settingsWorld) string {
	t.Helper()
	sum := sha256.Sum256(fileBytes(t, filepath.Join(w.owner, ownerjournal.KeysHeadFileName)))
	return keysPrefixOf(t, w) + " " + hex.EncodeToString(sum[:])
}

// appendChained appends one record to the world's key list with a digest
// that follows the chain, and moves the head to count it: a record the
// chain holds and the rules of the list may refuse.
func appendChained(t *testing.T, w *settingsWorld, rec string) {
	t.Helper()
	path := keysPathOf(w.state)
	data := fileBytes(t, path)
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	prev := sha256.Sum256([]byte(lines[0]))
	if len(lines) > 1 {
		last := lines[len(lines)-1]
		if _, err := hex.Decode(prev[:], []byte(last[:64])); err != nil {
			t.Fatal(err)
		}
	}
	h := sha256.New()
	h.Write(prev[:])
	h.Write([]byte(rec))
	sum := h.Sum(nil)
	data = append(data, []byte(hex.EncodeToString(sum)+" "+rec+"\n")...)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	head := fmt.Sprintf("ataniq-ownerkeys-head 1 %d %s\n", len(lines), hex.EncodeToString(sum))
	if err := os.WriteFile(filepath.Join(w.owner, ownerjournal.KeysHeadFileName), []byte(head), 0o600); err != nil {
		t.Fatal(err)
	}
}

// keysWorld is a settings world with TMPDIR in a folder of its own.
func keysWorld(t *testing.T) (*settingsWorld, string) {
	t.Helper()
	w := newSettingsWorld(t)
	tmp := filepath.Join(w.dir, "tmp")
	if err := os.Mkdir(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", tmp)
	return w, tmp
}

// nothingInTmp fails the test when the key check wrote in TMPDIR: it
// makes no copy and no temporary file.
func nothingInTmp(t *testing.T, tmp string) {
	t.Helper()
	if names, err := os.ReadDir(tmp); err != nil || len(names) != 0 {
		t.Errorf("the key check wrote %v in TMPDIR (%v)", names, err)
	}
}

// ownerFolder is every name of the trial's owner/ folder with its inode, mode,
// times and bytes, and the folder's own, so that any write is seen.
func ownerFolder(t *testing.T, w *settingsWorld) map[string]string {
	t.Helper()
	out := map[string]string{}
	names, err := os.ReadDir(w.owner)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range append([]string{"."}, func() []string {
		var s []string
		for _, e := range names {
			s = append(s, e.Name())
		}
		return s
	}()...) {
		p := filepath.Join(w.owner, n)
		var st syscall.Stat_t
		if err := syscall.Lstat(p, &st); err != nil {
			t.Fatal(err)
		}
		v := fmt.Sprint(st.Mode, " ", st.Ino, " ", st.Nlink, " ", st.Mtim.Nano(), " ", st.Ctim.Nano())
		if st.Mode&syscall.S_IFMT == syscall.S_IFREG {
			v += " " + string(fileBytes(t, p))
		}
		out[n] = v
	}
	return out
}

// --keys-prefix prints the list's length and SHA-256, and the SHA-256 of
// its head, once the journal's
// checks pass the list and its head, and writes nothing: the trial's owner/
// folder is as it was, to the inode and the time, and TMPDIR is empty. --keys-since that prefix
// then holds with no record appended. The flags need --check, exclude each
// other and take a prefix of the form only.
func TestKeysCheckPrintsThePrefixAndHoldsOnTheSameList(t *testing.T) {
	useSuite(t, cmdSuite{})
	w, tmp := keysWorld(t)
	folder := ownerFolder(t, w)
	code, out, msg := keysRun(t, w, "--keys-prefix")
	if code != 0 || msg != "" || out != keysSnapshotOf(t, w)+"\n" {
		t.Fatalf("--keys-prefix: exit %d: %q %q, want %q", code, out, msg, keysSnapshotOf(t, w))
	}
	w.sameTrial(t, "after --keys-prefix")
	nothingInTmp(t, tmp)
	prefix, _, _ := strings.Cut(strings.TrimSpace(out), " ")
	code, out, _ = keysRun(t, w, "--keys-since", prefix)
	if code != 0 || !strings.Contains(out, "keys  "+keysPathOf(w.state)+": 1 record(s), each well formed, in sequence, on the digest chain and within the rules of the list, and its head keys.head counts them") ||
		!strings.Contains(out, "no record was appended") || !strings.HasSuffix(out, "keys: holds: the key list is the list of step g\n") {
		t.Fatalf("--keys-since on the same list: exit %d:\n%s", code, out)
	}
	w.sameTrial(t, "after --keys-since")
	nothingInTmp(t, tmp)
	if after := ownerFolder(t, w); fmt.Sprint(after) != fmt.Sprint(folder) {
		t.Errorf("the key check changed the owner/ folder:\n%v\n%v", folder, after)
	}

	for _, c := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"--keys-prefix", "--keys-since", prefix}, leaddaemon.ExitUsage, "exclude each other"},
		{[]string{"--keys-since", "12"}, leaddaemon.ExitUsage, "is not LEN:SHA256"},
		{[]string{"--keys-since", "012:" + strings.Repeat("ab", 32)}, leaddaemon.ExitUsage, "is not LEN:SHA256"},
		{[]string{"--keys-since", "12:" + strings.Repeat("AB", 32)}, leaddaemon.ExitUsage, "is not LEN:SHA256"},
		{[]string{"--keys-since", "12:" + strings.Repeat("ab", 31)}, leaddaemon.ExitUsage, "is not LEN:SHA256"},
	} {
		code, out, msg := keysRun(t, w, c.args...)
		if code != c.code || !strings.Contains(msg, c.want) || out != "" {
			t.Errorf("%v: exit %d, %q %q, want %d and %q", c.args, code, out, msg, c.code, c.want)
		}
	}
	ta := canaryApp(t)
	ta.getenv = w.env
	if code := runBounded(t, ta, []string{"run", "--scratch", w.config, "--owner-channel", "--keys-prefix"}); code != leaddaemon.ExitUsage ||
		!strings.Contains(ta.err.String(), "need --check") {
		t.Errorf("--keys-prefix without --check: exit %d: %s", code, ta.err.String())
	}
	help := newTestApp(t, nil)
	if code := help.run([]string{"--help"}); code != 0 {
		t.Fatal(code)
	}
	for _, want := range []string{"--check --keys-prefix", "--keys-since LEN:SHA256", "keys: holds", "keys: CHANGED"} {
		if !strings.Contains(help.out.String(), want) {
			t.Errorf("the usage does not name %q", want)
		}
	}
}

// The journal's own writes after the prefix: a counter record (a phone login
// whose sign counter rises) holds, alone or several; a pairing, a revocation
// and a refused pairing are CHANGED, named by their type.
func TestKeysCheckNamesWhatTheJournalAppended(t *testing.T) {
	useSuite(t, cmdSuite{})
	at := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	id := ownerjournal.KeyDisplayID([]byte(strings.Repeat("c", 16)))
	journal := func(t *testing.T, w *settingsWorld, do func(k *ownerjournal.KeyFile) error) {
		t.Helper()
		k, err := ownerjournal.OpenKeys(keysPathOf(w.state))
		if err != nil {
			t.Fatal(err)
		}
		if err := do(k); err != nil {
			t.Fatal(err)
		}
		k.Close()
	}
	for _, c := range []struct {
		name string
		do   func(k *ownerjournal.KeyFile) error
		code int
		want []string
	}{
		{"one counter record", func(k *ownerjournal.KeyFile) error { return k.RaiseCounter(id, 5, at) }, 0,
			[]string{"1 record(s) appended: counter", "keys: holds: only counter records were appended"}},
		{"two counter records", func(k *ownerjournal.KeyFile) error {
			if err := k.RaiseCounter(id, 5, at); err != nil {
				return err
			}
			return k.RaiseCounter(id, 6, at)
		}, 0, []string{"2 record(s) appended: counter counter", "keys: holds"}},
		{"a pairing", func(k *ownerjournal.KeyFile) error {
			_, err := k.AddKey(ownerjournal.NewKey{CredentialID: []byte(strings.Repeat("d", 16)), PublicKey: []byte("k"), Kind: "platform-passkey", PairedAt: at, Words: keyWords()})
			return err
		}, 1, []string{"1 record(s) appended: added", "keys: CHANGED: a record that is not a counter was appended"}},
		{"a counter, then a revocation", func(k *ownerjournal.KeyFile) error {
			if err := k.RaiseCounter(id, 5, at); err != nil {
				return err
			}
			return k.RevokeKeys([]string{id}, at)
		}, 1, []string{"2 record(s) appended: counter revoked", "keys: CHANGED"}},
		{"a refused pairing", func(k *ownerjournal.KeyFile) error {
			return k.AppendRejected(ownerjournal.RejectedPairing{ID: ownerjournal.KeyDisplayID([]byte(strings.Repeat("e", 16))), Words: keyWords(), At: at})
		}, 1, []string{"1 record(s) appended: rejected", "keys: CHANGED"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, tmp := keysWorld(t)
			prefix := keysPrefixOf(t, w)
			journal(t, w, c.do)
			code, out, _ := keysRun(t, w, "--keys-since", prefix)
			if code != c.code {
				t.Errorf("exit %d, want %d:\n%s", code, c.code, out)
			}
			for _, want := range c.want {
				if !strings.Contains(out, want) {
					t.Errorf("the check does not say %q:\n%s", want, out)
				}
			}
			nothingInTmp(t, tmp)
		})
	}
}

// What the journal's checks refuse, or what is not the list of step g, is
// CHANGED, never a catch-up: a counter-shaped record whose digest follows the
// chain but that holds a key the form does not name, is numbered out of
// sequence, or raises no counter of a credential on the list; an unchanged
// list with a head that is not of the form; a head that lags the list by a
// valid counter record (the daemon would make it right, but it is not the
// list's head); no head; an old byte of the list changed; a prefix of the
// right length whose digest is another; a prefix longer than the list; and a
// prefix whose digest matches the list's bytes up to one byte inside a record
// (the prefix must end at a whole record). And what the daemon's opener
// refuses before it reads a byte, on the trial's own files: the list or its
// head of mode 0640, the list with a second name, and the list's name a final
// symbolic link to it. --keys-prefix refuses the same and prints no prefix.
func TestKeysCheckCallsDamageChanged(t *testing.T) {
	useSuite(t, cmdSuite{})
	id := ownerjournal.KeyDisplayID([]byte(strings.Repeat("c", 16)))
	head := func(w *settingsWorld) string { return filepath.Join(w.owner, ownerjournal.KeysHeadFileName) }
	for _, c := range []struct {
		name   string
		change func(t *testing.T, w *settingsWorld) (prefix string)
		want   string
	}{
		{"a counter record with a key the form does not name", func(t *testing.T, w *settingsWorld) string {
			p := keysPrefixOf(t, w)
			appendChained(t, w, `{"at":"2026-11-24T09:00:00Z","counter":5,"id":"`+id+`","note":"x","seq":2,"type":"counter"}`)
			return p
		}, "the journal's checks refuse the key list or its head"},
		{"a counter record numbered out of sequence", func(t *testing.T, w *settingsWorld) string {
			p := keysPrefixOf(t, w)
			appendChained(t, w, `{"at":"2026-11-24T09:00:00Z","counter":5,"id":"`+id+`","seq":3,"type":"counter"}`)
			return p
		}, "the journal's checks refuse the key list or its head"},
		{"a counter record that raises no counter on the list", func(t *testing.T, w *settingsWorld) string {
			p := keysPrefixOf(t, w)
			appendChained(t, w, `{"at":"2026-11-24T09:00:00Z","counter":5,"id":"00000000000000aa","seq":2,"type":"counter"}`)
			return p
		}, "the journal's checks refuse the key list or its head"},
		{"an unchanged list with a garbage head", func(t *testing.T, w *settingsWorld) string {
			p := keysPrefixOf(t, w)
			if err := os.WriteFile(head(w), []byte("not a head\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return p
		}, "the journal's checks refuse the key list or its head"},
		{"a head that lags the list", func(t *testing.T, w *settingsWorld) string {
			p := keysPrefixOf(t, w)
			old := fileBytes(t, head(w))
			appendChained(t, w, `{"at":"2026-11-24T09:00:00Z","counter":5,"id":"`+id+`","seq":2,"type":"counter"}`)
			if err := os.WriteFile(head(w), old, 0o600); err != nil {
				t.Fatal(err)
			}
			return p
		}, "does not count the whole list"},
		{"no head", func(t *testing.T, w *settingsWorld) string {
			p := keysPrefixOf(t, w)
			if err := os.Remove(head(w)); err != nil {
				t.Fatal(err)
			}
			return p
		}, "the head of the list (keys.head) is missing"},
		{"an old byte changed", func(t *testing.T, w *settingsWorld) string {
			p := keysPrefixOf(t, w)
			b := fileBytes(t, keysPathOf(w.state))
			b[len(b)-3] ^= 1
			if err := os.WriteFile(keysPathOf(w.state), b, 0o600); err != nil {
				t.Fatal(err)
			}
			return p
		}, "the journal's checks refuse the key list or its head"},
		{"another list of the same length", func(t *testing.T, w *settingsWorld) string {
			p := keysPrefixOf(t, w)
			n, _, _ := strings.Cut(p, ":")
			return n + ":" + strings.Repeat("ab", 32)
		}, "are not the list of step g"},
		{"a prefix longer than the list", func(t *testing.T, w *settingsWorld) string {
			p := keysPrefixOf(t, w)
			n, sum, _ := strings.Cut(p, ":")
			v, _ := strconv.Atoi(n)
			return strconv.Itoa(v+1) + ":" + sum
		}, "shorter than the"},
		{"a matching digest one byte inside a record", func(t *testing.T, w *settingsWorld) string {
			n := len(fileBytes(t, keysPathOf(w.state)))
			appendChained(t, w, `{"at":"2026-11-24T09:00:00Z","counter":5,"id":"`+id+`","seq":2,"type":"counter"}`)
			b := fileBytes(t, keysPathOf(w.state))
			sum := sha256.Sum256(b[:n+1])
			return strconv.Itoa(n+1) + ":" + hex.EncodeToString(sum[:])
		}, "are not the list of step g"},
		{"a list of mode 0640", func(t *testing.T, w *settingsWorld) string {
			if err := os.Chmod(keysPathOf(w.state), 0o640); err != nil {
				t.Fatal(err)
			}
			return keysPrefixOf(t, w)
		}, "not private"},
		{"a head of mode 0640", func(t *testing.T, w *settingsWorld) string {
			if err := os.Chmod(head(w), 0o640); err != nil {
				t.Fatal(err)
			}
			return keysPrefixOf(t, w)
		}, "not private"},
		{"a list with a second name", func(t *testing.T, w *settingsWorld) string {
			if err := os.Link(keysPathOf(w.state), filepath.Join(w.owner, "keys.again")); err != nil {
				t.Fatal(err)
			}
			return keysPrefixOf(t, w)
		}, "second name"},
		{"a list that is a final symbolic link", func(t *testing.T, w *settingsWorld) string {
			p := keysPrefixOf(t, w)
			real := filepath.Join(w.owner, "keys.real")
			if err := os.Rename(keysPathOf(w.state), real); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(real, keysPathOf(w.state)); err != nil {
				t.Fatal(err)
			}
			return p
		}, "symbolic link"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, tmp := keysWorld(t)
			prefix := c.change(t, w)
			before := map[string][]byte{}
			for _, n := range []string{ownerjournal.KeysFileName, ownerjournal.KeysHeadFileName} {
				if b, err := os.ReadFile(filepath.Join(w.owner, n)); err == nil {
					before[n] = b
				}
			}
			folder := ownerFolder(t, w)
			code, out, _ := keysRun(t, w, "--keys-since", prefix)
			if code != leaddaemon.ExitFailure || !strings.Contains(out, "keys: CHANGED: ") || !strings.Contains(out, c.want) || strings.Contains(out, "keys: holds") {
				t.Errorf("exit %d, want 1, CHANGED and %q:\n%s", code, c.want, out)
			}
			for n, b := range before {
				if !bytes.Equal(fileBytes(t, filepath.Join(w.owner, n)), b) {
					t.Errorf("the check changed owner/%s", n)
				}
			}
			if after := ownerFolder(t, w); fmt.Sprint(after) != fmt.Sprint(folder) {
				t.Errorf("the check changed the owner/ folder:\n%v\n%v", folder, after)
			}
			nothingInTmp(t, tmp)
			if strings.Contains(c.want, "list of step g") || strings.Contains(c.want, "shorter") {
				return
			}
			code, out, msg := keysRun(t, w, "--keys-prefix")
			if code != leaddaemon.ExitFailure || out != "" || !strings.Contains(msg, c.want) {
				t.Errorf("--keys-prefix: exit %d: %q %q", code, out, msg)
			}
		})
	}
}
