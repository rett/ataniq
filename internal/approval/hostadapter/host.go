//go:build linux && ataniq_ownerchannel

package hostadapter

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"ataniq/internal/approval/mergebuild"
	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/record"
	"ataniq/internal/intake"
)

// Host holds the adapters. It implements [ownerchannel.Merger],
// [ownerchannel.Intake], [ownerchannel.Verifier] and [ownerchannel.Mailbox]
// for the projects of its configuration, and is safe for use by several
// goroutines: it keeps no state of its own but the intake's records folder,
// which locks its writes.
type Host struct {
	cfg      Config
	projects map[string]Project
	store    *intake.Store
	mailbox  *FileMailbox
}

var (
	_ ownerchannel.Merger   = (*Host)(nil)
	_ ownerchannel.Intake   = (*Host)(nil)
	_ ownerchannel.Verifier = (*Host)(nil)
)

// New makes a host over the verification records of store, which it does not
// close, and the reply folder, which is the one a [FileMailbox] writes in.
func New(cfg Config, store *intake.Store, mailbox *FileMailbox) (*Host, error) {
	if store == nil || mailbox == nil {
		return nil, errors.New("hostadapter: no records store or no mailbox")
	}
	if cfg.Suite == nil {
		return nil, errors.New("hostadapter: no suite runner")
	}
	if len(cfg.Projects) == 0 {
		return nil, errors.New("hostadapter: no project")
	}
	if cfg.GitTimeout < 0 {
		return nil, errors.New("hostadapter: the git time is negative")
	}
	h := &Host{cfg: cfg, projects: map[string]Project{}, store: store, mailbox: mailbox}
	for _, p := range cfg.Projects {
		if err := p.check(); err != nil {
			return nil, err
		}
		if _, dup := h.projects[p.Name]; dup {
			return nil, errors.New("hostadapter: two projects with one name")
		}
		h.projects[p.Name] = p
	}
	return h, nil
}

// project returns the project of a name the service was given. A name the
// host does not serve is a failure of the call, not a refusal of a lead's
// request.
func (h *Host) project(name string) (Project, error) {
	p, ok := h.projects[name]
	if !ok {
		return Project{}, errors.New("hostadapter: the host does not serve this project")
	}
	return p, nil
}

func (h *Host) gitCall(p Project) gitCall {
	t := h.cfg.GitTimeout
	if t == 0 {
		t = DefaultGitTimeout
	}
	return gitCall{git: h.cfg.Git, dir: p.Mirror, timeout: t}
}

func (h *Host) source(p Project) intake.Source {
	return intake.Source{Git: h.cfg.Git, GitDir: p.Mirror}
}

func (h *Host) repo(p Project) mergebuild.Repo {
	return mergebuild.Repo{Git: h.cfg.Git, GitDir: p.Mirror, Timeout: h.gitCall(p).timeout}
}

// Build implements [ownerchannel.Merger]: it builds the host's merge of the
// feature into the target and pins it under refs/ataniq/pending/ with the
// service's key.
func (h *Host) Build(ctx context.Context, in ownerchannel.MergeInput) (ownerchannel.BuiltMerge, error) {
	p, err := h.project(in.Project)
	if err != nil {
		return ownerchannel.BuiltMerge{}, err
	}
	m, err := mergebuild.Build(ctx, h.repo(p), mergebuild.Inputs{
		TargetTip: in.TargetTip, FeatureTip: in.FeatureTip, Target: in.Target, Feature: in.Feature,
		RequestID: in.RequestID, Identity: p.Identity, At: in.Collected,
	}, in.Key)
	built := ownerchannel.BuiltMerge{Commit: m.Commit, Parents: m.Parents}
	switch {
	case err == nil:
		return built, nil
	case errors.Is(err, mergebuild.ErrNothingToMerge):
		return ownerchannel.BuiltMerge{}, ownerchannel.ErrNothingToMerge
	case errors.Is(err, mergebuild.ErrPinUnclear):
		// The merge is written and the pin's state is not known: the
		// service looks at the pin and takes it away.
		return built, fmt.Errorf("%w: %w", ownerchannel.ErrPinUnclear, err)
	}
	return ownerchannel.BuiltMerge{}, err
}

// Pinned implements [ownerchannel.Merger]: the commit the pin of key holds,
// or "" when there is no pin.
func (h *Host) Pinned(ctx context.Context, key string) (string, error) {
	ref, err := mergebuild.PinRef(key)
	if err != nil {
		return "", err
	}
	// The service does not name the project of a pin; the pin lives in the
	// mirror of the one project the host serves, or is looked for in each.
	var found string
	for _, p := range h.orderedProjects() {
		got, err := h.gitCall(p).refs(ctx, ref)
		if err != nil {
			return "", err
		}
		if info, ok := got[ref]; ok {
			if found != "" && found != info.id {
				return "", errors.New("hostadapter: one pin key holds two commits in two mirrors")
			}
			found = info.id
		}
	}
	return found, nil
}

// Unpin implements [ownerchannel.Merger]: it removes the pin of key, which
// must hold commit.
func (h *Host) Unpin(ctx context.Context, key, commit string) error {
	ref, err := mergebuild.PinRef(key)
	if err != nil {
		return err
	}
	var errs []error
	removed := false
	for _, p := range h.orderedProjects() {
		got, err := h.gitCall(p).refs(ctx, ref)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if _, ok := got[ref]; !ok {
			continue
		}
		if err := mergebuild.Unpin(ctx, h.repo(p), key, commit); err != nil {
			errs = append(errs, err)
			continue
		}
		removed = true
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	if !removed {
		return mergebuild.ErrNoPin
	}
	return nil
}

// orderedProjects lists the projects by name, so that a walk over them is
// the same each time.
func (h *Host) orderedProjects() []Project {
	names := make([]string, 0, len(h.projects))
	for n := range h.projects {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]Project, 0, len(names))
	for _, n := range names {
		out = append(out, h.projects[n])
	}
	return out
}

// refusal makes a refusal with a host-written detail.
func refusal(c record.Code, detail string) *record.Error {
	return &record.Error{Code: c, Detail: detail}
}
