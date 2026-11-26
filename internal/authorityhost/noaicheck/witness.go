package noaicheck

import (
	"maps"
	"slices"
	"strings"

	authorityhost "ataniq/deploy/authority-host"
)

const WitnessUnit = "etc/systemd/system/ataniq-witness.service"
const WitnessConfig = "/etc/authority-host/witness/config.json"

// witnessSettings accepts the literal syntax of the shipped unit. Keep repeated
// values in order because systemd can accumulate or reset a setting.
func witnessSettings(text string) (map[string][]string, bool) {
	settings := make(map[string][]string)
	section := ""
	for raw := range strings.Lines(text) {
		line := strings.Trim(strings.TrimSuffix(raw, "\n"), " \t\r")
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if strings.ContainsAny(line, "\\\x00\r") {
			return nil, false
		}
		switch line {
		case "[Unit]", "[Service]":
			section = line
		default:
			key, value, ok := strings.Cut(line, "=")
			if !ok || section == "" || strings.HasPrefix(line, "[") {
				return nil, false
			}
			key = section + strings.Trim(key, " \t")
			settings[key] = append(settings[key], strings.Trim(value, " \t"))
		}
	}
	return settings, true
}

// WitnessUnitSafe checks the candidate release's account, executable and
// outbound-only contract. Only the owner supplies an address grant on the host.
func WitnessUnitSafe(rel, text, releaseDir string) bool {
	_, name, drop := releaseUnitName(rel)
	if rel == WitnessUnit {
		want, valid := witnessSettings(strings.ReplaceAll(authorityhost.WitnessUnit(), "{release}", releaseDir))
		got, ok := witnessSettings(text)
		return valid && ok && maps.EqualFunc(got, want, slices.Equal[[]string])
	}
	if name == "ataniq-witness.socket" || name == "ataniq-witness.service" && !drop {
		return false
	}
	if drop && (name == "ataniq-witness.service" || name == "service" || name == "ataniq-.service") {
		// The shipped overlay needs no release-local overrides.
		settings, ok := witnessSettings(text)
		return ok && len(settings) == 0
	}
	section := ""
	for _, line := range logicalLines(text) {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			section = line
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if section == "[Socket]" && k == "Service" && strings.Trim(v, "\"'") == "ataniq-witness.service" {
			return false
		}
		if section == "[Service]" && strings.HasPrefix(k, "Exec") {
			// Inspect arguments too: a shell can receive the invocation in one
			// quoted word. Refuse escapes we cannot resolve as literal text.
			words, ok := splitSystemd(v)
			if !ok || strings.ContainsRune(v, '\\') || slices.ContainsFunc(words, func(word string) bool {
				return strings.Contains(word, "ataniq-witness")
			}) {
				return false
			}
		}
	}
	return true
}
