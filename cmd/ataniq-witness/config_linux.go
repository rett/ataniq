package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"ataniq/internal/assignment"
	"ataniq/internal/authorityhost/noaicheck"
	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/durable"
	"ataniq/internal/checkpoint/link"
	"ataniq/internal/seatgate"
	"golang.org/x/sys/unix"
)

const guardDirectory = "/var/lib/authority-host"

type config struct {
	Identity   durable.Identity `json:"identity"`
	Slot       cp.Slot          `json:"slot"`
	State      string           `json:"state"`
	Seed       string           `json:"seed"`
	Socket     string           `json:"socket"`
	ServiceTLS string           `json:"service_tls"`
	OwnerTLS   string           `json:"owner_tls"`
	ServicePin string           `json:"service_tls_pin"`
	OwnerPin   string           `json:"owner_tls_pin"`
	HomePin    string           `json:"home_tls_pin"`
	Home       string           `json:"home_endpoint"`
	SigningKey string           `json:"signing_public_key"`
	HomeKey    string           `json:"home_public_key"`

	// Read only to give older configurations a specific refusal.
	ObsoleteGuard json.RawMessage `json:"guard,omitempty"`
}

var (
	errSocket   = errors.New("owner socket already present or unusable")
	errEgress   = errors.New("invalid witness egress drop-in")
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
	return readRegular(d, filepath.Base(p), limit, private)
}

func readRegular(d *os.File, name string, limit int64, private bool) ([]byte, error) {
	f, err := openRegular(d, name, private)
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

// DecodeStrict groups unknown keys and invalid field values into one refusal.
// Diagnose a rejected file using only fixed reasons and schema field names.
func configDecodeError(data []byte) error {
	var object json.RawMessage
	if err := seatgate.DecodeStrict(data, &object); err != nil {
		return fmt.Errorf("%w: %v", errConfig, err) // Structural violations contain no input.
	}
	// Unmarshal ignores unknown keys and accepts case variants. If it can
	// read this strict object, the exact key check is what failed.
	var known config
	if err := json.Unmarshal(data, &known); err != nil {
		var field *json.UnmarshalTypeError
		if errors.As(err, &field) && field.Field != "" {
			return fmt.Errorf("%w: invalid value for %s", errConfig, field.Field)
		}
		// The epoch is the one field with a ,string tag. Its decoder can
		// return an untyped error containing the supplied string.
		return fmt.Errorf("%w: identity.epoch must be an unsigned decimal string", errConfig)
	}
	return fmt.Errorf("%w: unknown key", errConfig)
}

func loadConfig(path string) (config, durable.Config, error) {
	var c config
	var store durable.Config
	b, err := readFile(path, 64<<10, false)
	if err != nil {
		return c, store, err
	}
	if !utf8.Valid(b) {
		return c, store, fmt.Errorf("%w: invalid UTF-8", errConfig)
	}
	b = bytes.TrimRight(b, " \t\n\v\f\r")
	if len(b) == 0 {
		return c, store, fmt.Errorf("%w: expected a JSON object", errConfig)
	}
	if err := seatgate.DecodeStrict(b, &c); err != nil {
		return c, store, configDecodeError(b)
	}
	if c.ObsoleteGuard != nil {
		return c, store, fmt.Errorf("%w: remove the obsolete guard key", errConfig)
	}
	type field struct{ name, value string }
	if !absolute(c.Socket) {
		return c, store, fmt.Errorf("%w: socket: expected a clean absolute path", errConfig)
	}
	// Each storage, key, guard and runtime location has a separate owner or lifecycle.
	paths := []field{
		{"state", c.State}, {"seed", c.Seed}, {"guard directory", guardDirectory},
		{"socket parent", filepath.Dir(c.Socket)}, {"service_tls", c.ServiceTLS}, {"owner_tls", c.OwnerTLS},
	}
	for i, p := range paths {
		if !absolute(p.value) {
			return c, store, fmt.Errorf("%w: %s: expected a clean absolute path below root", errConfig, p.name)
		}
		for _, other := range paths[:i] {
			if p.value == other.value || strings.HasPrefix(other.value, p.value+"/") || strings.HasPrefix(p.value, other.value+"/") {
				return c, store, fmt.Errorf("%w: %s overlaps %s", errConfig, p.name, other.name)
			}
		}
	}
	for _, f := range []field{
		{"identity.witness", c.Identity.Witness}, {"identity.store", c.Identity.Store},
		{"identity.incarnation", c.Identity.Incarnation}, {"slot.authority", c.Slot.Authority}, {"slot.store", c.Slot.Store},
	} {
		if len(f.value) == 0 || len(f.value) > assignment.MaxReferenceBytes {
			return c, store, fmt.Errorf("%w: invalid length for %s", errConfig, f.name)
		}
		for _, ch := range f.value {
			if ch < ' ' || ch > '~' {
				return c, store, fmt.Errorf("%w: %s must be printable ASCII", errConfig, f.name)
			}
		}
	}
	if c.Slot.Witness != c.Identity.Witness {
		return c, store, fmt.Errorf("%w: slot.witness must match identity.witness", errConfig)
	}
	if c.Identity.Epoch == 0 {
		return c, store, fmt.Errorf("%w: identity.epoch must be nonzero", errConfig)
	}
	for _, f := range []field{
		{"service_tls_pin", c.ServicePin}, {"owner_tls_pin", c.OwnerPin}, {"home_tls_pin", c.HomePin},
		{"signing_public_key", c.SigningKey}, {"home_public_key", c.HomeKey},
	} {
		if !seatgate.ValidFingerprint(f.value) || f.value == strings.Repeat("0", 64) {
			return c, store, fmt.Errorf("%w: %s must be 64 lowercase hex digits and nonzero", errConfig, f.name)
		}
	}
	if c.ServicePin == c.OwnerPin || c.HomePin == c.OwnerPin || c.ServicePin == c.HomePin {
		return c, store, fmt.Errorf("%w: TLS pins must be distinct", errConfig)
	}
	if c.SigningKey == c.HomeKey {
		return c, store, fmt.Errorf("%w: signing_public_key and home_public_key must be distinct", errConfig)
	}
	// Use the same single numeric destination as the account's firewall rule.
	if _, ok := noaicheck.WitnessHomeAddress(c.Home); !ok {
		return c, store, fmt.Errorf("%w: invalid home_endpoint", errConfig)
	}
	store = durable.Config{Identity: c.Identity, Directory: c.State, KeyPath: c.Seed}
	store.Pin, _ = hex.DecodeString(c.SigningKey)
	store.HomePin, _ = hex.DecodeString(c.HomeKey)
	if _, err := cp.KeyID(store.Pin); err != nil {
		return c, store, fmt.Errorf("%w: invalid signing_public_key", errConfig)
	}
	if _, err := cp.KeyID(store.HomePin); err != nil {
		return c, store, fmt.Errorf("%w: invalid home_public_key", errConfig)
	}
	return c, store, nil
}

func loadTLS(dir, pin string) (seatgate.Identity, error) {
	var id seatgate.Identity
	d, err := directory(dir, false)
	if err != nil {
		return id, err
	}
	defer d.Close()
	st, err := d.Stat()
	if err != nil || st.Mode().Perm()&0022 != 0 {
		return id, errFiles
	}
	cert, err := readRegular(d, seatgate.CertFile, 16<<10, false)
	if err != nil {
		return id, err
	}
	key, err := readRegular(d, seatgate.KeyFile, 16<<10, true)
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

// checkSocket verifies the conditions visible without binding or unlinking.
func checkSocket(path string) error {
	if len(path) >= len(unix.RawSockaddrUnix{}.Path) {
		return errSocket
	}
	dir, err := directory(filepath.Dir(path), true)
	if err != nil {
		return errSocket
	}
	defer dir.Close()
	fd := int(dir.Fd())
	if unix.Faccessat(fd, ".", unix.W_OK|unix.X_OK, unix.AT_EACCESS) != nil {
		return errSocket
	}
	var st unix.Stat_t
	if err := unix.Fstatat(fd, filepath.Base(path), &st, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(err, unix.ENOENT) {
		return errSocket
	}
	return nil
}
