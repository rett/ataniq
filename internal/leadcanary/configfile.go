//go:build linux && ataniq_canary

package leadcanary

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadrole"
)

// requiredKeys are the keys that must be present, as paths of object keys. The
// config has no default for any of them. Only agent.env may be left out.
var requiredKeys = [][]string{
	{"version"}, {"role"}, {"state_root"}, {"runtime_root"}, {"inject"},
	{"holder", "executable"}, {"holder", "cols"}, {"holder", "rows"},
	{"holder", "sandbox_inputs"}, {"holder", "profile_digest"},
	{"agent", "kind"}, {"agent", "runtime"}, {"agent", "runtime_version"},
	{"agent", "footer_mode"}, {"agent", "argv"},
	{"limits", "memory_max_bytes"}, {"limits", "swap_max_bytes"}, {"limits", "tasks_max"},
	{"limits", "memory_floor_bytes"}, {"limits", "memory_share_percent"},
	{"provision", "mirror"}, {"provision", "upstream_branch"}, {"provision", "git_name"},
	{"provision", "git_email"}, {"provision", "managed_settings"}, {"provision", "guard_hook"},
	{"provision", "replay_screens"},
}

// ReadConfig reads the canary config at path and checks it.
//
// The file must be one regular file that the caller owns, with mode 0600
// exactly and one link, of at most [MaxConfigBytes]. It is opened once, by its
// handle, without following a symbolic link at its last element, and every
// check is of the handle: the owner, the mode, the link count, the size and the
// resolved path. That path must lie outside the state root and the runtime root
// the file names, by resolved path (symbolic links resolved as far as the
// folders exist), so the lead's sandbox and the holder's state can never hold
// the file that governs them.
//
// The content is one JSON object: valid UTF-8, no unknown field, no repeated
// key, no null, nothing after the object, version 1, and every key of the
// schema present (agent.env alone may be absent) with the values that
// [Config.Validate] accepts. The five limits have no default: a missing limit is
// refused, and so is one outside its bound. Every refusal is a *ConfigError
// that wraps [ErrConfig] and never repeats a value, except for a failure to open
// or read the file, which wraps the system error.
//
// ReadConfig does not look at the default roots or at the marker; the daemon
// calls [Config.CheckRoots] for that before it listens.
func ReadConfig(path string) (Config, error) {
	data, real, err := readConfigFile(path)
	if err != nil {
		return Config{}, err
	}
	cfg, err := parseConfig(data)
	if err != nil {
		return Config{}, err
	}
	if err := checkOutsideRoots(real, cfg.StateRoot, cfg.RuntimeRoot); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// checkOutsideRoots refuses a config file whose resolved path lies inside the
// state root or the runtime root, by resolved path of the roots (symbolic links
// resolved as far as they exist).
func checkOutsideRoots(real, stateRoot, runtimeRoot string) error {
	for _, r := range []struct{ field, root string }{{"state_root", stateRoot}, {"runtime_root", runtimeRoot}} {
		root, err := resolveLoose(r.root)
		if err != nil {
			return bad(r.field, "cannot be resolved")
		}
		if real == root || strings.HasPrefix(real, root+"/") {
			return bad("file", "the config lies inside the %s", r.field)
		}
	}
	return nil
}

// readConfigFile opens the file by handle and returns its content and its
// resolved path.
func readConfigFile(path string) (data []byte, real string, err error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, "", bad("file", "cannot make the path absolute")
	}
	// O_NONBLOCK so that a named pipe cannot hold the open. The mode and the
	// type are judged on the handle.
	fd, err := unix.Open(abs, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, "", fmt.Errorf("%w: file: cannot open: %w", ErrConfig, err)
	}
	f := os.NewFile(uintptr(fd), "config")
	defer f.Close()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return nil, "", fmt.Errorf("%w: file: cannot examine: %w", ErrConfig, err)
	}
	switch {
	case st.Mode&unix.S_IFMT != unix.S_IFREG:
		return nil, "", bad("file", "is not a regular file")
	case int(st.Uid) != unix.Geteuid():
		return nil, "", bad("file", "is not owned by the caller")
	case st.Mode&07777 != 0o600:
		return nil, "", bad("file", "must have mode 0600")
	case st.Nlink != 1:
		// The path the handle names is judged against the roots below; a second
		// link to the inode could sit inside one of them unseen.
		return nil, "", bad("file", "must have exactly one link")
	case st.Size > MaxConfigBytes:
		return nil, "", bad("file", "is longer than %d bytes", MaxConfigBytes)
	}
	// The path the handle names, not the one that was asked for: a folder on
	// the way may have been swapped for a link since.
	real, err = os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
	if err != nil || !filepath.IsAbs(real) || strings.HasSuffix(real, " (deleted)") {
		return nil, "", bad("file", "its resolved path cannot be found")
	}
	data, err = io.ReadAll(io.LimitReader(f, MaxConfigBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("%w: file: cannot read: %w", ErrConfig, err)
	}
	if len(data) > MaxConfigBytes {
		return nil, "", bad("file", "is longer than %d bytes", MaxConfigBytes)
	}
	return data, real, nil
}

// resolveLoose resolves the symbolic links of the nearest existing part of a
// clean absolute path and appends the rest, so a root that is not made yet can
// be compared.
func resolveLoose(p string) (string, error) {
	cur := p
	var rest []string
	for {
		_, err := os.Lstat(cur)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, unix.ENOTDIR) {
			return "", err
		}
		if cur == "/" {
			return "", err
		}
		rest = append([]string{filepath.Base(cur)}, rest...)
		cur = filepath.Dir(cur)
	}
	real, err := filepath.EvalSymlinks(cur)
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{real}, rest...)...), nil
}

// parseConfig decodes and validates the bytes of a config.
func parseConfig(data []byte) (Config, error) {
	var cfg Config
	if err := decodeStrict(data, &cfg, requiredKeys, nil); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// decodeStrict decodes the bytes of a config file into v with the rules both
// config files share: valid UTF-8, no key that an object repeats, no unknown
// field, one object and nothing after it, no null anywhere, and every key of
// required present. An element of an array of objects is checked by perElement:
// it is called with the decoded tree and returns the first missing key of the
// elements, as a field name, or "". The caller validates the values.
func decodeStrict(data []byte, v any, required [][]string, perElement func(tree map[string]any) string) error {
	if !utf8.Valid(data) {
		return bad("file", "is not valid UTF-8")
	}
	if err := scanKeys(data); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, leadrole.ErrInvalid) {
			return bad("role", "is not a role string")
		}
		return bad("file", "is not a config object: %s", decodeReason(err))
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		return bad("file", "holds data after the object")
	}
	var tree map[string]any
	if err := json.Unmarshal(data, &tree); err != nil {
		return bad("file", "is not a JSON object")
	}
	if field := findNull(tree, ""); field != "" {
		return bad(field, "is null")
	}
	for _, key := range required {
		if !present(tree, key) {
			return bad(strings.Join(key, "."), "is missing")
		}
	}
	if perElement != nil {
		if field := perElement(tree); field != "" {
			return bad(field, "is missing")
		}
	}
	return nil
}

// decodeReason gives the part of a decoding error that holds no value: the
// standard library's type and unknown-field messages name a field and a type,
// and a syntax error an offset.
func decodeReason(err error) string {
	var ute *json.UnmarshalTypeError
	var se *json.SyntaxError
	switch {
	case errors.As(err, &ute):
		return "a field has the wrong type: " + ute.Field
	case errors.As(err, &se):
		return "syntax error at offset " + strconv.FormatInt(se.Offset, 10)
	case strings.HasPrefix(err.Error(), "json: unknown field"):
		return "unknown field"
	}
	return "unreadable"
}

func present(tree map[string]any, key []string) bool {
	var cur any = tree
	for _, k := range key {
		m, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		if cur, ok = m[k]; !ok {
			return false
		}
	}
	return true
}

// findNull returns the path of the first null in a decoded tree, or "".
func findNull(node any, path string) string {
	switch v := node.(type) {
	case nil:
		if path == "" {
			return "file"
		}
		return path
	case map[string]any:
		for k, c := range v {
			p := k
			if path != "" {
				p = path + "." + k
			}
			if f := findNull(c, p); f != "" {
				return f
			}
		}
	case []any:
		for i, c := range v {
			if f := findNull(c, path+"["+strconv.Itoa(i)+"]"); f != "" {
				return f
			}
		}
	}
	return ""
}

// scanKeys refuses a key that an object repeats, which encoding/json would
// resolve silently to the last one, at any depth.
func scanKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	type level struct {
		obj     bool
		keyNext bool
		seen    map[string]struct{}
	}
	var stack []level
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return bad("file", "is not valid JSON")
		}
		done := false
		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{', '[':
				l := level{obj: t == '{', keyNext: t == '{'}
				if l.obj {
					l.seen = map[string]struct{}{}
				}
				stack = append(stack, l)
			default:
				stack = stack[:len(stack)-1]
				done = true
			}
		case string:
			if top := len(stack) - 1; top >= 0 && stack[top].obj && stack[top].keyNext {
				if _, dup := stack[top].seen[t]; dup {
					return bad("file", "repeats a key")
				}
				stack[top].seen[t] = struct{}{}
				stack[top].keyNext = false
			} else {
				done = true
			}
		default:
			done = true
		}
		if done && len(stack) > 0 {
			if top := len(stack) - 1; stack[top].obj {
				stack[top].keyNext = true
			}
		}
	}
}
