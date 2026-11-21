//go:build linux && ataniq_ownerchannel

package hostadapter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"ataniq/internal/approval/mergebuild"
	"ataniq/internal/intake/execconfig"
	"ataniq/internal/intake/verify"
)

// ScratchName is the name of the scratch pass's settings file inside the owner
// folder of the state folder.
const ScratchName = "scratch.json"

// DoorStandIn is the one value the settings' door may have in this build: the
// planner's door is the in-memory stand-in, which gives no real approval.
const DoorStandIn = "stand-in"

// maxScratchRoots caps the list of scratch roots.
const maxScratchRoots = 8

// maxScratchBytes caps the settings file.
const maxScratchBytes = 64 << 10

// Scratch is the settings of the scratch pass: one project, its mirror, the
// folder requests are dropped in, and the seat the stand-in door stands for.
// It is read from a file in the daemon's state folder, strictly: an unknown
// key, a repeated key and text after the object are refused. Nothing in it is
// a secret, and the file holds no private name of the repository.
type Scratch struct {
	// Project, Mirror, Role and Target are those of [Project].
	Project string `json:"project"`
	Mirror  string `json:"mirror"`
	Role    string `json:"role"`
	Target  string `json:"target"`
	// Identity is the author and committer of the merge commit.
	Identity ScratchIdentity `json:"identity"`
	// Gates are the gates of the suite, in order.
	Gates []ScratchGate `json:"gates"`
	// Drop is the drop folder of the collector.
	Drop string `json:"drop"`
	// Seat is what the stand-in door is given: the epoch a request must
	// name and the revision of the seat's record.
	Seat ScratchSeat `json:"seat"`
	// Door selects the planner's door. Only [DoorStandIn] is accepted: the
	// settings must say that the pass runs on the stand-in.
	Door string `json:"door"`
	// Roots are the scratch roots: clean absolute folders, none of them a
	// default folder of the account or holding one. The mirror must lie
	// under one of them. The command checks both against the account; this
	// package checks only their form.
	Roots []string `json:"roots"`
}

// ScratchIdentity is the identity of the merge commit.
type ScratchIdentity struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// ScratchGate is one gate of the suite.
type ScratchGate struct {
	Name string   `json:"name"`
	Argv []string `json:"argv"`
}

// ScratchSeat is the seat of the stand-in door.
type ScratchSeat struct {
	Epoch   int64 `json:"epoch"`
	SeatRev int64 `json:"seat_rev"`
}

// LoadScratch reads the settings file at path. The file must be a regular
// file of the caller's, not reachable by the group or by others, of at most
// 64 KiB.
func LoadScratch(path string) (Scratch, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return Scratch{}, fmt.Errorf("hostadapter: the scratch settings: %w", err)
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0o077 != 0 || !ownedByMe(st) {
		return Scratch{}, errors.New("hostadapter: the scratch settings file is not the caller's private regular file")
	}
	b, err := readSmall(path, maxScratchBytes)
	if err != nil {
		return Scratch{}, errors.New("hostadapter: the scratch settings file could not be read")
	}
	var s Scratch
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return Scratch{}, errors.New("hostadapter: the scratch settings are not valid")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Scratch{}, errors.New("hostadapter: the scratch settings hold text after the object")
	}
	if s.Seat.Epoch < 0 || s.Seat.SeatRev < 0 || s.Drop == "" || !filepath.IsAbs(s.Drop) || filepath.Clean(s.Drop) != s.Drop {
		return Scratch{}, errors.New("hostadapter: the scratch settings name no clean absolute drop folder, or a negative number")
	}
	if s.Door != DoorStandIn {
		return Scratch{}, errors.New("hostadapter: the scratch settings do not select the stand-in door (\"door\": \"stand-in\")")
	}
	if len(s.Roots) == 0 || len(s.Roots) > maxScratchRoots {
		return Scratch{}, errors.New("hostadapter: the scratch settings name no scratch root, or too many")
	}
	for _, r := range s.Roots {
		if !filepath.IsAbs(r) || filepath.Clean(r) != r {
			return Scratch{}, errors.New("hostadapter: a scratch root is not a clean absolute path")
		}
	}
	if err := s.ProjectOf().check(); err != nil {
		return Scratch{}, err
	}
	return s, nil
}

// ProjectOf is the [Project] the settings describe, with the built-in rule set.
func (s Scratch) ProjectOf() Project {
	gates := make([]verify.Gate, 0, len(s.Gates))
	for _, g := range s.Gates {
		gates = append(gates, verify.Gate{Name: g.Name, Argv: append([]string(nil), g.Argv...)})
	}
	return Project{
		Name: s.Project, Mirror: s.Mirror, Role: s.Role, Target: s.Target, Rules: execconfig.Default(),
		Identity: mergebuild.Identity{Name: s.Identity.Name, Email: s.Identity.Email}, Gates: gates,
	}
}
