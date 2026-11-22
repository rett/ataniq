package release

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"syscall"

	"ataniq/internal/authorityhost/switcher"
)

// The binaries every release carries: the no-AI check the switch runs on the
// candidate, and the switch itself.
var requiredBins = []string{"ah-noaicheck", "ah-switch"}

// Options is what Build makes a release from.
type Options struct {
	// Out is the folder the release folder <id> is made in.
	Out string
	// Counter is the release counter, 1 or more; Commit the full commit
	// (lowercase hex, 7 to 64 digits), of which the first 7 digits make the id.
	Counter uint64
	Commit  string
	// GoVersion, BuiltOn (the class of the build host, never its name) and
	// PackageSnapshot are the MANIFEST's values.
	GoVersion       string
	BuiltOn         string
	PackageSnapshot string
	// Bins are the files that become bin/<base name>; ah-noaicheck and
	// ah-switch are required.
	Bins []string
	// Src is the deployment source folder (deploy/authority-host): its etc/
	// and share/ are copied.
	Src string
	// Components is "static" for a release with no decision service, or "".
	Components string
}

// ID is the release's name, the counter zero-padded to four digits, a hyphen
// and the first 7 digits of the commit.
func (o Options) ID() string {
	return fmt.Sprintf("%04d-%s", o.Counter, o.Commit[:7])
}

// validate checks the values; a wrong one is a usage refusal.
func (o Options) validate() *Refusal {
	switch {
	case o.Counter < 1:
		return refuse(true, CodeCounter, "")
	case !commitPattern.MatchString(o.Commit):
		return refuse(true, CodeCommit, "")
	case !valuePattern.MatchString(o.GoVersion):
		return refuse(true, CodeGoVersion, "")
	case !valuePattern.MatchString(o.BuiltOn):
		return refuse(true, CodeBuiltOn, "")
	case !switcher.ValidSnapshot(o.PackageSnapshot):
		return refuse(true, CodeSnapshot, "")
	case o.Components != "" && o.Components != switcher.ComponentsStatic:
		return refuse(true, CodeComponents, "")
	case o.Out == "":
		return refuse(true, CodeOut, "")
	case o.Src == "":
		return refuse(true, CodeSrc, "")
	}
	if _, err := switcher.ParseID(o.ID()); err != nil {
		return refuse(true, CodeCounter, "")
	}
	seen := map[string]bool{}
	for _, b := range o.Bins {
		name := filepath.Base(b)
		if !validName(name) {
			return refuse(true, CodeBinName, "")
		}
		if seen[name] {
			return refuse(true, CodeBinTwice, name)
		}
		seen[name] = true
	}
	for _, name := range requiredBins {
		if !seen[name] {
			return refuse(true, CodeBinMissing, "bin/"+name)
		}
	}
	return nil
}

// manifest is the MANIFEST text: one key: value per line, in a fixed order.
func (o Options) manifest() []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "id: %s\ncounter: %d\ncommit: %s\nbuilt_on: %s\ngo_version: %s\npackage_snapshot: %s\n",
		o.ID(), o.Counter, o.Commit, o.BuiltOn, o.GoVersion, o.PackageSnapshot)
	if o.Components != "" {
		fmt.Fprintf(&b, "components: %s\n", o.Components)
	}
	return b.Bytes()
}

// item is one folder or file of the release to make.
type item struct {
	dest string // path in the release
	dir  bool
	exec bool
	root *os.Root // where the source file is, with src below it
	src  string
}

// Build makes the release folder Out/<id> and returns its id and the number
// of files in it. It refuses, before it writes anything, a counter below 1,
// a commit or value with characters the switch's MANIFEST would not read, a
// missing required binary, an output folder that exists already, and any
// name, link, fifo, device or socket in the source's etc/ and share/ it
// cannot copy as a plain folder or file. The folder is made beside its final
// place and renamed into it, so a failed build leaves nothing under the id.
//
// Folders are 0755 and files 0644 (0755 for bin/*); no link, owner or time
// of the source is carried, and the content holds no time, so the same
// input makes the same bytes.
func Build(o Options) (string, int, error) {
	if r := o.validate(); r != nil {
		return "", 0, r
	}
	id := o.ID()
	if err := os.MkdirAll(o.Out, 0o755); err != nil {
		r := ioRefusal(err)
		r.Finding.Code = CodeOut
		return "", 0, r
	}
	final := filepath.Join(o.Out, id)
	if _, err := os.Lstat(final); err == nil {
		return "", 0, refuse(false, CodeExists, id)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", 0, ioRefusal(err)
	}

	items, closers, err := plan(o)
	defer func() {
		for _, c := range closers {
			c.Close()
		}
	}()
	if err != nil {
		return "", 0, err
	}

	stage := filepath.Join(o.Out, "."+id+".partial")
	if err := os.Mkdir(stage, 0o755); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return "", 0, refuse(false, CodeExists, "."+id+".partial")
		}
		return "", 0, ioRefusal(err)
	}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(stage)
		}
	}()
	if err := os.Chmod(stage, 0o755); err != nil {
		return "", 0, ioRefusal(err)
	}
	files, err := write(stage, o, items)
	if err != nil {
		return "", 0, err
	}
	if err := os.Rename(stage, final); err != nil {
		return "", 0, ioRefusal(err)
	}
	ok = true
	return id, files, nil
}

// plan lists what the release holds, sorted by destination, and opens the
// source folders; the caller closes them.
func plan(o Options) ([]item, []io.Closer, error) {
	var closers []io.Closer
	items := []item{{dest: "bin", dir: true}}
	var total int64
	n := 0

	srcRoot, err := os.OpenRoot(o.Src)
	if err != nil {
		return nil, closers, refuse(false, CodeSrc, "")
	}
	closers = append(closers, srcRoot)
	for _, top := range []string{"etc", "share"} {
		info, err := srcRoot.Lstat(top)
		if err != nil || !info.IsDir() {
			return nil, closers, refuse(false, CodeSrc, "")
		}
		items = append(items, item{dest: top, dir: true})
		ents, err := walk(srcRoot, top, &n)
		if err != nil {
			return nil, closers, ioRefusal(err)
		}
		for _, e := range ents {
			if !validPath(e.path) {
				return nil, closers, refuse(false, CodeBadName, "")
			}
			switch {
			case e.dir():
				items = append(items, item{dest: e.path, dir: true})
			case e.regular():
				if e.size > MaxFileBytes {
					return nil, closers, refuse(false, CodeTooLarge, e.path)
				}
				total += e.size
				items = append(items, item{dest: e.path, root: srcRoot, src: e.path})
			default:
				return nil, closers, refuse(false, CodeNotRegular, e.path)
			}
		}
	}

	for _, b := range o.Bins {
		dir, name := filepath.Split(b)
		if dir == "" {
			dir = "."
		}
		root, err := os.OpenRoot(dir)
		if err != nil {
			return nil, closers, refuse(false, CodeNotRegular, "bin/"+name)
		}
		closers = append(closers, root)
		info, err := root.Lstat(name)
		if err != nil || !info.Mode().IsRegular() {
			return nil, closers, refuse(false, CodeNotRegular, "bin/"+name)
		}
		if info.Size() > MaxFileBytes {
			return nil, closers, refuse(false, CodeTooLarge, "bin/"+name)
		}
		total += info.Size()
		if n++; n > MaxEntries {
			return nil, closers, refuse(false, CodeTooLarge, "")
		}
		items = append(items, item{dest: "bin/" + name, exec: true, root: root, src: name})
	}
	if total > MaxTotalBytes {
		return nil, closers, refuse(false, CodeTooLarge, "")
	}
	sort.Slice(items, func(i, j int) bool { return items[i].dest < items[j].dest })
	return items, closers, nil
}

// write makes the release's content in the new folder stage and returns the
// number of files, MANIFEST and SHA256SUMS included.
func write(stage string, o Options, items []item) (int, error) {
	sums := map[string]string{}
	var written int64
	for _, it := range items {
		if !it.dir {
			continue
		}
		p := filepath.Join(stage, filepath.FromSlash(it.dest))
		if err := os.Mkdir(p, 0o755); err != nil {
			return 0, ioRefusal(err)
		}
		if err := os.Chmod(p, 0o755); err != nil {
			return 0, ioRefusal(err)
		}
	}
	for _, it := range items {
		if it.dir {
			continue
		}
		in, info, err := openRegular(it.root, it.src)
		if err != nil {
			return 0, ioRefusal(err)
		}
		sum, err := copyFile(filepath.Join(stage, filepath.FromSlash(it.dest)), in, info.Size(), it.exec)
		in.Close()
		if err != nil {
			return 0, ioRefusal(err)
		}
		if written += info.Size(); written > MaxTotalBytes {
			return 0, refuse(false, CodeTooLarge, "")
		}
		sums[it.dest] = sum
	}
	manifest := o.manifest()
	if err := writeFile(filepath.Join(stage, switcher.ManifestFile), manifest, 0o644); err != nil {
		return 0, ioRefusal(err)
	}
	sums[switcher.ManifestFile] = sumOf(manifest)
	if err := writeFile(filepath.Join(stage, switcher.SumsFile), sumsText(sums), 0o644); err != nil {
		return 0, ioRefusal(err)
	}
	return len(sums) + 1, nil
}

// sumsText is SHA256SUMS: a line "<sha256>  <path>" per file, sorted by path
// as LC_ALL=C sort sorts it (by byte), as sha256sum writes it.
func sumsText(sums map[string]string) []byte {
	paths := make([]string, 0, len(sums))
	for p := range sums {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var b bytes.Buffer
	for _, p := range paths {
		b.WriteString(sums[p])
		b.WriteString("  ")
		b.WriteString(p)
		b.WriteByte('\n')
	}
	return b.Bytes()
}

func sumOf(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func mode(exec bool) fs.FileMode {
	if exec {
		return 0o755
	}
	return 0o644
}

// copyFile writes size bytes of in to the new file p, which must not exist,
// and returns their SHA-256; a source that grows or shrinks while it is read
// is an error.
func copyFile(p string, in io.Reader, size int64, exec bool) (string, error) {
	out, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(in, size+1))
	if err == nil && n != size {
		err = errors.New("file changed while it was read")
	}
	if err == nil {
		err = out.Chmod(mode(exec))
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeFile(p string, data []byte, perm fs.FileMode) error {
	_, err := copyFile(p, bytes.NewReader(data), int64(len(data)), perm&0o111 != 0)
	return err
}
