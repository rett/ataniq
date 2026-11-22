package release

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"ataniq/internal/authorityhost/noaicheck"
)

// Limits on what Baseline reads.
const (
	// MaxInputBytes bounds each input file.
	MaxInputBytes = 2 << 20
	// maxInputLine bounds a line of an input file.
	maxInputLine = 4 << 10
)

// BaselineOptions is what Baseline fills the allowlists from: files the
// owner collected on the host (see the README, "Filling the allowlists").
type BaselineOptions struct {
	// Src is the deployment source folder; its etc/ and share/ are copied.
	Src string
	// Units is a list of unit names, Ports a list of TCP ports, Packages the
	// output of "pacman -Qq" and NFT the output of "nft list ruleset".
	Units, Ports, Packages, NFT string
	// Out is the folder made; it must not exist. Empty means a new folder
	// in the system's temporary folder.
	Out string
	// Stamp is a text (a date) the headers of the filled files carry.
	Stamp string
}

// stampPattern is what a stamp may hold: it is written into a comment.
var stampPattern = regexp.MustCompile(`^[A-Za-z0-9._:+ -]{1,128}$`)

// baselineUnit is a systemd unit name as noaicheck's units.allow holds one: the
// characters of a unit name (a backslash, which systemd also allows, is not
// readable in a list) and one of the unit types as its suffix; a template is
// "name@.service".
var baselineUnit = regexp.MustCompile(`^[A-Za-z0-9:_.@-]+\.(service|socket|target|device|mount|automount|swap|path|timer|slice|scope)$`)

// packageName is a pacman package name: letters, digits and @._+-, not
// starting with a hyphen or a dot.
var packageName = regexp.MustCompile(`^[A-Za-z0-9@_+][A-Za-z0-9@._+-]*$`)

// The files of share/ that Baseline writes.
const (
	shareUnits    = "share/" + noaicheck.FileUnitsAllow
	sharePorts    = "share/" + noaicheck.FilePortsAllow
	sharePackages = "share/" + noaicheck.FilePackagesAllow
	shareNFT      = "share/" + noaicheck.FileNFTDigest
)

// Baseline makes a scratch copy of the source folder's etc/ and share/ whose
// allowlists are filled: units.allow from the unit names of o.Units,
// ports.allow from the ports of o.Ports, packages.allow from the package
// names of o.Packages and nft.digest from the listing o.NFT, by the digest
// function of "ah-noaicheck nft-digest". The folder can be given to build as
// its source. It returns the folder made.
//
// Each input must be a regular file of at most MaxInputBytes; a blank line
// and a line starting with "#" are left out, a line that is not a valid
// entry of its list refuses the input, a duplicate is dropped quietly, and
// an input that leaves no entry is refused. The entries are written sorted,
// under a header that says what they were taken from (and the stamp), never
// a host name. Nothing is written into Src, and an output folder that exists
// already, or lies in Src, is refused; the folder is made beside its place
// and renamed into it, so a failed run leaves nothing.
func Baseline(o BaselineOptions) (string, error) {
	if o.Src == "" || o.Units == "" || o.Ports == "" || o.Packages == "" || o.NFT == "" {
		return "", refuse(true, CodeBaselineArgs, "")
	}
	if o.Stamp != "" && !stampPattern.MatchString(o.Stamp) {
		return "", refuse(true, CodeStamp, "")
	}
	texts, err := baselineTexts(o)
	if err != nil {
		return "", err
	}

	items, srcRoot, _, _, err := sourceItems(o.Src)
	if srcRoot != nil {
		defer srcRoot.Close()
	}
	if err != nil {
		return "", err
	}
	var keep []item
	for _, it := range items {
		if _, replaced := texts[it.dest]; replaced || it.dest == "bin" {
			continue
		}
		keep = append(keep, it)
	}
	for dest, text := range texts {
		keep = append(keep, item{dest: dest, data: []byte(text)})
	}
	sort.Slice(keep, func(i, j int) bool { return keep[i].dest < keep[j].dest })

	stage, final, err := baselineStage(o)
	if err != nil {
		return "", err
	}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(stage)
		}
	}()
	if err := os.Chmod(stage, 0o755); err != nil {
		return "", ioRefusal(err)
	}
	if _, err := writeItems(stage, keep); err != nil {
		return "", err
	}
	if stage != final {
		if err := os.Rename(stage, final); err != nil {
			return "", ioRefusal(err)
		}
	}
	ok = true
	return final, nil
}

// baselineStage makes the new folder the baseline is written into and
// returns it and the folder it becomes. For a given Out that is a folder
// beside it, renamed into place when whole; without one it is a new folder
// of the temporary folder, which is its own place.
func baselineStage(o BaselineOptions) (stage, final string, err error) {
	if o.Out == "" {
		if inside(o.Src, os.TempDir()) {
			return "", "", refuse(false, CodeOut, "")
		}
		dir, err := os.MkdirTemp("", "ah-baseline-")
		if err != nil {
			return "", "", ioRefusal(err)
		}
		return dir, dir, nil
	}
	out, err := filepath.Abs(o.Out)
	if err != nil || inside(o.Src, out) {
		return "", "", refuse(false, CodeOut, "")
	}
	if _, err := os.Lstat(out); err == nil {
		return "", "", refuse(false, CodeOutExists, "")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", "", ioRefusal(err)
	}
	parent, base := filepath.Split(out)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		r := ioRefusal(err)
		r.Finding.Code = CodeOut
		return "", "", r
	}
	stage = filepath.Join(parent, "."+base+".partial")
	if err := os.Mkdir(stage, 0o755); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return "", "", refuse(false, CodeOutExists, "")
		}
		return "", "", ioRefusal(err)
	}
	return stage, out, nil
}

// inside reports whether path (which need not exist) is src or lies beneath
// it, once every link of the part that exists is resolved.
func inside(src, path string) bool {
	a, errA := resolve(src)
	b, errB := resolve(path)
	if errA != nil || errB != nil {
		return true // unknown, so refused
	}
	rel, err := filepath.Rel(a, b)
	return err != nil || rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// resolve is the absolute path p with the links of its existing part
// resolved and the rest, which does not exist, appended as it is.
func resolve(p string) (string, error) {
	p, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	var rest []string
	for {
		real, err := filepath.EvalSymlinks(p)
		if err == nil {
			for i := len(rest) - 1; i >= 0; i-- {
				real = filepath.Join(real, rest[i])
			}
			return real, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", err
		}
		rest = append(rest, filepath.Base(p))
		p = parent
	}
}

// baselineTexts reads the four inputs and returns the text of each file of
// share/ that is made from them, by its path in the release.
func baselineTexts(o BaselineOptions) (map[string]string, error) {
	stamp := ""
	if o.Stamp != "" {
		stamp = " (" + o.Stamp + ")"
	}

	unitData, err := readInput(o.Units, "units")
	if err != nil {
		return nil, err
	}
	units, err := entries("units", unitData, func(line string) (string, bool) {
		return line, baselineUnit.MatchString(line)
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(units)

	portData, err := readInput(o.Ports, "ports")
	if err != nil {
		return nil, err
	}
	portLines, err := entries("ports", portData, func(line string) (string, bool) {
		p, err := strconv.Atoi(line)
		return line, err == nil && p >= 1 && p <= 65535 && line == strconv.Itoa(p)
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(portLines, func(i, j int) bool {
		a, _ := strconv.Atoi(portLines[i])
		b, _ := strconv.Atoi(portLines[j])
		return a < b
	})

	pkgData, err := readInput(o.Packages, "packages")
	if err != nil {
		return nil, err
	}
	packages, err := entries("packages", pkgData, func(line string) (string, bool) {
		return line, packageName.MatchString(line)
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(packages)

	nftData, err := readInput(o.NFT, "nft")
	if err != nil {
		return nil, err
	}
	if len(noaicheck.NormaliseNFT(nftData)) == 0 {
		return nil, refuse(false, CodeInputEmpty, "nft")
	}
	digest := noaicheck.NFTDigest(nftData)

	texts := map[string]string{
		shareUnits: "# The allowlist of enabled systemd units, read by ah-noaicheck (see\n" +
			"# ../README.md). One unit name per line; a template such as \"getty@.service\"\n" +
			"# allows every instance of it. Sorted, no duplicate.\n#\n" +
			"# Filled by \"ah-release baseline\" from the unit names the owner took from the\n" +
			"# HIT unit lines of one run of \"ah-noaicheck --phase pre --require-policy\" on\n" +
			"# the host" + stamp + ".\n" + strings.Join(units, "\n") + "\n",
		sharePorts: "# The allowlist of listening TCP ports, read by \"ah-noaicheck ports\" (see\n" +
			"# ../README.md). One decimal port number per line, allowed on any address.\n" +
			"# Sorted, no duplicate.\n#\n" +
			"# Filled by \"ah-release baseline\" from the ports of \"ss -H -ltn\" on the host" + stamp + ".\n" +
			strings.Join(portLines, "\n") + "\n",
		sharePackages: "# The package allowlist of the authority host, read by ah-noaicheck (see\n" +
			"# ../README.md). One package name per line, as pacman names it. Sorted, no\n" +
			"# duplicate.\n#\n" +
			"# Filled by \"ah-release baseline\" from the output of \"pacman -Qq\" on the host" + stamp + ".\n" +
			strings.Join(packages, "\n") + "\n",
		shareNFT: "# The SHA-256 of the normalised firewall listing, read by ah-noaicheck (see\n" +
			"# ../README.md), the digest \"ah-noaicheck nft-digest FILE\" prints.\n#\n" +
			"# Filled by \"ah-release baseline\" from the output of \"nft list ruleset\" on the\n" +
			"# host" + stamp + ".\n" + digest + "\n",
	}

	// What is written is what the no-AI check reads back.
	for dest, parse := range map[string]func(string) error{
		shareUnits:    func(s string) error { _, err := noaicheck.ParseList(strings.NewReader(s)); return err },
		sharePackages: func(s string) error { _, err := noaicheck.ParseList(strings.NewReader(s)); return err },
		sharePorts:    func(s string) error { _, err := noaicheck.ParsePorts(strings.NewReader(s)); return err },
		shareNFT: func(s string) error {
			d, err := noaicheck.ParseDigest(strings.NewReader(s))
			if err == nil && d != digest {
				err = errors.New("digest")
			}
			return err
		},
	} {
		if err := parse(texts[dest]); err != nil {
			return nil, ioRefusal(fmt.Errorf("baseline %s: %w", dest, err))
		}
	}
	return texts, nil
}

// readInput reads the input file path, which must be a regular file of at
// most MaxInputBytes. name is the input's name in a refusal ("units").
func readInput(path, name string) ([]byte, error) {
	dir, base := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, refuse(false, CodeInputNotFile, name)
	}
	defer root.Close()
	f, info, err := openRegular(root, base)
	if err != nil {
		return nil, refuse(false, CodeInputNotFile, name)
	}
	defer f.Close()
	if info.Size() > MaxInputBytes {
		return nil, refuse(false, CodeInputTooLarge, name)
	}
	data, err := io.ReadAll(io.LimitReader(f, info.Size()+1))
	if err != nil || int64(len(data)) != info.Size() {
		return nil, refuse(false, CodeInputNotFile, name)
	}
	return data, nil
}

// entries reads the lines of an input: blank lines and lines that start
// with "#" (after spaces) are left out, every other line, with its spaces
// trimmed, must pass valid, and a duplicate is dropped quietly. A line over
// maxInputLine bytes, one with a control byte (a tab or a carriage return
// included) or a non-ASCII byte, or one that fails valid, refuses, naming
// the line number. No entry at all is refused.
func entries(name string, data []byte, valid func(line string) (string, bool)) ([]string, error) {
	text := strings.TrimSuffix(string(data), "\n")
	seen := map[string]bool{}
	var out []string
	if len(data) > 0 {
		for i, raw := range strings.Split(text, "\n") {
			bad := func() error {
				r := refuse(false, CodeInputInvalid, name)
				r.Detail = "line " + strconv.Itoa(i+1)
				return r
			}
			if len(raw) > maxInputLine {
				return nil, bad()
			}
			for j := 0; j < len(raw); j++ {
				if c := raw[j]; c < 0x20 || c >= 0x7f {
					return nil, bad()
				}
			}
			line := strings.Trim(raw, " ")
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			entry, ok := valid(line)
			if !ok {
				return nil, bad()
			}
			if !seen[entry] {
				seen[entry] = true
				out = append(out, entry)
			}
		}
	}
	if len(out) == 0 {
		return nil, refuse(false, CodeInputEmpty, name)
	}
	return out, nil
}
