package sandboxnet

// PastaProgram is the name of the network wrapper whose arguments [PastaArgs]
// returns.
const PastaProgram = "pasta"

// PastaArgs returns the arguments that follow the pasta executable, ending in
// the "--" that separates them from the command pasta wraps:
//
//	pasta -f -q --config-net -t none -u none -T none -U none --no-map-gw --
//
// The caller puts the executable first and the wrapped command after. Every
// call returns a fresh slice, so a caller may append to it.
//
// What this gives, as the lead-hosting design (section 3.2) proposes it: a
// private network namespace with outbound traffic through NAT, no port
// forwarding in either direction, and the host's loopback not mapped, so a
// host loopback service is not reachable.
//
// Stated limits of the first profile, which this function does not change:
//
//   - Outbound traffic is not narrowed to the providers' hosts. Nothing here
//     restricts which destinations the sandbox can reach.
//   - NAT also reaches the host's own non-loopback addresses, so a host
//     service bound to all interfaces is reachable, and so is the LAN
//     (design cases S2 and S14 record what is reachable; the policy is an
//     open owner decision).
//
// This list is a proposal and is unqualified: it says what is passed to pasta,
// not what the resulting network allows.
func PastaArgs() []string {
	return []string{
		// -f: stay in the foreground, so the holder supervises the process
		// instead of a daemon that forks away.
		"-f",
		// -q: quiet; no informational output on the holder's terminal.
		"-q",
		// --config-net: configure the interface, address and routes inside
		// the new namespace, so the wrapped command finds a working network
		// and bubblewrap can share it (--share-net).
		"--config-net",
		// -t none: no TCP port forwarding from the host into the namespace;
		// nothing on the host side can connect to a service in the sandbox.
		"-t", "none",
		// -u none: no UDP port forwarding from the host into the namespace.
		"-u", "none",
		// -T none: no TCP port forwarding from the namespace to the host's
		// loopback; the sandbox cannot reach a host loopback service this way.
		"-T", "none",
		// -U none: the same for UDP.
		"-U", "none",
		// --no-map-gw: do not map the gateway address to the host, so the
		// gateway is not a route to the host's own services. Outbound NAT
		// through the gateway to other addresses stays.
		"--no-map-gw",
		// --: end of pasta's options; the wrapped command follows.
		"--",
	}
}
