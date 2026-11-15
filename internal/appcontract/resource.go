package appcontract

import (
	"math"

	"ataniq/internal/appmodel"
)

// Resource identifies one of the five resources with an independent version.
// Command observations instead use the target's status revision.
type Resource interface{ resource() }

type ProjectResource struct{ Project appmodel.ProjectRef }
type LeadResource struct {
	Project appmodel.ProjectRef
	Role    string
}
type ConversationResource struct {
	Project      appmodel.ProjectRef
	Conversation appmodel.ConversationRef
}
type ClientResource struct {
	Instance Instance
	Client   string
}
type PermissionResource struct {
	Project appmodel.ProjectRef
	Client  string
}
type CommandResource struct {
	Project appmodel.ProjectRef
	Key     appmodel.CommandKey
}

func (ProjectResource) resource()      {}
func (LeadResource) resource()         {}
func (ConversationResource) resource() {}
func (ClientResource) resource()       {}
func (PermissionResource) resource()   {}
func (CommandResource) resource()      {}

// ResourceRevision computes a token from a resource's own version. For a
// conversation that version is its admission version, not its queue state.
func (k ProjectionKeys) ResourceRevision(i Instance, resource Resource, version uint64) (string, error) {
	if !k.initialized || version == 0 || version > math.MaxInt64 {
		return "", errInvalid
	}
	kind, first, second, err := resourceFields(i, resource)
	if err != nil {
		return "", err
	}
	f := append(i.fields(), []byte(kind), []byte(first), []byte(second), number(version))
	return mac(k.resource, frame("RESOURCE", f...)), nil
}

func localProject(i Instance, p appmodel.ProjectRef) bool {
	return p.Authority == i.Authority && validID(p.ID)
}
func localConversation(i Instance, c appmodel.ConversationRef) bool {
	return c.Authority == i.Authority && validID(c.ID)
}

func resourceFields(i Instance, resource Resource) (string, string, string, error) {
	if !validID(i.Authority) {
		return "", "", "", errInvalid
	}
	var kind, first, second string
	switch r := resource.(type) {
	case ProjectResource:
		if !localProject(i, r.Project) {
			return "", "", "", errInvalid
		}
		kind, first = "project", r.Project.ID
	case LeadResource:
		if !localProject(i, r.Project) {
			return "", "", "", errInvalid
		}
		kind, first, second = "lead", r.Project.ID, r.Role
	case ConversationResource:
		if !localProject(i, r.Project) || !localConversation(i, r.Conversation) {
			return "", "", "", errInvalid
		}
		kind, first, second = "conversation", r.Project.ID, r.Conversation.ID
	case ClientResource:
		if r.Instance != i {
			return "", "", "", errInvalid
		}
		kind, first = "client", r.Client
	case PermissionResource:
		if !localProject(i, r.Project) {
			return "", "", "", errInvalid
		}
		kind, first, second = "permission", r.Client, r.Project.ID
	default:
		return "", "", "", errInvalid
	}
	if !validID(first) || ((kind == "lead" || kind == "conversation" || kind == "permission") && !validID(second)) {
		return "", "", "", errInvalid
	}
	return kind, first, second, nil
}
