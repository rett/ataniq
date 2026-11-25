//go:build linux && ataniq_ownerchannel && ataniq_canary

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"ataniq/internal/approval/hostadapter"
	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/leaddaemon"
)

// This file is run --check --keys-prefix and run --check --keys-since PREFIX,
// in a build with both tags: the read-only check of the key list of the state
// folder (owner/keys.log) and its head (owner/keys.head) that the attended
// proof takes before the sitting (step g) and after it (step 13).
//
// The list and its head are judged by the journal's own reader,
// ownerjournal.OpenKeys, the code the daemon opens them with: the header, each
// record read by the strict reader of package record (no unknown, repeated or
// null key, nothing after the object), the records' numbers in sequence, the
// digest chain, the rules of the list (a counter rises, a revocation names a
// credential on the list ...), and the head's count and digest. OpenKeys
// takes a lock and makes the head right when it lags the list, so it is never
// run on the state folder's files: their bytes are read, without following a
// link, and the reader runs on a private copy in a temporary folder of its
// own, which is removed afterwards. Nothing of the state folder is written. A
// head the reader had to make right is not the head of the list: it is
// refused here, so only a head that matches the list passes.
//
// --keys-prefix prints the list's prefix, its length in bytes and the SHA-256
// of those bytes ("LEN:SHA256"), once the list and its head pass. The owner
// records it at step g. --keys-since PREFIX then checks, at step 13, that the
// list and its head pass, that the recorded prefix is the list's first LEN
// bytes, ending at a whole record, and names the type of each record appended
// after it, read from the records the reader has checked. Counter records
// alone (a phone login with a passkey whose sign counter rises) pass; any
// other record (added: a pairing; revoked: a revocation; rejected: a refused
// pairing), a changed or missing old byte, and any damage of the list or its
// head is CHANGED.

// maxKeysBytes bounds the list read: the journal's own bound is 4 MiB.
const maxKeysBytes = 4 << 20

// maxKeysHeadBytes bounds the head read: the journal's own bound is 256 bytes.
const maxKeysHeadBytes = 256

// keysPrefix is a recorded prefix of the key list: its length in bytes and
// the SHA-256 of those bytes.
type keysPrefix struct {
	size int
	sum  [sha256.Size]byte
}

func (p keysPrefix) String() string {
	return strconv.Itoa(p.size) + ":" + hex.EncodeToString(p.sum[:])
}

// parseKeysPrefix reads "LEN:SHA256": a decimal length without a leading
// zero, a colon and 64 lower-case hexadecimal digits.
func parseKeysPrefix(s string) (keysPrefix, error) {
	bad := fmt.Errorf("the prefix %q is not LEN:SHA256 (a length in bytes, a colon and 64 lower-case hexadecimal digits)", s)
	n, sum, ok := strings.Cut(s, ":")
	if !ok || n == "" || len(n) > 8 || (len(n) > 1 && n[0] == '0') || len(sum) != 2*sha256.Size {
		return keysPrefix{}, bad
	}
	for _, c := range n {
		if c < '0' || c > '9' {
			return keysPrefix{}, bad
		}
	}
	for _, c := range sum {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return keysPrefix{}, bad
		}
	}
	var p keysPrefix
	p.size, _ = strconv.Atoi(n)
	if _, err := hex.Decode(p.sum[:], []byte(sum)); err != nil {
		return keysPrefix{}, bad
	}
	return p, nil
}

// trialKeys is the key list of the state folder as the journal's reader
// passed it: its bytes and its number of records.
type trialKeys struct {
	data    []byte
	records int
}

// readPlain reads a regular file of at most max bytes without following a
// link. It reports false when there is no file.
func readPlain(path string, max int64) ([]byte, bool, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	switch {
	case errors.Is(err, unix.ENOENT):
		return nil, false, nil
	case errors.Is(err, unix.ELOOP):
		return nil, true, fmt.Errorf("%s is a link", path)
	case err != nil:
		return nil, true, fmt.Errorf("%s cannot be opened: %v", path, err)
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, true, err
	}
	if !st.Mode().IsRegular() {
		return nil, true, fmt.Errorf("%s is not a regular file", path)
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, true, fmt.Errorf("%s cannot be read: %v", path, err)
	}
	if int64(len(b)) > max {
		return nil, true, fmt.Errorf("%s is larger than %d bytes", path, max)
	}
	return b, true, nil
}

// readTrialKeys reads owner/keys.log and owner/keys.head of the state folder
// and has the journal's own reader judge them, on a private copy. It writes
// nothing in the state folder.
func readTrialKeys(stateDir string) (trialKeys, error) {
	owner := filepath.Join(stateDir, hostadapter.OwnerDirName)
	logPath := filepath.Join(owner, ownerjournal.KeysFileName)
	headPath := filepath.Join(owner, ownerjournal.KeysHeadFileName)
	data, ok, err := readPlain(logPath, maxKeysBytes)
	if err != nil {
		return trialKeys{}, err
	}
	if !ok {
		return trialKeys{}, fmt.Errorf("there is no key list %s", logPath)
	}
	head, ok, err := readPlain(headPath, maxKeysHeadBytes)
	if err != nil {
		return trialKeys{}, err
	}
	if !ok {
		return trialKeys{}, fmt.Errorf("there is no head %s of the key list", headPath)
	}
	tmp, err := os.MkdirTemp("", "ataniqd-keys-check-")
	if err != nil {
		return trialKeys{}, fmt.Errorf("a private folder for the copy of the key list cannot be made: %v", err)
	}
	defer os.RemoveAll(tmp)
	if err := os.Chmod(tmp, 0o700); err != nil {
		return trialKeys{}, err
	}
	cLog := filepath.Join(tmp, ownerjournal.KeysFileName)
	cHead := filepath.Join(tmp, ownerjournal.KeysHeadFileName)
	if err := os.WriteFile(cLog, data, 0o600); err != nil {
		return trialKeys{}, err
	}
	if err := os.WriteFile(cHead, head, 0o600); err != nil {
		return trialKeys{}, err
	}
	k, err := ownerjournal.OpenKeys(cLog)
	if err != nil {
		return trialKeys{}, fmt.Errorf("the journal's reader refuses the key list or its head: %v", err)
	}
	if err := k.Close(); err != nil {
		return trialKeys{}, err
	}
	after, err := os.ReadFile(cHead)
	if err != nil || !bytes.Equal(after, head) {
		return trialKeys{}, fmt.Errorf("the head %s does not count the whole list (the daemon would make it right at its next open): it is not the head of the list", headPath)
	}
	return trialKeys{data: data, records: bytes.Count(data, []byte{'\n'}) - 1}, nil
}

// prefixOf is the list's prefix of its whole length.
func (k trialKeys) prefixOf() keysPrefix {
	return keysPrefix{size: len(k.data), sum: sha256.Sum256(k.data)}
}

// appendedSince returns the type of each record of the list after the
// prefix p, in order, or an error when p is not a prefix of the list ending
// at a whole record.
func (k trialKeys) appendedSince(p keysPrefix) ([]string, error) {
	if p.size > len(k.data) {
		return nil, fmt.Errorf("the list is %d bytes, shorter than the %d of step g: records were removed", len(k.data), p.size)
	}
	if p.size == 0 || k.data[p.size-1] != '\n' || sha256.Sum256(k.data[:p.size]) != p.sum {
		return nil, fmt.Errorf("its first %d bytes are not the list of step g: a record of it was changed or removed", p.size)
	}
	var kinds []string
	rest := k.data[p.size:]
	for len(rest) > 0 {
		var line []byte
		line, rest, _ = bytes.Cut(rest, []byte{'\n'})
		_, rec, ok := bytes.Cut(line, []byte{' '})
		var r struct {
			Type string `json:"type"`
		}
		if !ok || json.Unmarshal(rec, &r) != nil || r.Type == "" {
			return nil, errors.New("a record appended since step g has no type")
		}
		kinds = append(kinds, r.Type)
	}
	return kinds, nil
}

// keysCheck is run --check --keys-prefix or --keys-since: the key list of the
// state folder alone. It ends 0 when the list holds, 1 when it does not, and 2
// for a prefix that cannot be read.
func (a *app) keysCheck(s settingsFlags, stateDir string) int {
	owner := filepath.Join(stateDir, hostadapter.OwnerDirName)
	if s.keysPrefix {
		k, err := readTrialKeys(stateDir)
		if err != nil {
			fmt.Fprintf(a.stderr, "ataniqd run: --keys-prefix: %s\n", leaddaemon.SafeText(err.Error()))
			return leaddaemon.ExitFailure
		}
		fmt.Fprintln(a.stdout, k.prefixOf().String())
		return leaddaemon.ExitOK
	}
	p, err := parseKeysPrefix(s.keysSince)
	if err != nil {
		fmt.Fprintf(a.stderr, "ataniqd run: --keys-since: %s\n", leaddaemon.SafeText(err.Error()))
		return leaddaemon.ExitUsage
	}
	w := a.stdout
	changed := func(format string, v ...any) int {
		fmt.Fprintf(w, "keys: CHANGED: %s\n", leaddaemon.SafeText(fmt.Sprintf(format, v...)))
		return leaddaemon.ExitFailure
	}
	k, err := readTrialKeys(stateDir)
	if err != nil {
		return changed("%v", err)
	}
	fmt.Fprintf(w, "keys  %s: %d record(s), each well formed, in sequence, on the digest chain and within the rules of the list, and its head %s counts them and holds the last digest (the journal's own reader, on a private copy)\n",
		leaddaemon.SafeText(filepath.Join(owner, ownerjournal.KeysFileName)), k.records, ownerjournal.KeysHeadFileName)
	kinds, err := k.appendedSince(p)
	if err != nil {
		return changed("keys.log is not the list of step g (%s) with records appended: %v", p, err)
	}
	if len(kinds) == 0 {
		fmt.Fprintf(w, "keys  since step g (%d bytes): no record was appended\n", p.size)
		fmt.Fprintln(w, "keys: holds: the key list is the list of step g")
		return leaddaemon.ExitOK
	}
	fmt.Fprintf(w, "keys  since step g (%d bytes): %d record(s) appended: %s\n", p.size, len(kinds), strings.Join(kinds, " "))
	for _, t := range kinds {
		if t != "counter" {
			return changed("a record that is not a counter was appended to the key list (added: a pairing; revoked: a revocation; rejected: a refused pairing): a finding unless you made it during the sitting")
		}
	}
	fmt.Fprintln(w, "keys: holds: only counter records were appended to the list of step g (a phone login with a passkey whose sign counter rises)")
	return leaddaemon.ExitOK
}
