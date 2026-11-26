package noaicheck

import (
	"path"
	"slices"
	"strings"
)

// ReleaseProgram maps a literal systemd executable word to its file in bin/.
// The current link names this release when the unit is active. Strip only
// execution prefixes which leave the invoked executable unchanged.
func ReleaseProgram(value, releaseDir string) string {
	words, ok := splitSystemd(value)
	if !ok || len(words) == 0 || strings.ContainsRune(value, '\\') || slices.Contains(words, ";") {
		return ""
	}
	program := strings.TrimLeft(words[0], "@-:+!")
	if strings.ContainsAny(program, "$%") || !path.IsAbs(program) || path.Clean(program) != program {
		return ""
	}
	name := path.Base(program)
	if program == releaseDir+"/bin/"+name || program == "/opt/authority-host/current/bin/"+name {
		return name
	}
	return ""
}

// ReleaseExecsSafe checks each command independently, including commands in
// drop-ins and those before a reset. A reset itself launches nothing.
func ReleaseExecsSafe(text, releaseDir string, shipped func(string) bool) bool {
	if !strings.Contains(text, "Exec") {
		return true
	}
	section := ""
	for _, line := range logicalLines(text) {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			section = line
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || section != "[Service]" && section != "[Socket]" {
			continue
		}
		switch strings.TrimSpace(key) {
		case "ExecCondition", "ExecStart", "ExecStartPre", "ExecStartPost", "ExecReload", "ExecStop", "ExecStopPost", "ExecStopPre":
			if value = strings.TrimSpace(value); value != "" {
				name := ReleaseProgram(value, releaseDir)
				if name == "" || !shipped(name) {
					return false
				}
			}
		}
	}
	return true
}
