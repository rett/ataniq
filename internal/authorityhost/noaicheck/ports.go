package noaicheck

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// tcpListen is the state of a listening socket in proc/net/tcp.
const tcpListen = "0A"

// errProcNet is returned for a line of proc/net/tcp that cannot be read.
var errProcNet = errors.New("malformed line")

// errNoHeader is returned for a socket table that is empty or does not
// start with the kernel's header line ("sl local_address rem_address st
// ..."): it is not a table the check can trust to list every socket.
var errNoHeader = errors.New("no header")

// DetailHeader is the detail of "HIT incomplete /proc/net/tcp header", a
// socket table that is empty or has no valid header line. A table with a
// header and no listening socket is fine.
const DetailHeader = "header"

// CheckPorts, run by RunAll and alone by the ports subcommand: a listening
// TCP socket (state LISTEN in proc/net/tcp or proc/net/tcp6) on a port not
// in ports.allow is a hit of kind "port" named "tcp:ADDRESS:PORT" or
// "tcp6:[ADDRESS]:PORT". The files show the sockets of the network
// namespace of the process that reads them; UDP is not checked. A file that
// cannot be read is a hit of kind "unreadable", and so is a missing
// proc/net/tcp; proc/net/tcp6 is missing when IPv6 is off. A table that is
// there but empty or without the kernel's header line is
// "incomplete <file> header". An empty
// ports.allow allows none; with RequirePolicy it is also the hit
// "policy ports.allow empty".
func (c *Checker) CheckPorts() {
	if len(c.lists.PortsAllow) == 0 && c.RequirePolicy {
		c.notEnforced(FilePortsAllow, DetailEmpty, "")
	}
	for _, f := range []struct {
		rel, proto string
		required   bool
	}{{"proc/net/tcp", "tcp", true}, {"proc/net/tcp6", "tcp6", false}} {
		_, data, err := c.t.readFile(f.rel, 64<<20)
		if errors.Is(err, errBudget) {
			return
		}
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				if !f.required {
					continue
				}
				err = errors.New("missing")
			}
			c.hit(KindUnreadable, show(f.rel), describe(err))
			continue
		}
		socks, err := listening(string(data))
		switch {
		case errors.Is(err, errNoHeader):
			c.hit(KindIncomplete, show(f.rel), DetailHeader)
			continue
		case err != nil:
			c.hit(KindUnreadable, show(f.rel), describe(err))
		}
		for _, s := range socks {
			if !slices.Contains(c.lists.PortsAllow, int(s.Port())) {
				c.hit(KindPort, f.proto+":"+s.String(), "")
			}
		}
	}
}

// listening returns the local address of every listening socket in the
// content of proc/net/tcp or tcp6. Content that does not start with the
// header line is errNoHeader. The lines it can read are returned with an
// error naming the first one it cannot.
func listening(data string) ([]netip.AddrPort, error) {
	lines := strings.Split(data, "\n")
	if h := strings.Fields(lines[0]); len(h) < 4 || h[0] != "sl" || h[1] != "local_address" || h[2] != "rem_address" || h[3] != "st" {
		return nil, errNoHeader
	}
	var out []netip.AddrPort
	var bad error
	for i, line := range lines {
		f := strings.Fields(line)
		if i == 0 || len(f) == 0 {
			continue // the header
		}
		if len(f) < 4 {
			bad = firstBad(bad, i)
			continue
		}
		if f[3] != tcpListen {
			continue
		}
		ap, err := parseProcAddr(f[1])
		if err != nil {
			bad = firstBad(bad, i)
			continue
		}
		out = append(out, ap)
	}
	return out, bad
}

func firstBad(err error, line int) error {
	if err != nil {
		return err
	}
	return fmt.Errorf("%w %d", errProcNet, line+1)
}

// parseProcAddr reads "0100007F:0016": the address as 32-bit words in the
// host's byte order (little-endian on the hosts this runs on) and the port
// in hex.
func parseProcAddr(s string) (netip.AddrPort, error) {
	a, p, ok := strings.Cut(s, ":")
	if !ok {
		return netip.AddrPort{}, errProcNet
	}
	port, err := strconv.ParseUint(p, 16, 16)
	if err != nil {
		return netip.AddrPort{}, err
	}
	raw, err := hex.DecodeString(a)
	if err != nil || (len(raw) != 4 && len(raw) != 16) {
		return netip.AddrPort{}, errProcNet
	}
	for w := 0; w < len(raw); w += 4 {
		raw[w], raw[w+1], raw[w+2], raw[w+3] = raw[w+3], raw[w+2], raw[w+1], raw[w]
	}
	addr, _ := netip.AddrFromSlice(raw)
	return netip.AddrPortFrom(addr, uint16(port)), nil
}
