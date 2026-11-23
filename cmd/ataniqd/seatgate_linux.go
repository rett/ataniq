//go:build linux && ataniq_ownerchannel

package main

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"path/filepath"

	"ataniq/internal/approval/hostadapter"
	"ataniq/internal/leaddaemon"
	"ataniq/internal/ownerlocal"
	"ataniq/internal/seatgate"
)

// This file is the seat gate of the command, in a build with the
// ataniq_ownerchannel tag: the flag --seat-gate of run (registered with
// --owner-channel in owner_linux.go), the seat origin the gate reads once at
// start, and the command "ataniqd seat-gate", which makes the gate's key and
// lists the pinned seat keys. seatgate_off_linux.go has the same names for
// every other build.

// seatGateListen opens the gate's listener. A test replaces it to see whether
// the daemon listens at all.
var seatGateListen = net.Listen

// seatGateDir is the gate's folder in a state folder: owner/gate.
func seatGateDir(stateDir string) string {
	return filepath.Join(stateDir, hostadapter.OwnerDirName, leaddaemon.SeatGateDirName)
}

// genesisDir is the folder of the guided interview's thread store in a state
// folder: owner/genesis. The gate opens the store there (package
// genesis/thread) and makes the folder, mode 0700, when it is not there; a
// folder the store refuses is logged, and the gate serves without genesis.
func genesisDir(stateDir string) string {
	return filepath.Join(stateDir, hostadapter.OwnerDirName, leaddaemon.GenesisDirName)
}

// seatGatePrepare reads what the gate needs before the daemon takes a lock:
// the address of --seat-gate, the gate's key in the state folder and the pin
// file beside it. It returns the gate, or a code to end with: a configuration
// the daemon refuses. Nothing is made and nothing listens yet.
func (a *app) seatGatePrepare(addr, stateDir string) (*leaddaemon.SeatGate, int) {
	refuse := func(format string, args ...any) (*leaddaemon.SeatGate, int) {
		fmt.Fprintf(a.stderr, "ataniqd run: --seat-gate: "+format+"\n", args...)
		return nil, leaddaemon.ExitConfig
	}
	ap, err := leaddaemon.ParseSeatGateAddr(addr)
	if err != nil {
		return refuse("%s", err)
	}
	dir := seatGateDir(stateDir)
	id, err := seatgate.LoadIdentity(dir)
	if err != nil {
		return refuse("the gate's key in %s cannot be used (%s); make one with ataniqd seat-gate init",
			leaddaemon.SafeText(dir), leaddaemon.SafeText(err.Error()))
	}
	pinFile := filepath.Join(dir, leaddaemon.SeatPinsFileName)
	pins, err := leaddaemon.ReadSeatPins(pinFile)
	if err != nil {
		return refuse("%s: %s", leaddaemon.SafeText(pinFile), leaddaemon.SafeText(err.Error()))
	}
	if len(pins) == 0 {
		return refuse("%s pins no seat key, so no seat could connect", leaddaemon.SafeText(pinFile))
	}
	origin, why := seatGateOrigin(a.getenv)
	return &leaddaemon.SeatGate{
		Addr: ap, Identity: id, Pins: pins, Origin: origin, NoOrigin: why, GenesisDir: genesisDir(stateDir),
		Log:    func(format string, args ...any) { fmt.Fprintf(a.stderr, "ataniqd: "+format+"\n", args...) },
		Listen: seatGateListen,
	}, -1
}

// seatGateOrigin reads the seat origin, once, as the daemon starts: the
// owner-local file seat-origin (package ownerlocal), made canonical by
// leaddaemon.CanonicalSeatOrigin. Without one (no configuration folder, no
// file or an empty one, a file ownerlocal refuses, or one that holds no
// canonical origin) it returns "" and why, for the gate's start line: the gate
// then serves pair and read only, and the daemon starts as before. why holds
// nothing of the file's content.
func seatGateOrigin(getenv func(string) string) (origin, why string) {
	if ownerlocal.SeatOriginPath(getenv) == "" {
		return "", "no configuration folder (XDG_CONFIG_HOME or HOME) to read the seat origin from"
	}
	v, err := ownerlocal.SeatOrigin(getenv)
	switch {
	case err != nil:
		return "", "the owner-local seat-origin file is refused (" + err.Error() + ")"
	case v == "":
		return "", "no seat origin is configured (the owner-local seat-origin file is missing or empty)"
	}
	o, _, err := leaddaemon.CanonicalSeatOrigin(v)
	if err != nil {
		return "", "the owner-local seat-origin file holds no canonical origin (https:// and a lower-case domain name)"
	}
	return o, ""
}

// withSeatGate wraps the builder of the owner channel's service so that the
// gate starts on the service once it is built, and stops before the service
// is closed, when the daemon's server has ended. A gate that cannot start
// closes the service and stops the daemon at its serve step. A nil gate
// leaves the builder as it is.
func withSeatGate(gate *leaddaemon.SeatGate, build func() (leaddaemon.OwnerService, func() error, error)) func() (leaddaemon.OwnerService, func() error, error) {
	if gate == nil {
		return build
	}
	return func() (leaddaemon.OwnerService, func() error, error) {
		svc, closeSvc, err := build()
		if err != nil || svc == nil {
			return svc, closeSvc, err
		}
		stop, err := gate.Start(svc)
		if err != nil {
			if closeSvc != nil {
				_ = closeSvc()
			}
			return nil, nil, err
		}
		return svc, func() error {
			stop()
			if closeSvc != nil {
				return closeSvc()
			}
			return nil
		}, nil
	}
}

// seatGateCommand handles "ataniqd seat-gate init|pins [--state-dir DIR]".
func (a *app) seatGateCommand(args []string) (code int, ok bool) {
	if len(args) == 0 || args[0] != "seat-gate" {
		return 0, false
	}
	if len(args) < 2 || (args[1] != "init" && args[1] != "pins") {
		fmt.Fprint(a.stderr, usage)
		return leaddaemon.ExitUsage, true
	}
	word := args[1]
	var stateDir string
	if code := a.flags("seat-gate "+word, args[2:], func(fs *flag.FlagSet) {
		fs.StringVar(&stateDir, "state-dir", "", "state folder")
	}); code >= 0 {
		return code, true
	}
	if stateDir == "" {
		var err error
		if stateDir, err = leaddaemon.DefaultStateDir(a.getenv); err != nil {
			fmt.Fprintf(a.stderr, "ataniqd: %s\n", leaddaemon.SafeText(err.Error()))
			return leaddaemon.ExitConfig, true
		}
	} else if !filepath.IsAbs(stateDir) || filepath.Clean(stateDir) != stateDir {
		fmt.Fprintf(a.stderr, "ataniqd seat-gate %s: the state folder %q is not a clean absolute path\n", word, leaddaemon.SafeText(stateDir))
		return leaddaemon.ExitConfig, true
	}
	dir := seatGateDir(stateDir)
	if word == "init" {
		return a.seatGateInit(dir), true
	}
	return a.seatGatePins(dir), true
}

// seatGateInit makes the gate's key in dir and prints its fingerprint, the
// pin the seat is given. It never overwrites a key.
func (a *app) seatGateInit(dir string) int {
	id, err := seatgate.WriteIdentityFiles(dir, leaddaemon.SeatGateIdentityName)
	if errors.Is(err, seatgate.ErrExists) {
		fmt.Fprintf(a.stderr, "ataniqd seat-gate init: %s already holds a key, which is never overwritten; move it away by hand to make a new one\n", leaddaemon.SafeText(dir))
		return leaddaemon.ExitFailure
	}
	if err != nil {
		fmt.Fprintf(a.stderr, "ataniqd seat-gate init: %s: %s\n", leaddaemon.SafeText(dir), leaddaemon.SafeText(err.Error()))
		return leaddaemon.ExitFailure
	}
	fmt.Fprintln(a.stdout, id.Fingerprint())
	return leaddaemon.ExitOK
}

// seatGatePins prints the pins of the pin file in dir, one per line, as the
// daemon would read them at its next start.
func (a *app) seatGatePins(dir string) int {
	path := filepath.Join(dir, leaddaemon.SeatPinsFileName)
	pins, err := leaddaemon.ReadSeatPins(path)
	if err != nil {
		fmt.Fprintf(a.stderr, "ataniqd seat-gate pins: %s: %s\n", leaddaemon.SafeText(path), leaddaemon.SafeText(err.Error()))
		return leaddaemon.ExitFailure
	}
	for _, p := range pins {
		fmt.Fprintln(a.stdout, p)
	}
	return leaddaemon.ExitOK
}
