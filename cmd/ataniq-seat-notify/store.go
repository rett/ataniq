package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unicode/utf8"

	"ataniq/internal/seatnotice"
)

// maxSubscriptions is the most subscriptions the store keeps: a phone and a
// few more devices of one owner.
const maxSubscriptions = 4

// maxStoreFile bounds the store file: four subscriptions of the largest
// endpoint the wire allows come to under 12 KiB.
const maxStoreFile = 16 << 10

// storeVersion is the only version of the file's format.
const storeVersion = 1

// The store's errors are fixed texts: none holds a path, an endpoint or a
// key.
var (
	errTooMany     = errors.New("the store holds the most subscriptions it keeps")
	errStoreUnsafe = errors.New("the subscriptions folder or file is not safe to use (a real folder and a regular file of this user, the file mode 0600, no link)")
	errStoreFormat = errors.New("the subscriptions file is not what this notifier writes")
	errStoreIO     = errors.New("the subscriptions file cannot be read or written")
)

// store keeps the push subscriptions in a JSON file of mode 0600, in a
// folder the notifier owns. Reading is strict: a file that is not exactly
// what the store writes is refused, never repaired. Writing is atomic: a new
// file is written next to the old one, synced and renamed over it, then the
// folder is synced, so a crash leaves the old file or the new, never half.
type store struct {
	path  string
	allow func(endpoint string) bool

	mu   sync.Mutex
	subs []seatnotice.Subscription
}

// fileSub and file are the file's shape; the order of the fields is the
// order written.
type fileSub struct {
	Endpoint string `json:"endpoint"`
	P256DH   string `json:"p256dh"`
	Auth     string `json:"auth"`
}

type file struct {
	Version       int       `json:"version"`
	Subscriptions []fileSub `json:"subscriptions"`
}

// openStore reads the store at path; a file that is not there is an empty
// store. allow says whether an endpoint is one the notifier may send to: a
// file that holds any other is refused whole.
func openStore(path string, allow func(string) bool) (*store, error) {
	s := &store{path: path, allow: allow}
	dir := filepath.Dir(path)
	if err := checkOwnDir(dir); err != nil {
		return nil, err
	}
	// A temporary file a crash left behind is this store's own, in its own
	// folder; remove it.
	if old, _ := filepath.Glob(filepath.Join(dir, s.tempPattern())); len(old) > 0 {
		for _, p := range old {
			if st, err := os.Lstat(p); err == nil && st.Mode().IsRegular() {
				os.Remove(p)
			}
		}
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return s, nil
		}
		if errors.Is(err, syscall.ELOOP) {
			return nil, errStoreUnsafe
		}
		return nil, errStoreIO
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, errStoreIO
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.Mode().IsRegular() || st.Mode().Perm() != 0o600 || sys.Nlink != 1 || sys.Uid != uint32(os.Geteuid()) || st.Size() > maxStoreFile {
		return nil, errStoreUnsafe
	}
	data, err := io.ReadAll(io.LimitReader(f, maxStoreFile+1))
	if err != nil || len(data) > maxStoreFile {
		return nil, errStoreIO
	}
	subs, err := parseStore(data, allow)
	if err != nil {
		return nil, err
	}
	s.subs = subs
	return s, nil
}

// checkOwnDir holds the store's folder to being a real folder (not a link),
// owned by this user, that group and other cannot write.
func checkOwnDir(dir string) error {
	st, err := os.Lstat(dir)
	if err != nil {
		return errStoreIO
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.IsDir() || st.Mode()&fs.ModeSymlink != 0 || st.Mode().Perm()&0o022 != 0 || sys.Uid != uint32(os.Geteuid()) {
		return errStoreUnsafe
	}
	return nil
}

// tempPattern is the name pattern of the temporary files of the store's
// writes: a hidden file of the store's own name.
func (s *store) tempPattern() string {
	return "." + filepath.Base(s.path) + "-*.tmp"
}

// parseStore reads the file's bytes: one JSON object with exactly the keys
// version (1) and subscriptions (an array of at most four objects with
// exactly the keys endpoint, p256dh and auth, all strings), each key once
// and in its exact case, nothing but white space after it; every
// subscription valid for the wire, allowed by allow, and no endpoint twice.
func parseStore(data []byte, allow func(string) bool) ([]seatnotice.Subscription, error) {
	if !utf8.Valid(data) || !json.Valid(data) {
		return nil, errStoreFormat
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	bad := func() ([]seatnotice.Subscription, error) { return nil, errStoreFormat }
	if !isDelim(dec, '{') {
		return bad()
	}
	var subs []seatnotice.Subscription
	var sawVersion, sawSubs bool
	for dec.More() {
		tok, err := dec.Token()
		key, ok := tok.(string)
		if err != nil || !ok {
			return bad()
		}
		switch {
		case key == "version" && !sawVersion:
			sawVersion = true
			tok, err := dec.Token()
			if n, ok := tok.(json.Number); err != nil || !ok || n.String() != "1" {
				return bad()
			}
		case key == "subscriptions" && !sawSubs:
			sawSubs = true
			if !isDelim(dec, '[') {
				return bad()
			}
			for dec.More() {
				if len(subs) >= maxSubscriptions {
					return bad()
				}
				sub, ok := readSub(dec)
				if !ok {
					return bad()
				}
				subs = append(subs, sub)
			}
			if !isDelim(dec, ']') {
				return bad()
			}
		default:
			return bad()
		}
	}
	if !isDelim(dec, '}') || !sawVersion || !sawSubs {
		return bad()
	}
	if _, err := dec.Token(); err != io.EOF {
		return bad()
	}
	seen := map[string]bool{}
	for _, sub := range subs {
		h := seatnotice.EndpointSHA256(sub.Endpoint)
		if seatnotice.ValidateSubscription(sub) != nil || !allow(sub.Endpoint) || seen[h] {
			return bad()
		}
		seen[h] = true
	}
	return subs, nil
}

func isDelim(dec *json.Decoder, d json.Delim) bool {
	tok, err := dec.Token()
	got, ok := tok.(json.Delim)
	return err == nil && ok && got == d
}

// readSub reads one subscription object: exactly the three keys, each once,
// each a string.
func readSub(dec *json.Decoder) (seatnotice.Subscription, bool) {
	var sub seatnotice.Subscription
	if !isDelim(dec, '{') {
		return sub, false
	}
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		key, ok := tok.(string)
		if err != nil || !ok || seen[key] {
			return sub, false
		}
		seen[key] = true
		val, err := dec.Token()
		str, ok := val.(string)
		if err != nil || !ok {
			return sub, false
		}
		switch key {
		case "endpoint":
			sub.Endpoint = str
		case "p256dh":
			sub.P256DH = str
		case "auth":
			sub.Auth = str
		default:
			return sub, false
		}
	}
	if !isDelim(dec, '}') || len(seen) != 3 {
		return sub, false
	}
	return sub, true
}

// count is how many subscriptions the store holds.
func (s *store) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs)
}

// list returns a copy of the subscriptions, in the order they were made.
func (s *store) list() []seatnotice.Subscription {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]seatnotice.Subscription(nil), s.subs...)
}

// put keeps sub, in the place of the one with the same endpoint when there
// is one (even when the store is full), else as a new one, or errTooMany
// when four are kept already. The file is written first: when that fails the
// store is as it was.
func (s *store) put(sub seatnotice.Subscription) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := append([]seatnotice.Subscription(nil), s.subs...)
	replaced := false
	for i := range next {
		if next[i].Endpoint == sub.Endpoint {
			next[i] = sub
			replaced = true
		}
	}
	if !replaced {
		if len(next) >= maxSubscriptions {
			return errTooMany
		}
		next = append(next, sub)
	}
	committed, err := s.write(next)
	if committed {
		s.subs = next
	}
	return err
}

// remove drops the subscriptions whose endpoint hashes are given, and
// returns how many it dropped. A hash that matches none is not an error.
func (s *store) remove(hashes ...string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	drop := map[string]bool{}
	for _, h := range hashes {
		drop[h] = true
	}
	var next []seatnotice.Subscription
	for _, sub := range s.subs {
		if !drop[seatnotice.EndpointSHA256(sub.Endpoint)] {
			next = append(next, sub)
		}
	}
	n := len(s.subs) - len(next)
	if n == 0 {
		return 0, nil
	}
	committed, err := s.write(next)
	if committed {
		s.subs = next
		return n, err
	}
	return 0, err
}

// write replaces the file with subs: a new file of mode 0600 in the same
// folder, written, synced, closed, renamed over the old one, and the folder
// synced. committed says the rename happened, so the new file is the store's
// whatever the folder's sync then said.
func (s *store) write(subs []seatnotice.Subscription) (committed bool, err error) {
	f := file{Version: storeVersion, Subscriptions: make([]fileSub, len(subs))}
	for i, sub := range subs {
		f.Subscriptions[i] = fileSub(sub)
	}
	data, err := json.Marshal(f)
	if err != nil {
		return false, errStoreIO
	}
	data = append(data, '\n')
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, strings.TrimSuffix(s.tempPattern(), "*.tmp")+"*.tmp")
	if err != nil {
		return false, errStoreIO
	}
	name := tmp.Name()
	renamed := false
	defer func() {
		if !renamed {
			tmp.Close()
			os.Remove(name)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return false, errStoreIO
	}
	if _, err := tmp.Write(data); err != nil {
		return false, errStoreIO
	}
	if err := tmp.Sync(); err != nil {
		return false, errStoreIO
	}
	if err := tmp.Close(); err != nil {
		return false, errStoreIO
	}
	if err := os.Rename(name, s.path); err != nil {
		return false, errStoreIO
	}
	renamed = true
	d, err := os.Open(dir)
	if err != nil {
		return true, errStoreIO
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return true, errStoreIO
	}
	return true, nil
}
