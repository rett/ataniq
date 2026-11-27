package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"ataniq/internal/seatgate"
	"golang.org/x/sys/unix"
)

func identityInit(c config) (retErr error) {
	parent, err := directory(filepath.Dir(c.HomeKey), true)
	if err != nil {
		return fmt.Errorf("home_key: %w", err)
	}
	defer parent.Close()
	keyFD, keyName := int(parent.Fd()), filepath.Base(c.HomeKey)
	var st unix.Stat_t
	if err := unix.Fstatat(keyFD, keyName, &st, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(err, unix.ENOENT) {
		return errors.New("home_key: destination exists or cannot be checked")
	}

	tlsParent, err := directory(filepath.Dir(c.TLSDir), false)
	if err != nil {
		return fmt.Errorf("tls_dir: %w", err)
	}
	defer tlsParent.Close()
	fi, err := tlsParent.Stat()
	if err != nil || fi.Mode().Perm()&0022 != 0 {
		return errors.New("tls_dir: unsafe parent directory")
	}
	tlsFD, tlsName := int(tlsParent.Fd()), filepath.Base(c.TLSDir)
	if err := unix.Mkdirat(tlsFD, tlsName, 0700); err != nil {
		return errors.New("tls_dir: destination exists or cannot be created")
	}
	defer func() {
		if retErr != nil {
			if err := unix.Unlinkat(tlsFD, tlsName, unix.AT_REMOVEDIR); err != nil {
				retErr = errors.Join(retErr, errors.New("tls_dir: could not remove incomplete identity directory"))
			}
		}
	}()
	fd, err := unix.Openat(tlsFD, tlsName, unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_RDONLY, 0)
	if err != nil {
		return errors.New("tls_dir: cannot open new directory")
	}
	dir := os.NewFile(uintptr(fd), tlsName)
	defer dir.Close()
	if err := dir.Chmod(0700); err != nil {
		return errors.New("tls_dir: cannot set private mode")
	}

	// Keep the seatgate writer inside the directory we just opened. The
	// final dot lets its Lstat inspect the directory behind the proc fd.
	_, err = seatgate.WriteIdentityFiles(fmt.Sprintf("/proc/self/fd/%d/.", fd), "ataniq-home")
	if err != nil {
		return fmt.Errorf("tls_dir: %w", err)
	}
	defer func() {
		if retErr != nil {
			for _, name := range []string{seatgate.KeyFile, seatgate.CertFile} {
				if err := unix.Unlinkat(fd, name, 0); err != nil {
					retErr = errors.Join(retErr, errors.New("tls_dir: could not remove incomplete identity file"))
				}
			}
		}
	}()
	if _, err := readTLS(dir); err != nil {
		return fmt.Errorf("tls_dir: %w", err)
	}
	return createSeed(keyFD, keyName)
}

func createSeed(parent int, name string) error {
	var seed [ed25519.SeedSize]byte
	rand.Read(seed[:])
	defer clear(seed[:])
	fd, err := unix.Openat(parent, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return errors.New("home_key: destination exists or cannot be created")
	}
	f := os.NewFile(uintptr(fd), name)
	err = f.Chmod(0600)
	if err == nil {
		_, err = f.Write(seed[:])
	}
	if cerr := f.Close(); err != nil || cerr != nil {
		if unix.Unlinkat(parent, name, 0) != nil {
			return errors.New("home_key: could not remove incomplete seed")
		}
		return errors.New("home_key: could not write seed")
	}
	return nil
}

func readTLS(dir *os.File) (seatgate.Identity, error) {
	key, err := readRegular(dir, seatgate.KeyFile, 16<<10, true)
	if err != nil {
		return seatgate.Identity{}, err
	}
	defer clear(key)
	cert, err := readRegular(dir, seatgate.CertFile, 16<<10, false)
	if err != nil {
		return seatgate.Identity{}, err
	}
	return seatgate.ParseIdentity(cert, key)
}

type homeIdentity struct {
	tls seatgate.Identity
	key ed25519.PrivateKey
}

func loadHomeKey(c config) (ed25519.PrivateKey, error) {
	seed, err := readFile(c.HomeKey, ed25519.SeedSize, true)
	defer clear(seed)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("home_key: expected a private 0600 file containing one raw 32-byte seed")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func loadIdentity(c config) (homeIdentity, error) {
	var id homeIdentity
	key, err := loadHomeKey(c)
	if err != nil {
		return id, err
	}
	d, err := directory(c.TLSDir, true)
	if err != nil {
		clear(key)
		return id, fmt.Errorf("tls_dir: %w", err)
	}
	defer d.Close()
	id.tls, err = readTLS(d)
	if err != nil {
		clear(key)
		return id, fmt.Errorf("tls_dir: %w", err)
	}
	id.key = key
	return id, nil
}

func printPins(c config, out io.Writer) error {
	id, err := loadIdentity(c)
	if err != nil {
		return err
	}
	defer clear(id.key)
	_, err = fmt.Fprintf(out, "home_tls_pin=%s\nhome_public_key=%x\n", id.tls.Fingerprint(), id.key.Public().(ed25519.PublicKey))
	return err
}
