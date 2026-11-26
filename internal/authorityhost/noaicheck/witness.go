package noaicheck

import "strings"

const WitnessUnit = "etc/systemd/system/ataniq-witness.service"
const WitnessConfig = "/etc/authority-host/witness/config.json"

// WitnessUnitSafe checks the candidate release's account, executable and
// outbound-only contract. The address grant belongs in the owner's drop-in.
func WitnessUnitSafe(rel, text, releaseDir string) bool {
	_, name, drop := releaseUnitName(rel)
	main := rel == WitnessUnit
	applies := name == "ataniq-witness.service" || drop && (name == "service" || name == "ataniq-.service")
	if name == "ataniq-witness.socket" || (name == "ataniq-witness.service" && !main && !drop) {
		return false
	}
	bin := releaseDir + "/bin/ataniq-witness"
	required := map[string]string{
		"User": "ataniq-witness", "Group": "ataniq-witness",
		"ExecStart":               bin + " run --config " + WitnessConfig,
		"ExecStartPre":            bin + " check --config " + WitnessConfig,
		"RestrictAddressFamilies": "AF_UNIX AF_INET AF_INET6", "SocketBindDeny": "any", "IPAddressDeny": "any",
		"StateDirectory": "ataniq-witness", "StateDirectoryMode": "0700", "RuntimeDirectory": "ataniq-witness", "RuntimeDirectoryMode": "0700",
		"NoNewPrivileges": "yes", "PrivateTmp": "yes", "ProtectSystem": "strict", "UMask": "0077",
		"CapabilityBoundingSet": "", "AmbientCapabilities": "",
	}
	seen := map[string]bool{}
	section := ""
	for _, line := range logicalLines(text) {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if strings.HasPrefix(line, "[") {
			section = line
			if applies && (section == "[Socket]" || section == "[Install]") {
				return false
			}
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if section == "[Socket]" && k == "Service" && strings.Trim(v, "\"'") == "ataniq-witness.service" {
			return false
		}
		if section != "[Service]" {
			continue
		}
		if !applies {
			if strings.HasPrefix(k, "Exec") && strings.Contains(v, "/bin/ataniq-witness ") {
				return false
			}
			continue
		}
		if want, ok := required[k]; ok {
			if drop || seen[k] || v != want {
				return false
			}
			seen[k] = true
			continue
		}
		// Reject exceptions to the bind denial, privilege switches and extra commands.
		switch k {
		case "SocketBindAllow", "IPAddressAllow", "Sockets", "SupplementaryGroups", "DynamicUser", "EnvironmentFile", "Environment", "PassEnvironment":
			return false
		}
		if strings.HasPrefix(k, "Exec") || strings.HasPrefix(k, "Listen") {
			return false
		}
	}
	return !main || len(seen) == len(required)
}
