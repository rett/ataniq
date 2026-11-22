package noaicheck

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The data files of a release, in its share/ folder.
const (
	FileAINames           = "ai-names.list"
	FileForbiddenPackages = "forbidden-packages.list"
	FilePackagesAllow     = "packages.allow"
	FileUnitsAllow        = "units.allow"
	FilePortsAllow        = "ports.allow"
	FileNFTDigest         = "nft.digest"
)

// maxListSize bounds a data file; the lists are a few kilobytes.
const maxListSize = 1 << 20

// Lists holds a release's data files, parsed.
type Lists struct {
	// AINames are the executable names of known agent tools: a file, a
	// process or an install entry with one of these names is a hit.
	AINames []string
	// AIHomePaths are the credential and config paths of those tools,
	// relative to a home folder (".claude", ".config/opencode").
	AIHomePaths []string
	// ForbiddenPackages are packages the host never needs; one installed is
	// a hit even while the package allowlist is empty.
	ForbiddenPackages []string
	// PackagesAllow is the package allowlist; empty means not enforced yet.
	PackagesAllow []string
	// UnitsAllow is the allowlist of enabled units; empty allows none.
	UnitsAllow []string
	// PortsAllow is the allowlist of listening TCP ports.
	PortsAllow []int
	// NFTDigest is the pinned SHA-256 of the normalised firewall listing,
	// lower-case hex, or "" when the release pins none.
	NFTDigest string
}

// ErrList is wrapped by every refusal of a data file's content.
var ErrList = errors.New("invalid list")

// ParseList reads a data file of one name per line. Blank lines and lines
// whose first non-space byte is '#' are ignored; surrounding spaces are
// trimmed. A line is refused when it holds a path separator, a control byte
// (a tab or a carriage return included), a space inside the name, invalid
// UTF-8, or is "." or "..". An error names the line number, never its text.
func ParseList(r io.Reader) ([]string, error) {
	return parseLines(r, func(n int, name string) error {
		if strings.Contains(name, "/") {
			return lineErr(n, "a path separator")
		}
		return checkComponent(n, name)
	})
}

// ParseAINames reads ai-names.list. An entry starting with '.' is a path
// relative to a home folder and may have more components separated by '/'
// (".config/opencode"), each a valid name; any other entry is an executable
// name and is refused when it holds a path separator.
func ParseAINames(r io.Reader) (names, homePaths []string, err error) {
	entries, err := parseLines(r, func(n int, entry string) error {
		if !strings.HasPrefix(entry, ".") {
			if strings.Contains(entry, "/") {
				return lineErr(n, "a path separator in an executable name")
			}
			return checkComponent(n, entry)
		}
		for _, part := range strings.Split(entry, "/") {
			if err := checkComponent(n, part); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	for _, e := range entries {
		if strings.HasPrefix(e, ".") {
			homePaths = append(homePaths, e)
		} else {
			names = append(names, e)
		}
	}
	return names, homePaths, nil
}

// ParsePorts reads ports.allow: one decimal TCP port (1 to 65535) per line.
func ParsePorts(r io.Reader) ([]int, error) {
	entries, err := ParseList(r)
	if err != nil {
		return nil, err
	}
	ports := make([]int, 0, len(entries))
	for i, e := range entries {
		p, err := strconv.Atoi(e)
		if err != nil || p < 1 || p > 65535 || e != strconv.Itoa(p) {
			return nil, fmt.Errorf("%w: entry %d: not a port number", ErrList, i+1)
		}
		ports = append(ports, p)
	}
	return ports, nil
}

// ParseDigest reads nft.digest: one line holding a SHA-256 in hex as its
// first field (the output of sha256sum is accepted). Comments and blank lines
// are ignored like in every list.
func ParseDigest(r io.Reader) (string, error) {
	var digest string
	_, err := parseLines(r, func(n int, line string) error {
		if digest != "" {
			return lineErr(n, "a second digest")
		}
		field, _, _ := strings.Cut(line, " ")
		field = strings.ToLower(field)
		if b, err := hex.DecodeString(field); err != nil || len(b) != sha256.Size {
			return lineErr(n, "not a SHA-256 in hex")
		}
		digest = field
		return nil
	})
	if err != nil {
		return "", err
	}
	if digest == "" {
		return "", fmt.Errorf("%w: no digest", ErrList)
	}
	return digest, nil
}

func parseLines(r io.Reader, check func(n int, line string) error) ([]string, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxListSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxListSize {
		return nil, fmt.Errorf("%w: larger than %d bytes", ErrList, maxListSize)
	}
	var out []string
	seen := map[string]bool{}
	// Split on '\n' only, so that a carriage return stays and is refused.
	for i, raw := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		n := i + 1
		if !utf8.ValidString(raw) {
			return nil, lineErr(n, "invalid UTF-8")
		}
		for i := 0; i < len(raw); i++ {
			if raw[i] < 0x20 || raw[i] == 0x7f {
				return nil, lineErr(n, "a control byte")
			}
		}
		line := strings.Trim(raw, " ")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if err := check(n, line); err != nil {
			return nil, err
		}
		if !seen[line] {
			seen[line] = true
			out = append(out, line)
		}
	}
	return out, nil
}

func checkComponent(n int, name string) error {
	switch {
	case name == "":
		return lineErr(n, "an empty path component")
	case name == "." || name == "..":
		return lineErr(n, "a dot component")
	case strings.ContainsAny(name, " #\\"):
		return lineErr(n, "a space, '#' or backslash inside the name")
	}
	return nil
}

func lineErr(n int, what string) error {
	return fmt.Errorf("%w: line %d has %s", ErrList, n, what)
}

// LoadLists reads the data files of the release folder dir (which holds
// share/). Every list except nft.digest is required.
func LoadLists(dir string) (*Lists, error) {
	share := filepath.Join(dir, "share")
	l := &Lists{}
	var err error
	open := func(name string) (*os.File, error) {
		return os.Open(filepath.Join(share, name))
	}
	read := func(name string, parse func(io.Reader) error) error {
		f, err := open(name)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := parse(f); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		return nil
	}
	list := func(dst *[]string) func(io.Reader) error {
		return func(r io.Reader) error {
			*dst, err = ParseList(r)
			return err
		}
	}
	steps := []struct {
		name  string
		parse func(io.Reader) error
	}{
		{FileAINames, func(r io.Reader) error {
			l.AINames, l.AIHomePaths, err = ParseAINames(r)
			return err
		}},
		{FileForbiddenPackages, list(&l.ForbiddenPackages)},
		{FilePackagesAllow, list(&l.PackagesAllow)},
		{FileUnitsAllow, list(&l.UnitsAllow)},
		{FilePortsAllow, func(r io.Reader) error {
			l.PortsAllow, err = ParsePorts(r)
			return err
		}},
	}
	for _, s := range steps {
		if err := read(s.name, s.parse); err != nil {
			return nil, err
		}
	}
	if len(l.AINames) == 0 || len(l.AIHomePaths) == 0 {
		return nil, fmt.Errorf("%s: %w: no executable name or no home path", FileAINames, ErrList)
	}
	err = read(FileNFTDigest, func(r io.Reader) error {
		l.NFTDigest, err = ParseDigest(r)
		return err
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return l, nil
}
