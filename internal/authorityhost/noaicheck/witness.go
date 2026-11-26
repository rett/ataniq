package noaicheck

import (
	"maps"
	"slices"
	"strings"
)

const WitnessUnit = "etc/systemd/system/ataniq-witness.service"
const WitnessConfig = "/etc/authority-host/witness/config.json"

// Keep the standalone checker independent of the candidate release and the
// deployment packages. TestWitnessReleaseScan compares this policy with the
// shipped unit, including all repeated values.
const witnessUnitPolicy = `[Unit]
Description=Authority host checkpoint witness
After=network.target
StartLimitIntervalSec=300
StartLimitBurst=5
[Service]
Type=exec
User=ataniq-witness
Group=ataniq-witness
ExecStart={release}/bin/ataniq-witness run --config /etc/authority-host/witness/config.json
Restart=on-failure
RestartPreventExitStatus=1
RestartSec=5s
RuntimeDirectory=ataniq-witness
RuntimeDirectoryMode=0700
StateDirectory=ataniq-witness
StateDirectoryMode=0700
UMask=0077
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
IPAddressDeny=any
SocketBindDeny=any
NoNewPrivileges=yes
CapabilityBoundingSet=
AmbientCapabilities=
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectProc=invisible
ProcSubset=pid
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectControlGroups=yes
ProtectClock=yes
ProtectHostname=yes
RestrictNamespaces=yes
RestrictRealtime=yes
RestrictSUIDSGID=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
RemoveIPC=yes
KeyringMode=private
SystemCallArchitectures=native
SystemCallFilter=@system-service
SystemCallFilter=~@privileged @resources
SystemCallErrorNumber=EPERM
MemoryMax=256M
TasksMax=64
`

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
		want, valid := witnessSettings(strings.ReplaceAll(witnessUnitPolicy, "{release}", releaseDir))
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

func (c *Checker) witnessExpected() bool {
	if c.witnessRelease {
		return true
	}
	_, _, err := c.t.lstat(WitnessUnit)
	if err != nil {
		c.unreadable(WitnessUnit, err)
		return false
	}
	return true
}
