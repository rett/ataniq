package noaicheck

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/netip"
	"strings"
)

const witnessDropIns = WitnessUnit + ".d"

var errWitnessEgress = errors.New("invalid witness egress drop-in")

// WitnessHomeAddress extracts the address of a canonical unicast TCP endpoint.
func WitnessHomeAddress(endpoint string) (netip.Addr, bool) {
	ap, err := netip.ParseAddrPort(endpoint)
	a := ap.Addr()
	if err != nil || ap.String() != endpoint || ap.Port() == 0 || a.Zone() != "" ||
		a.Is4In6() || !a.IsGlobalUnicast() || a.Is4() && a.As4()[0] == 0 {
		return netip.Addr{}, false
	}
	return a, true
}

// WitnessEgressAddress reads the unit's owner-local .conf files beneath root.
// A zero address with no error means there is no drop-in.
func WitnessEgressAddress(root string) (netip.Addr, error) {
	t, err := openTree(root)
	if err != nil {
		return netip.Addr{}, err
	}
	defer t.close()
	return t.witnessEgressAddress()
}

func (t *tree) witnessEgressAddress() (netip.Addr, error) {
	var address netip.Addr
	var problem error
	found := false
	_, _, err := t.walk(witnessDropIns, 1, func(e *wentry) bool {
		if !strings.HasSuffix(e.name, ".conf") || problem != nil {
			return false
		}
		found = true
		_, data, err := t.readFile(e.rel, 64<<10)
		if err != nil {
			problem = err
			return false
		}
		settings, ok := witnessSettings(string(data))
		for key, values := range settings {
			if key != "[Service]IPAddressAllow" || len(values) != 1 || address.IsValid() {
				ok = false
				break
			}
			value := values[0]
			a, err := netip.ParseAddr(value)
			if err != nil {
				p, err := netip.ParsePrefix(value)
				if err != nil || p.Bits() != p.Addr().BitLen() {
					ok = false
					break
				}
				a = p.Addr()
			}
			address = a
		}
		if !ok {
			problem = errWitnessEgress
		}
		return false
	}, func(_ string, err error) { problem = err })
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return netip.Addr{}, err
	}
	if problem != nil {
		return netip.Addr{}, problem
	}
	if found && !address.IsValid() {
		return netip.Addr{}, errWitnessEgress
	}
	return address, nil
}

func (c *Checker) checkWitnessEgress() {
	address, err := c.t.witnessEgressAddress()
	if err != nil {
		c.hit(KindUnit, show(witnessDropIns), "witness-egress")
		return
	}
	if !address.IsValid() {
		if c.witnessExpected() {
			c.note("witness egress drop-in missing: home unreachable")
		}
		return
	}
	_, data, err := c.t.readFile(WitnessConfig, 64<<10)
	var config struct {
		Home string `json:"home_endpoint"`
	}
	if err != nil || json.Unmarshal(data, &config) != nil {
		c.hit(KindUnit, WitnessConfig, "witness-egress")
		return
	}
	if want, ok := WitnessHomeAddress(config.Home); !ok || want != address {
		c.hit(KindUnit, show(witnessDropIns), "witness-egress")
	}
}
