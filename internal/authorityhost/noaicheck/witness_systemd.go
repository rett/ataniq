package noaicheck

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net/netip"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// systemctl reports the manager's in-memory policy, which can differ from
// disk until daemon-reload. Only query the real host's system manager.
func (t *tree) checkEffectiveEgress(address netip.Addr, requireLoaded bool) error {
	_, info, err := t.lstat("run/systemd/system")
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil || !info.IsDir() {
		return errWitnessEgress
	}
	_, err = t.resolve(WitnessUnit)
	allowUnloaded := !requireLoaded && errors.Is(err, fs.ErrNotExist)
	return queryWitnessEgress("/usr/bin/systemctl", address, allowUnloaded)
}

func queryWitnessEgress(systemctl string, address netip.Addr, allowUnloaded bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, systemctl, "--system", "show", "--all", "-p", "IPAddressAllow,IPAddressDeny", "-p", "LoadState", "ataniq-witness.service")
	cmd.Env = []string{"LC_ALL=C"}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return errWitnessEgress
	}
	if err := cmd.Start(); err != nil {
		return errWitnessEgress
	}
	const limit = 64 << 10
	data, readErr := io.ReadAll(io.LimitReader(stdout, limit+1))
	stdout.Close()
	err = cmd.Wait()
	var exit *exec.ExitError
	missing := errors.As(err, &exit) && exit.ExitCode() == 4 && allowUnloaded
	if readErr != nil || len(data) > limit || err != nil && !missing {
		return errWitnessEgress
	}
	if missing && !slices.Contains(strings.Split(string(data), "\n"), "LoadState=not-found") {
		return errWitnessEgress
	}
	if !effectiveWitnessEgress(string(data), address, allowUnloaded) {
		return errWitnessEgress
	}
	return nil
}

func effectiveWitnessEgress(output string, address netip.Addr, allowUnloaded bool) bool {
	props := make(map[string]string)
	for line := range strings.Lines(output) {
		key, value, ok := strings.Cut(strings.TrimSuffix(line, "\n"), "=")
		if _, duplicate := props[key]; !ok || duplicate {
			return false
		}
		props[key] = value
	}
	if props["LoadState"] == "not-found" {
		return allowUnloaded
	}
	allow, ok := props["IPAddressAllow"]
	if props["LoadState"] != "loaded" || !ok {
		return false
	}
	if address.IsValid() {
		got, ok := singleAddress(allow)
		if !ok || got != address {
			return false
		}
	} else if allow != "" {
		return false
	}
	deny := strings.Fields(props["IPAddressDeny"])
	return slices.Equal(deny, []string{"any"}) || len(deny) == 2 && slices.Contains(deny, "0.0.0.0/0") && slices.Contains(deny, "::/0")
}
