//go:build linux && ataniq_ownerchannel

package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"ataniq/internal/approval/credkind"
	"ataniq/internal/approval/door"
	"ataniq/internal/approval/door/doortest"
	"ataniq/internal/approval/hostadapter"
	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/exchange/gitenv"
	"ataniq/internal/leaddaemon"
)

// This file is the one place in the command that builds the host's side of a
// card, for the scratch pass: the owner channel's service on the real journal
// in the state folder, the real merge, intake and records of the host adapter,
// the real suite runner, the file-drop collector, and the stand-in for the
// planner's door. It is
// provisional and unqualified. Nothing here runs in a release build.

// ownerSuite replaces the suite runner of the build. It is nil: the builder
// then makes the real one (package verify in merge mode, through the sandbox
// template and the work folder the scratch settings name, with the real
// runner) and refuses settings that name none rather than refuse every
// request. A test sets a fake; nothing else does.
var ownerSuite hostadapter.Suite

// ownerDoorWrap, when set, wraps the stand-in door the builder hands the host
// adapter. It is nil: the adapter gets the stand-in itself. A test sets a
// wrapper that counts the calls at the door; nothing else does.
var ownerDoorWrap func(*doortest.Door) door.Door

// ownerNotice receives the host-written lines of the service and the adapter.
// The default writes them to the standard error, one line each.
var ownerNotice = func(line string) { fmt.Fprintf(os.Stderr, "ataniqd: owner channel: %s\n", line) }

func init() {
	ownerServiceBuilder = buildOwnerService
	ownerBuildFrom = buildOwnerServiceFrom
	ownerGuard = &ownerGuardSet{
		check: func(stateDir string, getenv func(string) string) error {
			return checkOwnerSettings(stateDir, stateScratch(stateDir), getenv)
		},
		checkFrom: checkOwnerSettings,
		banner:    ownerBanner,
	}
}

// checkOwnerSettings is the guard of the builder on the settings load reads:
// the guards of [checkOwnerScratchFrom] and, without a suite of a test, a
// suite the settings name, so that the daemon refuses before it takes a lock.
func checkOwnerSettings(stateDir string, load scratchLoad, getenv func(string) string) error {
	sc, err := checkOwnerScratchFrom(stateDir, load, getenv)
	if err != nil {
		return err
	}
	if ownerSuite == nil {
		_, err = sc.VerifySuiteOf()
	}
	return err
}

// openOwnerKeys opens the list of the owner's deciding keys, the file
// keys.log in the owner folder, and returns it for the service, which closes it.
// A list that cannot be opened (damaged, not private, held by another process)
// is no list: the daemon still serves the approvals, its pairing and key
// operations answer keys-unavailable, and the notice says why once. The file is
// never repaired or started over, so a damaged list is never taken for an empty
// one.
func openOwnerKeys(stateDir string) ownerchannel.KeyStore {
	path := filepath.Join(stateDir, hostadapter.OwnerDirName, ownerjournal.KeysFileName)
	k, err := ownerjournal.OpenKeys(path)
	if err != nil {
		ownerNotice("the list of deciding keys cannot be opened, so pairing a phone and the keys are refused: " + leaddaemon.SafeText(err.Error()))
		return nil
	}
	return k
}

// scratchKindPolicy is the kind policy of the service built on the scratch
// settings: the settings' "kinds" for their one project (every kind the
// verifier knows when they name none), and no kind for any other project,
// which this daemon does not serve.
func scratchKindPolicy(sc hostadapter.Scratch) func(project string) (credkind.Policy, bool) {
	p := sc.KindPolicy()
	return func(project string) (credkind.Policy, bool) {
		if project != sc.Project {
			return credkind.Policy{}, false
		}
		return p, true
	}
}

// withdrawer is the stand-in door's withdraw of a request, which no door
// interface has.
type withdrawer interface{ Withdraw(id string) error }

// standInEnder ends a request at the stand-in door, which has a withdraw of
// its own for it.
type standInEnder struct{ d withdrawer }

func (e standInEnder) EndRequest(_ context.Context, id string) error { return e.d.Withdraw(id) }

// scratchDoorConfig is the configuration of the stand-in door of the scratch
// settings, on the clock given. The stand-in keeps nothing across a restart
// and the journal keeps everything: its card numbers start from the time of
// day (and the adapter raises them past the journal's), not from 1, so that a
// restart does not make a card whose id the journal holds a decision for.
func scratchDoorConfig(sc hostadapter.Scratch, now func() time.Time) doortest.Config {
	cfg := doortest.Sample(now)
	cfg.FirstNumber = hostadapter.StandInFirstNumber(now())
	cfg.Seat.Project, cfg.Seat.RoleName = sc.Project, sc.Role
	cfg.Seat.Epoch, cfg.Seat.SeatRev = sc.Seat.Epoch, sc.Seat.SeatRev
	return cfg
}

// buildOwnerService is the [ownerServiceBuilder] of this build. It reads the
// scratch settings in the owner folder of the state folder, opens the host
// adapter on them, and returns the service with the function that closes
// everything. It refuses without settings that select the stand-in door and
// name the suite's sandbox template and work folder, and with a state folder, a
// scratch root or a mirror that is the account's default or not a scratch one.
// The suite is the real one built from the settings ("sandbox", "work"):
// building it starts nothing, and the template is read and the sandbox started
// only when a request is verified.
func buildOwnerService(stateDir string) (leaddaemon.OwnerService, func() error, error) {
	return buildOwnerServiceFrom(stateDir, stateScratch(stateDir))
}

// buildOwnerServiceFrom is [buildOwnerService] on the settings load reads in
// place of owner/scratch.json of the state folder: the [ownerBuildFrom] of
// this build, which run --owner-settings uses (a build with both tags). The
// journal, the list of keys and the records are the state folder's all the
// same.
func buildOwnerServiceFrom(stateDir string, load scratchLoad) (leaddaemon.OwnerService, func() error, error) {
	// The settings select the stand-in door, and the state folder, the roots
	// and the mirror are not the account's real ones. ownerRun makes the same
	// check before the daemon starts; this one is the builder's own.
	sc, err := checkOwnerScratchFrom(stateDir, load, ownerGetenv)
	if err != nil {
		return nil, nil, err
	}
	suite := ownerSuite
	if suite == nil {
		real, err := sc.VerifySuiteOf()
		if err != nil {
			return nil, nil, err
		}
		// The work folder is made private when it is not there; its parent
		// must exist. Nothing else is made or started.
		if err := os.Mkdir(real.WorkRoot, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, nil, fmt.Errorf("the work folder of the suite cannot be made: %w", err)
		}
		suite = real
	}
	git, err := gitenv.Find()
	if err != nil {
		return nil, nil, err
	}
	cfg := scratchDoorConfig(sc, time.Now)
	standIn := doortest.New(cfg)
	var d door.Door = standIn
	if ownerDoorWrap != nil {
		d = ownerDoorWrap(standIn)
	}
	// The ender withdraws through the door the adapter is given, when it
	// can, so that a test's wrapped door sees every call that changes the
	// stand-in, the withdraw included.
	var ender withdrawer = standIn
	if w, ok := d.(withdrawer); ok {
		ender = w
	}
	keys := openOwnerKeys(stateDir)
	o, err := hostadapter.Open(context.Background(), hostadapter.OpenConfig{
		Config: hostadapter.Config{
			Git: git, Projects: []hostadapter.Project{sc.ProjectOf()}, Suite: suite, Now: time.Now,
			Notice: func(line string) { ownerNotice(line) },
		},
		StateDir: stateDir, Door: d, Ender: standInEnder{ender},
		DropDir: sc.Drop, CollectProject: sc.Project,
		Service: ownerchannel.Config{
			Keys: keys, KindPolicy: scratchKindPolicy(sc),
			SeatLog: func(line string) { ownerNotice("seat gate: " + line) },
		},
	})
	if err != nil {
		if keys != nil {
			_ = keys.Close()
		}
		return nil, nil, err
	}
	return o.Service, o.Close, nil
}
