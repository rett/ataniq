package enrollment

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
)

func TestBindingFileRoundTrip(t *testing.T) {
	f := enrolled(t)
	path := filepath.Join(t.TempDir(), "binding")
	check(t, WriteBinding(path, f.b))
	stat, err := os.Stat(path)
	check(t, err)
	if stat.Mode() != 0600 {
		t.Fatal(stat.Mode())
	}
	raw := contents(t, path)
	noPrivateMaterial(t, raw, f.home, f.witness)
	encoded, err := EncodeBinding(f.b)
	check(t, err)
	if !bytes.Equal(encoded, raw) {
		t.Fatal("file and encoded document differ")
	}
	if err = WriteBinding(path, f.b); !errors.Is(err, os.ErrExist) {
		t.Fatal("overwrote binding", err)
	}
	b, err := ReadBinding(path)
	check(t, err)
	if !reflect.DeepEqual(b, f.b) {
		t.Fatal("binding changed in transit")
	}
	f.b = b
	s := f.open(t, nil)
	check(t, appendOnly(f.ctx, s, f.seed(t, s, "one")))
	b, err = ReadBinding(path)
	check(t, err)
	if !reflect.DeepEqual(b, f.b) {
		t.Fatal("append changed binding")
	}
}

func TestBindingRefusesAlteredRecords(t *testing.T) {
	f := enrolled(t)
	raw, err := EncodeBinding(f.b)
	check(t, err)
	for _, bad := range [][]byte{
		append(bytes.Clone(raw), '\n'), bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":2`), 1),
		append([]byte(`{"other":null,`), raw[1:]...),
		bytes.Replace(raw, []byte(`"Epoch":1`), []byte(`"Epoch":1,"Epoch":1`), 1),
		bytes.Replace(raw, []byte(`"RootInode"`), []byte(`"rootinode"`), 1),
	} {
		if _, err = DecodeBinding(bad); err == nil {
			t.Fatal("accepted malformed document")
		}
	}
	for _, tc := range []struct {
		name   string
		change func(*Binding)
	}{
		{"root dev", func(b *Binding) { b.Identity.RootDevice++ }},
		{"root inode", func(b *Binding) { b.Identity.RootInode++ }},
		{"ledger dev", func(b *Binding) { b.Identity.LedgerDevice++ }},
		{"ledger inode", func(b *Binding) { b.Identity.LedgerInode++ }},
		{"root path", func(b *Binding) { b.RootPath = privateRoot(t).Path }},
		{"ledger id", func(b *Binding) { b.Header.LedgerID = newName() }},
		{"wrong witness", func(b *Binding) { b.WitnessPin = pub(key(t)) }},
		{"wrong epoch", func(b *Binding) { b.Epoch++ }},
		{"wrong receipt", func(b *Binding) { b.Receipt.Signature = []byte("bad") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := f.b
			tc.change(&b)
			path := filepath.Join(t.TempDir(), "binding")
			if err := WriteBinding(path, b); err == nil {
				t.Fatal("published invalid binding")
			}
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("refusal left a file", err)
			}
			bad, err := json.Marshal(bindingFile{1, b})
			check(t, err)
			put(t, path, bad)
			if _, err = ReadBinding(path); err == nil {
				t.Fatal("read invalid binding")
			}
		})
	}
}

func TestBindingFileProtection(t *testing.T) {
	f := enrolled(t)
	parent := t.TempDir()
	path := filepath.Join(parent, "binding")
	check(t, WriteBinding(path, f.b))
	inside := filepath.Join(f.root.Path, "binding")
	want(t, WriteBinding(inside, f.b), Insecure)
	put(t, inside, contents(t, path))
	_, err := ReadBinding(inside)
	want(t, err, Insecure)
	for _, name := range []string{"symlink", "ancestor", "hard link", "mode", "fifo", "unsafe parent"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "copy")
			switch name {
			case "symlink":
				check(t, os.Symlink(path, p))
			case "ancestor":
				check(t, os.Symlink(parent, p))
				p = filepath.Join(p, "binding")
			case "hard link":
				check(t, os.Link(path, p))
				defer os.Remove(p)
			case "mode":
				put(t, p, contents(t, path))
				check(t, os.Chmod(p, 0644))
			case "fifo":
				check(t, syscall.Mkfifo(p, 0600))
			case "unsafe parent":
				put(t, p, contents(t, path))
				check(t, os.Chmod(dir, 0777))
			}
			if _, err := ReadBinding(p); err == nil {
				t.Fatal("read unsafe file")
			}
			if err := WriteBinding(p, f.b); err == nil {
				t.Fatal("wrote unsafe destination")
			}
		})
	}
	// Exercise the owner check with the actual binding's fstat metadata.
	// Changing ownership on disk requires privilege on typical test hosts.
	var st syscall.Stat_t
	check(t, syscall.Stat(path, &st))
	check(t, checkOwnerMode(st, false, path))
	st.Uid++
	want(t, checkOwnerMode(st, false, path), Insecure)

	header := contents(t, f.ledgerPath())
	put(t, f.ledgerPath(), bytes.Replace(header, []byte(f.b.Header.LedgerID), []byte(newName()), 1))
	_, err = ReadBinding(path)
	want(t, err, IdentityMismatch)
	check(t, os.Rename(f.ledgerPath(), f.ledgerPath()+".old"))
	put(t, f.ledgerPath(), header)
	_, err = ReadBinding(path)
	want(t, err, IdentityMismatch)
}
