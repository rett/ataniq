package noaicheck

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/netip"
	"path"
	"path/filepath"
	"strings"
)

const witnessDropIns = WitnessUnit + ".d"

var errUnitSyntax = errors.New(UnitSyntax)
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

// WitnessEgressAddress checks the drop-ins. Owner preflight can also query
// the running manager, including before the first installation of the unit.
func WitnessEgressAddress(root string, queryManager bool) (netip.Addr, error) {
	t, err := openTree(root)
	if err != nil {
		return netip.Addr{}, err
	}
	defer t.close()
	return t.witnessEgressAddress(queryManager && filepath.Clean(root) == "/", true, false)
}

// singleAddress accepts only a single host address, optionally with its full
// prefix length. A zero address is reserved for an absent grant.
func singleAddress(value string) (netip.Addr, bool) {
	if a, err := netip.ParseAddr(value); err == nil {
		return a, true
	}
	p, err := netip.ParsePrefix(value)
	return p.Addr(), err == nil && p.Bits() == p.Addr().BitLen()
}

// scanWitnessDropIns adds the grant in this directory to the host policy.
// Settings in separate files can accumulate or reset one another.
func (t *tree) scanWitnessDropIns(dir string, address *netip.Addr) (bool, error) {
	var problem error
	found := false
	_, _, err := t.walk(dir, 1, func(e *wentry) bool {
		if problem != nil || !strings.HasSuffix(e.name, ".conf") {
			return false
		}
		found = true
		p, data, err := t.readFile(e.rel, 64<<10)
		if err != nil {
			problem = err
			return false
		}
		text := string(data)
		if !UnitTextSafe(text) {
			problem = &fs.PathError{Op: "check", Path: show(p), Err: errUnitSyntax}
			return false
		}
		settings, ok := witnessSettings(text)
		if ok && len(settings) == 0 {
			return false
		}
		values := settings["[Service]IPAddressAllow"]
		if !ok || len(settings) != 1 || len(values) != 1 || address.IsValid() {
			problem = errWitnessEgress
			return false
		}
		if *address, ok = singleAddress(values[0]); !ok {
			problem = errWitnessEgress
		}
		return false
	}, func(_ string, err error) { problem = err })
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return found, err
	}
	return found, problem
}

func (t *tree) witnessEgressAddress(liveRoot, expected, requireLoaded bool) (netip.Addr, error) {
	var address netip.Addr
	found := false
	for _, scope := range []string{"ataniq-witness.service.d", "ataniq-.service.d", "service.d"} {
		if scope != "ataniq-witness.service.d" && !expected && !found {
			break
		}
		for _, d := range systemDirs {
			hasConfig, err := t.scanWitnessDropIns(path.Join(d.rel, scope), &address)
			if err != nil {
				return netip.Addr{}, err
			}
			found = found || hasConfig
		}
	}
	if found && !address.IsValid() {
		return netip.Addr{}, errWitnessEgress
	}
	if liveRoot && (expected || address.IsValid()) {
		if err := t.checkEffectiveEgress(address, requireLoaded); err != nil {
			return netip.Addr{}, err
		}
	}
	return address, nil
}

func (c *Checker) checkWitnessEgress(requireLoaded bool) {
	expected := c.witnessExpected()
	address, err := c.t.witnessEgressAddress(c.rootAbs == "/", expected, requireLoaded)
	var pe *fs.PathError
	if errors.As(err, &pe) && errors.Is(err, errUnitSyntax) {
		c.hit(KindUnit, pe.Path, UnitSyntax)
		return
	}
	if err != nil {
		c.hit(KindUnit, show(witnessDropIns), "witness-egress")
		return
	}
	if !address.IsValid() {
		if expected {
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
