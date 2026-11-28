package leaddaemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"

	"ataniq/internal/clientapi"
	"ataniq/internal/instanceid"
	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
)

// DirectoryScope is an immutable snapshot of readable project IDs and their
// display names. An empty name uses the project ID. Nil denies the directory.
type DirectoryScope map[string]string

type directoryRegistry interface {
	List() ([]leadregistry.Entry, error)
	ProjectHome(string) (leadregistry.ProjectAssignment, error)
}

// DirectoryHandler serves a managed directory from local registry reads.
type DirectoryHandler struct {
	registry  directoryRegistry
	authority string
	scope     func(clientapi.Principal) DirectoryScope
}

var _ clientapi.ManagedHandler = (*DirectoryHandler)(nil)

// NewDirectoryHandler binds the registry to its instance identity and the
// current principal-to-project policy.
func NewDirectoryHandler(registry directoryRegistry, home instanceid.Identity, scope func(clientapi.Principal) DirectoryScope) *DirectoryHandler {
	return &DirectoryHandler{registry: registry, authority: home.Authority, scope: scope}
}

func (h *DirectoryHandler) policy(ctx context.Context) DirectoryScope {
	p, ok := clientapi.PrincipalFromContext(ctx)
	if !ok || p.Issuer == "" || p.ClientID == "" || ctx.Err() != nil || h.scope == nil || h.authority == "" {
		return nil
	}
	return h.scope(p)
}

func (h *DirectoryHandler) rows(scope DirectoryScope) ([]clientapi.DirectoryRow, error) {
	rows := []clientapi.DirectoryRow{}
	if len(scope) == 0 {
		return rows, nil
	}
	entries, err := h.registry.List()
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		project := e.Role.Project()
		name, visible := scope[project]
		if !visible || e.Role.Kind() != leadrole.KindLead {
			continue
		}
		a, err := h.registry.ProjectHome(project)
		if errors.Is(err, leadregistry.ErrHomeMissing) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if a.HomeAuthority != h.authority || a.Holder == nil || a.Holder.Role != e.Role ||
			a.Holder.Authority != h.authority || a.ExecutingInstance != h.authority {
			continue
		}
		if name == "" {
			name = project
		}
		id := clientapi.DirectoryTarget{
			Authority: h.authority, ProjectID: project, LeadRole: e.Role.String(),
			HomeGeneration: a.HomeGeneration, Revision: a.Revision, LaunchEpoch: e.Epoch,
		}
		rows = append(rows, clientapi.DirectoryRow{DirectoryTarget: id, Target: id.ID(), DisplayName: name})
	}
	slices.SortFunc(rows, func(a, b clientapi.DirectoryRow) int { return strings.Compare(a.ProjectID, b.ProjectID) })
	return rows, nil
}

// The server rechecks the requested target at delivery. Bind the aggregate
// target to its sorted rows so losing one row invalidates the whole response.
func (h *DirectoryHandler) snapshot(scope DirectoryScope) ([]clientapi.DirectoryRow, string, error) {
	rows, err := h.rows(scope)
	if err != nil {
		return nil, "", err
	}
	hash := sha256.New()
	if err := json.NewEncoder(hash).Encode(rows); err != nil {
		return nil, "", err
	}
	return rows, "directory/" + h.authority + "/" + hex.EncodeToString(hash.Sum(nil)), nil
}

func (h *DirectoryHandler) Capabilities(ctx context.Context) (clientapi.TargetCapabilities, error) {
	scope := h.policy(ctx)
	caps := clientapi.TargetCapabilities{}
	if scope == nil {
		return caps, nil
	}
	rows, target, err := h.snapshot(scope)
	if err != nil {
		return nil, err
	}
	caps[target] = []string{clientapi.OpDirectoryList}
	for _, row := range rows {
		caps[row.Target] = []string{}
	}
	return caps, nil
}

func (h *DirectoryHandler) HandleManaged(ctx context.Context, target, op string, _ json.RawMessage) (any, *clientapi.ErrorBody) {
	forbidden := &clientapi.ErrorBody{Code: clientapi.CodeForbidden, Message: "the managed request is not authorized"}
	scope := h.policy(ctx)
	if scope == nil || op != clientapi.OpDirectoryList {
		return nil, forbidden
	}
	rows, currentTarget, err := h.snapshot(scope)
	if err != nil {
		return nil, &clientapi.ErrorBody{Code: clientapi.CodeInternal, Message: "the directory could not be read"}
	}
	current := h.policy(ctx)
	if current == nil || !maps.Equal(current, scope) || target != currentTarget {
		return nil, forbidden
	}
	return clientapi.DirectoryListResult{Rows: rows}, nil
}
