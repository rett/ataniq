//go:build linux && ataniq_ownerchannel

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"ataniq/internal/approval/door/doortest"
	"ataniq/internal/approval/hostadapter"
	"ataniq/internal/exchange/gitenv"
	"ataniq/internal/leaddaemon"
)

// This file is the one place in the command that builds the host's side of a
// card, for the scratch pass: the owner channel's service on the real journal
// in the state folder, the real merge, intake and records of the host adapter,
// the file-drop collector, and the stand-in for the planner's door. It is
// provisional and unqualified. Nothing here runs in a release build.

// ownerSuite is the suite runner the host's verifier uses. It is nil in this
// build: the verification sandbox of package verify takes a tip under
// refs/leads/, and the host's merge lives under refs/ataniq/pending/, so there
// is no real suite to hand over yet, and the daemon refuses to serve the owner
// channel without one rather than refuse every request. A test sets a fake.
var ownerSuite hostadapter.Suite

// ownerNotice receives the host-written lines of the service and the adapter.
// The default writes them to the standard error, one line each.
var ownerNotice = func(line string) { fmt.Fprintf(os.Stderr, "ataniqd: owner channel: %s\n", line) }

func init() { ownerServiceBuilder = buildOwnerService }

// standInEnder ends a request at the stand-in door, which has a withdraw of
// its own for it.
type standInEnder struct{ d *doortest.Door }

func (e standInEnder) EndRequest(_ context.Context, id string) error { return e.d.Withdraw(id) }

// buildOwnerService is the [ownerServiceBuilder] of this build. It reads the
// scratch settings in the owner folder of the state folder, opens the host
// adapter on them, and returns the service with the function that closes
// everything. It refuses without a suite runner and without settings.
func buildOwnerService(stateDir string) (leaddaemon.OwnerService, func() error, error) {
	suite := ownerSuite
	if suite == nil {
		return nil, nil, errors.New("this build has no verification suite for a merge yet, so no card can be made")
	}
	settingsPath := filepath.Join(stateDir, hostadapter.OwnerDirName, hostadapter.ScratchName)
	sc, err := hostadapter.LoadScratch(settingsPath)
	if err != nil {
		return nil, nil, err
	}
	git, err := gitenv.Find()
	if err != nil {
		return nil, nil, err
	}
	cfg := doortest.Sample(time.Now)
	cfg.Seat.Project, cfg.Seat.RoleName = sc.Project, sc.Role
	cfg.Seat.Epoch, cfg.Seat.SeatRev = sc.Seat.Epoch, sc.Seat.SeatRev
	standIn := doortest.New(cfg)
	o, err := hostadapter.Open(context.Background(), hostadapter.OpenConfig{
		Config: hostadapter.Config{
			Git: git, Projects: []hostadapter.Project{sc.ProjectOf()}, Suite: suite, Now: time.Now,
			Notice: func(line string) { ownerNotice(line) },
		},
		StateDir: stateDir, Door: standIn, Ender: standInEnder{standIn},
		DropDir: sc.Drop, CollectProject: sc.Project,
	})
	if err != nil {
		return nil, nil, err
	}
	return o.Service, o.Close, nil
}
