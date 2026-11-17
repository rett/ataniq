package leaddaemon

import (
	"context"
	"time"

	"ataniq/internal/clientapi"
	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
)

// daemonInfo is the part of the daemon block that is fixed once the reconcile
// is done.
type daemonInfo struct {
	pid       int
	startedAt time.Time
	bootID    string
	completed bool
	at        time.Time
}

// handler is the [clientapi.Handler]. It holds no lock of its own: the daemon
// block is read-only after the reconcile, and the registry hands out copies.
type handler struct {
	reg     *leadregistry.Registry
	version string
	info    daemonInfo
}

var _ clientapi.Handler = (*handler)(nil)

// Hello supplies the version.
func (h *handler) Hello(context.Context) clientapi.HelloResult {
	return clientapi.HelloResult{Version: h.version}
}

// Status maps the registry to the wire. It returns as soon as the context
// ends and never writes to a client: the server does, after it returns.
func (h *handler) Status(ctx context.Context) (clientapi.StatusResult, error) {
	if err := ctx.Err(); err != nil {
		return clientapi.StatusResult{}, err
	}
	entries, err := h.reg.List()
	if err != nil {
		return clientapi.StatusResult{}, err
	}
	rev, err := h.reg.Revision()
	if err != nil {
		return clientapi.StatusResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return clientapi.StatusResult{}, err
	}
	at := h.info.at
	res := clientapi.StatusResult{
		Daemon: clientapi.DaemonStatus{
			PID:                    h.info.pid,
			StartedAt:              h.info.startedAt,
			RegistryRevision:       rev,
			BootID:                 h.info.bootID,
			LastReconcileCompleted: h.info.completed,
			LastReconcileAt:        &at,
		},
		Leads: make([]clientapi.LeadStatus, 0, len(entries)),
	}
	for _, e := range entries {
		res.Leads = append(res.Leads, leadStatus(e))
	}
	return res, nil
}

// leadStatus maps one registry entry. The requested and the observed profile
// are mapped from their own fields and never from each other: a field that
// was not observed is shown as not observed, with no value.
func leadStatus(e leadregistry.Entry) clientapi.LeadStatus {
	s := clientapi.LeadStatus{
		Role:         e.Role.String(),
		Kind:         e.Role.Kind().String(),
		Project:      e.Role.Project(),
		Manager:      string(e.Manager),
		Epoch:        e.Epoch,
		Desired:      string(e.Desired),
		State:        string(e.Shown.State),
		ReasonCode:   e.Shown.Reason.Code,
		ReasonDetail: e.Shown.Reason.Detail,
		EvidenceKind: string(e.Shown.Evidence),
		Since:        e.Shown.At,
		Requested: clientapi.RequestedProfile{
			Provider:       string(e.Requested.Provider),
			Version:        e.Requested.Version,
			Model:          e.Requested.Model,
			Effort:         e.Requested.Effort,
			PermissionMode: e.Requested.PermissionMode,
			AccessMode:     e.Requested.AccessMode,
		},
		Observed: clientapi.ObservedProfile{
			Provider:       observed(e.Observed.Provider),
			Version:        observed(e.Observed.Version),
			Model:          observed(e.Observed.Model),
			Effort:         observed(e.Observed.Effort),
			PermissionMode: observed(e.Observed.PermissionMode),
			AccessMode:     observed(e.Observed.AccessMode),
		},
	}
	if in := e.Intent; in != nil {
		s.LaunchIntent = &clientapi.LaunchIntent{ID: in.ID, Epoch: in.Epoch, Unit: in.UnitName, Outcome: string(in.Outcome)}
	}
	if e.Role.Kind() == leadrole.KindCoordinator {
		s.Feature = e.Role.Feature()
		s.Parent = e.Role.Parent().String()
		instance := e.Epoch
		s.Instance = &instance
		if in := e.Intent; in != nil {
			parentEpoch := in.ParentEpoch
			s.ParentEpoch = &parentEpoch
			s.Identity = in.Identity
		}
	}
	return s
}

func observed(f leadregistry.Field) clientapi.Observed {
	if !f.Observed {
		return clientapi.Observed{}
	}
	return clientapi.Observe(f.Value)
}
