//go:build linux

package enrollment

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"

	"ataniq/internal/checkpoint"
)

// Version 1 stores the installation record in declaration order, using Go's
// compact JSON encoding (including padded base64 for byte slices).
type bindingFile struct {
	Version int     `json:"version"`
	Binding Binding `json:"binding"`
}

// ValidateBinding authenticates the original enrollment and checks the named
// ledger's root, inode and header. The ledger can already contain events.
func ValidateBinding(b Binding) error {
	if !utf8.ValidString(b.RootPath) || !filepath.IsAbs(b.RootPath) || b.RootPath != filepath.Clean(b.RootPath) {
		return refuse(Insecure, errors.New("invalid ledger root path"))
	}
	if bytes.Equal(b.HomePin, b.WitnessPin) {
		return checkpoint.ErrRefused
	}
	q, err := b.request()
	if err != nil {
		return err
	}
	body, err := q.Body("")
	if err != nil {
		return err
	}
	if err = checkpoint.MatchReceipt(b.WitnessPin, b.Epoch, "enroll", body, b.Receipt); err != nil {
		return err
	}

	f, err := (FileRoot{b.RootPath}).openRoot()
	if err != nil {
		return err
	}
	defer f.close()
	r, identity, err := f.readLedger()
	if err != nil {
		return err
	}
	if identity != b.Identity {
		return refuse(IdentityMismatch, nil)
	}
	header := make([]byte, len(q.HeaderBytes))
	if _, err = io.ReadFull(r, header); err != nil || !bytes.Equal(header, q.HeaderBytes) {
		return refuse(IdentityMismatch, err)
	}
	return f.check()
}

// EncodeBinding validates the binding against the ledger before encoding it.
func EncodeBinding(b Binding) ([]byte, error) {
	if err := ValidateBinding(b); err != nil {
		return nil, err
	}
	return json.Marshal(bindingFile{1, b})
}

// DecodeBinding accepts an exact v1 document and verifies its ledger pin.
func DecodeBinding(raw []byte) (Binding, error) {
	var doc bindingFile
	if err := exactJSON(raw, &doc); err != nil {
		return Binding{}, err
	}
	if doc.Version != 1 {
		return Binding{}, checkpoint.ErrRefused
	}
	if err := ValidateBinding(doc.Binding); err != nil {
		return Binding{}, err
	}
	return doc.Binding, nil
}

// bindingParent opens the destination through real directories. Root-owned
// sticky temporary directories can contain an owner's private directory.
func bindingParent(path string) (*fileLedger, error) {
	if !filepath.IsAbs(path) || path != filepath.Clean(path) || path == "/" {
		return nil, refuse(Insecure, errors.New("invalid binding path"))
	}
	dir, err := openDirectory(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	// The filesystem root is the anchor for the walk. Verify each child.
	components := append(dir.parents, dir.dir)
	for _, f := range components[1:] {
		var st syscall.Stat_t
		if err = syscall.Fstat(int(f.Fd()), &st); err != nil || (st.Uid != 0 && int(st.Uid) != os.Geteuid()) || (st.Mode&0022 != 0 && st.Mode&syscall.S_ISVTX == 0) {
			return nil, errors.Join(refuse(Insecure, errors.New("unprotected binding directory")), err, dir.close())
		}
	}
	return dir, nil
}

func outsideRoot(path, root string) error {
	if root == "/" || path == root || strings.HasPrefix(path, root+"/") {
		return refuse(Insecure, errors.New("binding must live outside the ledger root"))
	}
	return nil
}

// WriteBinding exclusively publishes and syncs a mode-0600 binding in a
// separately protected restore domain. An existing destination is preserved.
func WriteBinding(path string, b Binding) error {
	raw, err := EncodeBinding(b)
	if err != nil {
		return err
	}
	if err = outsideRoot(path, b.RootPath); err != nil {
		return err
	}
	dir, err := bindingParent(path)
	if err != nil {
		return err
	}
	defer dir.close()
	if err = dir.publish(filepath.Base(path), func(syscall.Stat_t) ([]byte, error) { return raw, nil }); err != nil {
		return err
	}
	return dir.checkPath()
}

// ReadBinding checks file ownership and mode before parsing the installation record.
func ReadBinding(path string) (Binding, error) {
	dir, err := bindingParent(path)
	if err != nil {
		return Binding{}, err
	}
	defer dir.close()
	raw, err := dir.readOptional(filepath.Base(path))
	if err != nil {
		return Binding{}, err
	}
	if raw == nil {
		return Binding{}, os.ErrNotExist
	}
	b, err := DecodeBinding(raw)
	if err != nil {
		return Binding{}, err
	}
	if err = outsideRoot(path, b.RootPath); err != nil {
		return Binding{}, err
	}
	if err = dir.checkPath(); err != nil {
		return Binding{}, err
	}
	return b, nil
}
