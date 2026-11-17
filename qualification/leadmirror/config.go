package leadmirror

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"ataniq/internal/exchange/safefs"
	"ataniq/internal/exchange/sandboxplan"
)

// Errors of the config and of the refusals before any case runs. Test them
// with errors.Is.
var (
	// ErrConfig means that the config file is missing, unreadable or wrong.
	ErrConfig = errors.New("leadmirror: bad config")
	// ErrNotAcknowledged means that the config does not say
	// "i_run_this_on_purpose": true.
	ErrNotAcknowledged = errors.New("leadmirror: the config does not acknowledge the run (i_run_this_on_purpose must be true)")
	// ErrNoTemplate means that the config names no sandbox template, or the
	// template cannot be read or parsed.
	ErrNoTemplate = errors.New("leadmirror: no usable sandbox template")
	// ErrStateRoot means that the scratch state root is refused: it is not a
	// plain absolute path, it is a symlink or not a folder, or it holds
	// anything.
	ErrStateRoot = errors.New("leadmirror: the scratch state root is refused")
)

// maxConfigBytes is the largest config file that [ReadConfig] accepts.
const maxConfigBytes = 64 << 10

// Config is the config file. The person who runs the harness writes it. Every
// field is required.
type Config struct {
	// StateRoot is the scratch state root: an absolute, clean path of plain
	// characters (letters, digits, dot, dash, underscore and slash) that does
	// not exist yet or is an empty folder. The harness refuses a path that
	// holds anything.
	StateRoot string `json:"state_root"`
	// Template is the absolute path of the sandbox template file, in the
	// sandboxplan format.
	Template string `json:"template"`
	// Role and Project name the scratch lead and project. Both pass
	// sandboxplan.ValidName.
	Role    string `json:"role"`
	Project string `json:"project"`
	// Acknowledged must be true. It says that the person who runs the
	// harness means to run it on this host with this template.
	Acknowledged bool `json:"i_run_this_on_purpose"`
}

// ReadConfig reads and checks the config file at path. It refuses a missing
// file, a file that is not a regular file (no symlink is followed in the last
// name), a file over 64 KiB, an unknown field, data after the config, a
// missing field and a config without the acknowledgement. The error matches
// [ErrConfig] or, for the acknowledgement, [ErrNotAcknowledged].
func ReadConfig(path string) (Config, error) {
	data, err := readSmallFile(path, maxConfigBytes)
	if err != nil {
		return Config{}, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var c Config
	if err := dec.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return Config{}, fmt.Errorf("%w: data after the config", ErrConfig)
	}
	return c, c.Validate()
}

// Validate checks the fields without reading any file. The acknowledgement is
// checked first.
func (c Config) Validate() error {
	if !c.Acknowledged {
		return ErrNotAcknowledged
	}
	if err := checkStateRootText(c.StateRoot); err != nil {
		return err
	}
	if c.Template == "" {
		return fmt.Errorf("%w: the config names no template", ErrNoTemplate)
	}
	if !filepath.IsAbs(c.Template) || filepath.Clean(c.Template) != c.Template {
		return fmt.Errorf("%w: the template path is not absolute and clean", ErrNoTemplate)
	}
	if !sandboxplan.ValidName(c.Role) {
		return fmt.Errorf("%w: role is not a valid name", ErrConfig)
	}
	if !sandboxplan.ValidName(c.Project) {
		return fmt.Errorf("%w: project is not a valid name", ErrConfig)
	}
	return nil
}

// LoadTemplate reads the sandbox template file at path and returns the
// planner built from it and the hex SHA-256 of the file's bytes, which the
// report carries as the template's digest. The error matches [ErrNoTemplate].
func LoadTemplate(path string) (*sandboxplan.Template, string, error) {
	data, err := readSmallFile(path, sandboxplan.MaxTemplateBytes)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %w", ErrNoTemplate, err)
	}
	t, err := sandboxplan.ParseTemplate(data)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %w", ErrNoTemplate, err)
	}
	sum := sha256.Sum256(data)
	return t, hex.EncodeToString(sum[:]), nil
}

// readSmallFile reads a regular file through safefs: the last name is not
// followed if it is a symlink. The folders on the way belong to the person
// who runs the harness.
func readSmallFile(path string, max int64) ([]byte, error) {
	if path == "" {
		return nil, errors.New("no path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	root, err := safefs.OpenRoot(filepath.Dir(abs))
	if err != nil {
		return nil, fmt.Errorf("the folder of the file: %w", err)
	}
	defer root.Close()
	return root.ReadFile(filepath.Base(abs), max)
}

// plainPath reports whether s holds only letters, digits, dot, dash,
// underscore and slash. A scratch path made of these needs no quoting when it
// is written into a shell command or a git config value.
func plainPath(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '-' || c == '_' || c == '/':
		default:
			return false
		}
	}
	return true
}

// checkStateRootText checks the text of the state root: absolute, clean,
// plain characters, and not a folder at the top of the file system.
func checkStateRootText(p string) error {
	switch {
	case p == "":
		return fmt.Errorf("%w: no path", ErrStateRoot)
	case !filepath.IsAbs(p) || filepath.Clean(p) != p:
		return fmt.Errorf("%w: the path is not absolute and clean", ErrStateRoot)
	case p == "/" || filepath.Dir(p) == "/":
		return fmt.Errorf("%w: the path is too close to the root of the file system", ErrStateRoot)
	case !plainPath(p):
		return fmt.Errorf("%w: the path may hold only letters, digits, dot, dash, underscore and slash", ErrStateRoot)
	}
	return nil
}
