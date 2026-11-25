//go:build linux && ataniq_ownerchannel && ataniq_canary

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"ataniq/internal/approval/hostadapter"
	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/leaddaemon"
)

// This file is run --check --keys-prefix and run --check --keys-since PREFIX,
// in a build with both tags: the read-only check of the key list of the state
// folder (owner/keys.log) and its head (owner/keys.head) that the attended
// proof takes before the sitting (step g) and after it (step 13).
//
// The list and its head are judged by ownerjournal.VerifyKeys, the journal's
// own checks read only, on the files themselves: the owner/ folder opened once
// without following its last part, the list and the head opened by that
// folder's descriptor without following a link, and the ownership, mode and
// link-count checks the journal's opener makes, on the descriptors held (the
// folder the user's own with mode 0700, the files the user's with mode 0600
// and one name); then the list's bytes, read from the descriptor checked,
// under the opener's replay (the header, each record by the strict reader of
// package record, the numbers in sequence, the digest chain, the rules of the
// list), and a head that counts every record and holds the last digest.
// Nothing is created, repaired, locked or written, in the state folder or
// anywhere else: where the daemon's opener would make a list or a head, or
// make a lagging head right, the check refuses, so a list the daemon would
// refuse or change at its next open never holds here.
//
// --keys-prefix prints the list's prefix, its length in bytes and the SHA-256
// of those bytes ("LEN:SHA256"), once the list and its head pass. prepare.sh
// records it at step g. --keys-since PREFIX then checks, at step 13, that the
// list and its head pass, that the recorded prefix is the list's first LEN
// bytes, ending at a whole record, and names the type of each record appended
// after it, read from the records the check passed. Counter records alone (a
// phone login with a passkey whose sign counter rises) pass; any other record
// (added: a pairing; revoked: a revocation; rejected: a refused pairing), a
// changed or missing old byte, and any refusal of the list or its head is
// CHANGED. It is a guard for the owner's trial against his own mistakes, not
// against someone who can write the folder: the digests are not a signature.

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

// trialKeys is the key list of the state folder as the journal's checks
// passed it: its bytes and its number of records.
type trialKeys struct {
	data    []byte
	records int
}

// readTrialKeys has the journal's read-only checks judge owner/keys.log and
// owner/keys.head of the state folder. It writes nothing.
func readTrialKeys(stateDir string) (trialKeys, error) {
	logPath := filepath.Join(stateDir, hostadapter.OwnerDirName, ownerjournal.KeysFileName)
	snap, err := ownerjournal.VerifyKeys(logPath)
	switch {
	case errors.Is(err, ownerjournal.ErrHeadBehind):
		return trialKeys{}, fmt.Errorf("the head %s does not count the whole list (the daemon would make it right at its next open, or a record was appended while the check read): it is not the head of the list; check again (%v)", ownerjournal.KeysHeadFileName, err)
	case err != nil:
		return trialKeys{}, fmt.Errorf("the journal's checks refuse the key list or its head: %v", err)
	}
	return trialKeys{data: snap.Data, records: snap.Records}, nil
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
	fmt.Fprintf(w, "keys  %s: %d record(s), each well formed, in sequence, on the digest chain and within the rules of the list, and its head %s counts them and holds the last digest (the journal's own checks, read only)\n",
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
