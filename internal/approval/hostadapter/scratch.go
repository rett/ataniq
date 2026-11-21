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
	"strings"

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
	// Sandbox is the clean absolute path of the sandbox template file the
	// suite runs in, the one the intake's verify takes as --sandbox. It
	// is optional here and needed to run the real suite; it must not be
	// in the state folder, which is kept for the host's records and journal
	// and free of configuration. Work is the
	// clean absolute path of the private folder the runs work in, which
	// must not be in the state folder either. Both lie under a scratch
	// root (the command checks it), the template is in neither the drop
	// folder, the mirror nor the work folder, and the template is read by each run.
	Sandbox string `json:"sandbox,omitempty"`
	Work    string `json:"work,omitempty"`
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
	if err := noRepeatedKey(b); err != nil {
		return Scratch{}, errors.New("hostadapter: the scratch settings are not valid: a key appears twice in one object")
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
	for _, p := range []string{s.Sandbox, s.Work} {
		if p != "" && (!filepath.IsAbs(p) || filepath.Clean(p) != p || strings.ContainsAny(p, "\x00\n")) {
			return Scratch{}, errors.New("hostadapter: the scratch settings name a sandbox template or a work folder that is not a clean absolute path")
		}
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

// noRepeatedKey returns an error when a JSON object of b, at any depth, names
// one key twice. The decoder of package json keeps the last value of a repeated
// key without a word, so the text is scanned token by token first. A text that
// is not JSON is left to the decoder, which refuses it.
func noRepeatedKey(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	var value func() error
	value = func() error {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		d, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		switch d {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				k, err := dec.Token()
				if err != nil {
					return err
				}
				key, _ := k.(string)
				// encoding/json matches a key to a field without regard to
				// case, so "drop" and "Drop" are one key to the decoder.
				key = strings.ToLower(key)
				if seen[key] {
					return errRepeatedKey
				}
				seen[key] = true
				if err := value(); err != nil {
					return err
				}
			}
		case '[':
			for dec.More() {
				if err := value(); err != nil {
					return err
				}
			}
		}
		_, err = dec.Token() // the closing delimiter
		return err
	}
	if err := value(); errors.Is(err, errRepeatedKey) {
		return err
	}
	return nil
}

var errRepeatedKey = errors.New("a key appears twice in one object")

// VerifySuiteOf is the real suite the settings describe, with the real runner.
// It refuses settings that name no sandbox template or no work folder, and
// starts nothing.
func (s Scratch) VerifySuiteOf() (*VerifySuite, error) {
	if s.Sandbox == "" || s.Work == "" {
		return nil, errors.New("hostadapter: the scratch settings name no \"sandbox\" template and \"work\" folder, so there is no suite to run")
	}
	v := &VerifySuite{Template: s.Sandbox, WorkRoot: s.Work}
	if err := v.Check(); err != nil {
		return nil, err
	}
	return v, nil
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
