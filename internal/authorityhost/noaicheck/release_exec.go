package noaicheck

import (
	"path"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// UnitSyntax is the finding for a unit or drop-in outside the supported text format.
const UnitSyntax = "unit-syntax"

// UnitTextSafe accepts BOM-free UTF-8 physical lines with LF or CRLF endings.
// Check comments too, before any parser can discard or join a directive.
func UnitTextSafe(text string) bool {
	if strings.HasPrefix(text, "\ufeff") {
		return false
	}
	for raw := range strings.Lines(text) {
		line, ended := strings.CutSuffix(raw, "\n")
		if ended {
			line = strings.TrimSuffix(line, "\r")
		}
		if strings.HasSuffix(line, "\\") || !utf8.ValidString(line) {
			return false
		}
		for _, r := range line {
			if r != '\t' && unicode.IsControl(r) {
				return false
			}
		}
	}
	return true
}

// ReleaseProgram maps a literal systemd executable word to its file in bin/.
// The shipped units use unprefixed executable paths.
func ReleaseProgram(value, releaseDir string) string {
	words, ok := splitSystemd(value)
	if !ok || len(words) == 0 || strings.ContainsRune(value, '\\') || slices.Contains(words, ";") {
		return ""
	}
	program := words[0]
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
	for line := range strings.SplitSeq(text, "\n") {
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
