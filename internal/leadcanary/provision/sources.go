//go:build linux && ataniq_canary

package provision

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"ataniq/internal/leadcanary/standin"
	"ataniq/internal/leadsandbox/safefile"
)

// Bounds of what provisioning copies, in bytes.
const (
	// MaxManagedSettingsBytes is the largest managed-settings file. It is the
	// bound the start sequence hashes it under.
	MaxManagedSettingsBytes = 1 << 20
	// MaxGuardHookBytes is the largest guard hook executable.
	MaxGuardHookBytes = 64 << 20
	// MaxReplayFileBytes is the largest replay screen, text or report. It is
	// the bound the stand-in reads them under.
	MaxReplayFileBytes = 1 << 20
)

// readTrusted reads the file at abs, for a field of the config. The folder
// that holds it is opened without following a symbolic link in any component,
// and the file is judged on the handle it was read from: a regular file that
// the caller owns, that group and others cannot write (L7), with no setuid,
// setgid or sticky bit and, for an executable, the owner's execute bit. The
// errors name the field and never hold the file's content.
func readTrusted(field, abs string, max int64, executable bool) ([]byte, error) {
	dir, name := filepath.Split(abs)
	root, err := safefile.OpenRootNoLinks(filepath.Clean(dir))
	if err != nil {
		return nil, fmt.Errorf("%s: the folder of the file cannot be opened: %w", field, err)
	}
	defer root.Close()
	res, err := root.ReadFileInfo(name, max)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", field, err)
	}
	info := res.Info
	switch {
	case !owned(info):
		return nil, fmt.Errorf("%s: the file is not owned by the caller", field)
	case info.Mode&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky) != 0:
		return nil, fmt.Errorf("%s: the file has a setuid, setgid or sticky bit", field)
	case info.Mode&0o022 != 0:
		return nil, fmt.Errorf("%s: the file is writable by its group or by others (mode %04o)", field, info.Mode&fs.ModePerm)
	case executable && info.Mode&0o100 == 0:
		return nil, fmt.Errorf("%s: the file is not executable by its owner", field)
	}
	return res.Data, nil
}

// replayScreens are the files read from provision.replay_screens: the two
// screens with their texts, which the stand-in needs, and the optional report
// payload.
type replayScreens struct {
	names []string
	data  [][]byte
}

// replayNames returns the required names, then the optional one.
func replayNames() (required []string, optional string) {
	text := func(json string) string { return strings.TrimSuffix(json, ".json") + ".txt" }
	return []string{
		standin.FirstScreen, text(standin.FirstScreen),
		standin.SecondScreen, text(standin.SecondScreen),
	}, standin.ReportFile
}

// readReplay reads the replay files from the folder the config names. Each is
// a regular file of at most MaxReplayFileBytes, found without following a
// link; the four required ones must be there and the report may be. Nothing
// else in the folder is looked at or copied, the switch file included.
func readReplay(dir string) (replayScreens, error) {
	root, err := safefile.OpenRootNoLinks(dir)
	if err != nil {
		return replayScreens{}, fmt.Errorf("provision.replay_screens: %w", err)
	}
	defer root.Close()
	required, optional := replayNames()
	var out replayScreens
	for _, name := range append(required, optional) {
		data, err := root.ReadFile(name, MaxReplayFileBytes)
		if err != nil {
			if name == optional && errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return replayScreens{}, fmt.Errorf("provision.replay_screens: %s: %w", name, err)
		}
		out.names = append(out.names, name)
		out.data = append(out.data, data)
	}
	return out, nil
}
