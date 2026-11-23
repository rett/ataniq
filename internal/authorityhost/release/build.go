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
	"strings"
	"syscall"

	"ataniq/internal/authorityhost/switcher"
)

// The binaries every release carries: the no-AI check the switch runs on the
// candidate, and the switch itself.
var requiredBins = []string{"ah-noaicheck", "ah-switch"}

// PackageStepName is the file name of a release's package step, and
// StaticPackageStep is the one the builder writes itself into a static
// release, which is refused a step of its own (static means this no-op, so a
// release that must move the package snapshot is not static): it checks its
// arguments as the switch gives them and changes nothing. It exits 0 only on a first switch (an empty
// --from) or an unchanged snapshot (--from equal to --to), so it is
// idempotent; any other --from is a snapshot the host would be recorded at
// without being there, and it exits 1 with a line on stderr. The switch
// refuses a static release of another snapshot than the host's known,
// non-empty one in its step 3, before the transaction begins and before this
// step runs (package-step-failed; current, the services, the inhibit and the
// host's snapshot are untouched), so the step is not reached by that typo.
// It still refuses when it runs with another --from, which happens only
// while the host's package state is unknown (the switch then runs the
// unfinished step again from its own --from); that refused run leaves the
// package state unknown, as any failed package step does, and the owner
// settles it with ah-switch recover --package-state. It is POSIX sh and holds
// no time, user or host name.
const PackageStepName = "ah-pkgstep"

const StaticPackageStep = `#!/bin/sh
# The package step of a static release, written by ah-release build: a static
# release installs and removes no package, so it has nothing to do on a first
# switch or an unchanged snapshot, and cannot move the host to another one.
if [ "$#" -ne 6 ] || [ "$1" != "--release" ] || [ "$3" != "--from" ] || [ "$5" != "--to" ]; then
	echo "usage: ah-pkgstep --release DIR --from OLD --to NEW" >&2
	exit 2
fi
if [ -n "$4" ] && [ "$4" != "$6" ]; then
	echo "ah-pkgstep: refused: a static release changes no package, and its package snapshot is not the host's (build it with the host's --package-snapshot)" >&2
	exit 1
fi
echo "ah-pkgstep: static release, no package change"
exit 0
`

// ReleasePlaceholder, in a file of the source's etc/systemd, is replaced by
// Build with the release's own folder on the host,
// /opt/authority-host/releases/<id>, as {release} in the command lists is by
// the switch. A unit can so run the binary of its own release
// (ExecStart={release}/bin/NAME): the switch verifies a unit with
// systemd-analyze before current is moved, and that refuses an ExecStart
// whose program is not there, which /opt/authority-host/current/bin/NAME is
// not while current is the release before the one that first ships NAME.
const ReleasePlaceholder = "{release}"

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
	// ah-switch are required. A static release gets the no-op
	// StaticPackageStep and is refused an ah-pkgstep of its own (a release
	// that must move the package snapshot is not static).
	Bins []string
	// Src is the deployment source folder (deploy/authority-host): its etc/
	// and share/ are copied, with ReleasePlaceholder replaced in the files of
	// its etc/systemd.
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
	if o.Components == switcher.ComponentsStatic && seen[PackageStepName] {
		// A static release is the generated no-op: it changes no package, and
		// the switch refuses a static release that would move the snapshot.
		return refuse(true, CodeStaticStep, "bin/"+PackageStepName)
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
	data []byte // content made by the builder itself, when root is nil
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
// input makes the same bytes. In the files of etc/systemd every
// ReleasePlaceholder becomes /opt/authority-host/releases/<id>; such a file
// is read whole, and one over 4 MiB is refused (too-large).
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

// sourceItems lists the folders and files of the deployment source folder
// src that a release or a baseline copies, its etc/ and share/, with their
// total size and the number of entries; srcRoot is open on src (it is nil
// when src could not be opened), and the caller closes it.
func sourceItems(src string) (items []item, srcRoot *os.Root, total int64, n int, err error) {
	items = []item{{dest: "bin", dir: true}}
	srcRoot, err = os.OpenRoot(src)
	if err != nil {
		return nil, nil, 0, 0, refuse(false, CodeSrc, "")
	}
	for _, top := range []string{"etc", "share"} {
		info, err := srcRoot.Lstat(top)
		if err != nil || !info.IsDir() {
			return nil, srcRoot, 0, 0, refuse(false, CodeSrc, "")
		}
		items = append(items, item{dest: top, dir: true})
		ents, err := walk(srcRoot, top, &n)
		if err != nil {
			return nil, srcRoot, 0, 0, ioRefusal(err)
		}
		for _, e := range ents {
			if !validPath(e.path) {
				return nil, srcRoot, 0, 0, refuse(false, CodeBadName, "")
			}
			switch {
			case e.dir():
				items = append(items, item{dest: e.path, dir: true})
			case e.regular():
				if e.size > MaxFileBytes {
					return nil, srcRoot, 0, 0, refuse(false, CodeTooLarge, e.path)
				}
				total += e.size
				items = append(items, item{dest: e.path, root: srcRoot, src: e.path})
			default:
				return nil, srcRoot, 0, 0, refuse(false, CodeNotRegular, e.path)
			}
		}
	}
	return items, srcRoot, total, n, nil
}

// plan lists what the release holds, sorted by destination, and opens the
// source folders; the caller closes them.
func plan(o Options) ([]item, []io.Closer, error) {
	var closers []io.Closer
	items, srcRoot, total, n, err := sourceItems(o.Src)
	if srcRoot != nil {
		closers = append(closers, srcRoot)
	}
	if err != nil {
		return nil, closers, err
	}
	for i := range items {
		extra, err := expandUnit(&items[i], hostRelDir+o.ID())
		if err != nil {
			return nil, closers, err
		}
		total += extra
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
	if o.Components == switcher.ComponentsStatic && !hasBin(o.Bins, PackageStepName) {
		if n++; n > MaxEntries {
			return nil, closers, refuse(false, CodeTooLarge, "")
		}
		total += int64(len(StaticPackageStep))
		items = append(items, item{dest: "bin/" + PackageStepName, exec: true, data: []byte(StaticPackageStep)})
	}
	if total > MaxTotalBytes {
		return nil, closers, refuse(false, CodeTooLarge, "")
	}
	sort.Slice(items, func(i, j int) bool { return items[i].dest < items[j].dest })
	return items, closers, nil
}

// expandUnit reads a file of etc/systemd whole and replaces every
// ReleasePlaceholder in it with relDir, the release's folder on the host;
// the item then writes those bytes. It returns how many bytes the file
// grew by. Any other item is left as it is.
func expandUnit(it *item, relDir string) (int64, error) {
	if it.dir || it.root == nil || !strings.HasPrefix(it.dest, systemdDir) {
		return 0, nil
	}
	data, err := readSmall(it.root, it.src)
	switch {
	case errors.Is(err, errSmallTooLarge):
		return 0, refuse(false, CodeTooLarge, it.dest)
	case err != nil:
		return 0, ioRefusal(err)
	}
	expanded := bytes.ReplaceAll(data, []byte(ReleasePlaceholder), []byte(relDir))
	it.root, it.src, it.data = nil, "", expanded
	return int64(len(expanded) - len(data)), nil
}

// hasBin reports whether one of the binaries is named name.
func hasBin(bins []string, name string) bool {
	for _, b := range bins {
		if filepath.Base(b) == name {
			return true
		}
	}
	return false
}

// writeItems makes the folders and files of items in the new folder stage
// and returns the digest of each file by its path in the release.
func writeItems(stage string, items []item) (map[string]string, error) {
	sums := map[string]string{}
	var written int64
	for _, it := range items {
		if !it.dir {
			continue
		}
		p := filepath.Join(stage, filepath.FromSlash(it.dest))
		if err := os.Mkdir(p, 0o755); err != nil {
			return nil, ioRefusal(err)
		}
		if err := os.Chmod(p, 0o755); err != nil {
			return nil, ioRefusal(err)
		}
	}
	for _, it := range items {
		if it.dir {
			continue
		}
		dest := filepath.Join(stage, filepath.FromSlash(it.dest))
		if it.root == nil {
			sum, err := copyFile(dest, bytes.NewReader(it.data), int64(len(it.data)), it.exec)
			if err != nil {
				return nil, ioRefusal(err)
			}
			sums[it.dest] = sum
			continue
		}
		in, info, err := openRegular(it.root, it.src)
		if err != nil {
			return nil, ioRefusal(err)
		}
		sum, err := copyFile(dest, in, info.Size(), it.exec)
		in.Close()
		if err != nil {
			return nil, ioRefusal(err)
		}
		if written += info.Size(); written > MaxTotalBytes {
			return nil, refuse(false, CodeTooLarge, "")
		}
		sums[it.dest] = sum
	}
	return sums, nil
}

// write makes the release's content in the new folder stage and returns the
// number of files, MANIFEST and SHA256SUMS included.
func write(stage string, o Options, items []item) (int, error) {
	sums, err := writeItems(stage, items)
	if err != nil {
		return 0, err
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
