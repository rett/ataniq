package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"ataniq/internal/assignment"
	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/durable"
	"ataniq/internal/checkpoint/link"
	"ataniq/internal/seatgate"
	"golang.org/x/sys/unix"
)

type config struct {
	Identity   durable.Identity `json:"identity"`
	Slot       cp.Slot          `json:"slot"`
	State      string           `json:"state"`
	Seed       string           `json:"seed"`
	Guard      string           `json:"guard"`
	Socket     string           `json:"socket"`
	ServiceTLS string           `json:"service_tls"`
	OwnerTLS   string           `json:"owner_tls"`
	ServicePin string           `json:"service_tls_pin"`
	OwnerPin   string           `json:"owner_tls_pin"`
	HomePin    string           `json:"home_tls_pin"`
	Home       string           `json:"home_endpoint"`
	SigningKey string           `json:"signing_public_key"`
	HomeKey    string           `json:"home_public_key"`
}

var (
	errConfig   = errors.New("invalid witness configuration")
	errFiles    = errors.New("unsafe or missing witness file or directory")
	errIdentity = errors.New("witness identity or pin mismatch")
)

func absolute(p string) bool {
	return filepath.IsAbs(p) && filepath.Clean(p) == p && p != "/" && !strings.ContainsRune(p, 0)
}

// Pin each directory component before opening its child. Trusted sticky parents
// allow private temporary trees. The leaf file is checked on its open descriptor.
func directory(p string, private bool) (*os.File, error) {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return nil, errFiles
	}
	const flags = unix.O_PATH | unix.O_CLOEXEC | unix.O_DIRECTORY | unix.O_NOFOLLOW
	fd, err := unix.Open("/", flags, 0)
	if err != nil {
		return nil, errFiles
	}
	for _, part := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' }) {
		next, err := unix.Openat(fd, part, flags, 0)
		unix.Close(fd)
		if err != nil {
			return nil, errFiles
		}
		fd = next
		var st unix.Stat_t
		if unix.Fstat(fd, &st) != nil || (st.Uid != 0 && st.Uid != uint32(os.Geteuid())) || (st.Mode&0022 != 0 && st.Mode&unix.S_ISVTX == 0) {
			unix.Close(fd)
			return nil, errFiles
		}
	}
	f := os.NewFile(uintptr(fd), p)
	if private {
		var st unix.Stat_t
		if unix.Fstat(fd, &st) != nil || st.Uid != uint32(os.Geteuid()) || st.Mode != unix.S_IFDIR|0700 {
			f.Close()
			return nil, errFiles
		}
	}
	return f, nil
}

func openRegular(dir *os.File, name string, private bool) (*os.File, error) {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errFiles
	}
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 ||
		(st.Uid != 0 && st.Uid != uint32(os.Geteuid())) || st.Mode&0022 != 0 ||
		(private && (st.Uid != uint32(os.Geteuid()) || st.Mode != unix.S_IFREG|0600)) {
		unix.Close(fd)
		return nil, errFiles
	}
	return os.NewFile(uintptr(fd), name), nil
}

func readFile(p string, limit int64, private bool) ([]byte, error) {
	if !absolute(p) {
		return nil, errFiles
	}
	d, err := directory(filepath.Dir(p), false)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	f, err := openRegular(d, filepath.Base(p), private)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, errFiles
	}
	return b, nil
}

func loadConfig(path string) (config, durable.Config, error) {
	var c config
	var store durable.Config
	b, err := readFile(path, 64<<10, false)
	if err != nil {
		return c, store, err
	}
	if !utf8.Valid(b) || seatgate.DecodeStrict(b, &c) != nil {
		return c, store, errConfig
	}
	for _, p := range []string{c.State, c.Seed, c.Guard, c.ServiceTLS, c.OwnerTLS, c.Socket} {
		if !absolute(p) {
			return c, store, errConfig
		}
	}
	for _, v := range []string{c.Identity.Witness, c.Identity.Store, c.Identity.Incarnation, c.Slot.Authority, c.Slot.Store} {
		if len(v) == 0 || len(v) > assignment.MaxReferenceBytes {
			return c, store, errConfig
		}
	}
	if c.Slot.Witness != c.Identity.Witness || c.Identity.Epoch == 0 || c.ServiceTLS == c.OwnerTLS {
		return c, store, errConfig
	}
	for _, p := range []string{c.ServicePin, c.OwnerPin, c.HomePin, c.SigningKey, c.HomeKey} {
		if !seatgate.ValidFingerprint(p) || p == strings.Repeat("0", 64) {
			return c, store, errConfig
		}
	}
	if c.ServicePin == c.OwnerPin || c.HomePin == c.OwnerPin || c.ServicePin == c.HomePin || c.SigningKey == c.HomeKey {
		return c, store, errConfig
	}
	// Use the same single numeric destination as the account's firewall rule.
	home, err := netip.ParseAddrPort(c.Home)
	if err != nil || home.Port() == 0 || home.Addr().Unmap().IsUnspecified() || home.Addr().IsMulticast() || home.Addr().Zone() != "" {
		return c, store, errConfig
	}
	store = durable.Config{Identity: c.Identity, Directory: c.State, KeyPath: c.Seed}
	store.Pin, _ = hex.DecodeString(c.SigningKey)
	store.HomePin, _ = hex.DecodeString(c.HomeKey)
	for _, pub := range []ed25519.PublicKey{store.Pin, store.HomePin} {
		if _, err := cp.KeyID(pub); err != nil {
			return c, store, errConfig
		}
	}
	return c, store, nil
}

func loadTLS(dir, pin string) (seatgate.Identity, error) {
	var id seatgate.Identity
	cert, err := readFile(filepath.Join(dir, seatgate.CertFile), 16<<10, false)
	if err != nil {
		return id, err
	}
	key, err := readFile(filepath.Join(dir, seatgate.KeyFile), 16<<10, true)
	if err != nil {
		return id, err
	}
	defer clear(key)
	id, err = seatgate.ParseIdentity(cert, key)
	if err != nil || id.Fingerprint() != pin {
		return seatgate.Identity{}, errIdentity
	}
	return id, nil
}

func serviceConfig(c config, store durable.Config) (*link.Server, error) {
	seed, err := readFile(store.KeyPath, ed25519.SeedSize, true)
	if err != nil {
		return nil, err
	}
	defer clear(seed)
	if len(seed) != ed25519.SeedSize {
		return nil, errIdentity
	}
	key := ed25519.NewKeyFromSeed(seed)
	defer clear(key)
	if !bytes.Equal(key.Public().(ed25519.PublicKey), store.Pin) {
		return nil, errIdentity
	}
	id, err := loadTLS(c.ServiceTLS, c.ServicePin)
	if err != nil {
		return nil, err
	}
	return link.NewServer(id, store.Pin, link.Remote{Network: "tcp", Address: c.Home, ServerPin: c.HomePin}, c.Slot)
}

// Open re-proves the tail with writes. Preflight checks only the published
// files' metadata and leaves replay and locking to run.
func checkState(path string) error {
	d, err := directory(path, true)
	if err != nil {
		return err
	}
	defer d.Close()
	var st unix.Stat_t
	switch err := unix.Fstatat(int(d.Fd()), "poisoned", &st, unix.AT_SYMLINK_NOFOLLOW); {
	case err == nil:
		return durable.ErrPoisoned
	case !errors.Is(err, unix.ENOENT):
		return errFiles
	}
	for _, name := range []string{"journal.v1", "witness.lock"} {
		f, err := openRegular(d, name, true)
		if err != nil {
			return err
		}
		st, err := f.Stat()
		f.Close()
		if err != nil || st.Size() == 0 {
			return errFiles
		}
	}
	return nil
}
