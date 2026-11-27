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

// TrimUnitSpace removes a physical line ending, if present, and trims only
// the ASCII space and tab that systemd ignores around unit fields.
func TrimUnitSpace(s string) string {
	return strings.Trim(unitLineText(s), " \t")
}

func unitLineText(s string) string {
	if line, ended := strings.CutSuffix(s, "\n"); ended {
		return strings.TrimSuffix(line, "\r")
	}
	return s
}

// UnitTextSafe checks release units and witness egress drop-ins. It also
// rejects any physical line ending in a backslash, including comments.
func UnitTextSafe(text string) bool {
	return unitTextSafe(text, false)
}

// HostUnitTextSafe checks the physical lines of host systemd configuration
// before joining continuations or discarding comments.
func HostUnitTextSafe(text string) bool {
	return unitTextSafe(text, true)
}

func unitTextSafe(text string, allowContinuations bool) bool {
	for raw := range strings.Lines(text) {
		line := unitLineText(raw)
		if !allowContinuations && strings.HasSuffix(line, "\\") || !utf8.ValidString(line) {
			return false
		}
		for _, r := range line {
			switch {
			case r != '\t' && unicode.IsControl(r):
				return false
			case r > unicode.MaxASCII && unicode.IsSpace(r):
				return false
			case r >= '\u200b' && r <= '\u200f', r == '\u2060', r == '\ufeff':
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
	for line := range strings.Lines(text) {
		line = TrimUnitSpace(line)
		if strings.HasPrefix(line, "[") {
			section = line
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || section != "[Service]" && section != "[Socket]" {
			continue
		}
		switch TrimUnitSpace(key) {
		case "ExecCondition", "ExecStart", "ExecStartPre", "ExecStartPost", "ExecReload", "ExecStop", "ExecStopPost", "ExecStopPre":
			if value = TrimUnitSpace(value); value != "" {
				name := ReleaseProgram(value, releaseDir)
				if name == "" || !shipped(name) {
					return false
				}
			}
		}
	}
	return true
}
