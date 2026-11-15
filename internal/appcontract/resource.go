package appcontract

import "math"

// Resource identifies one of the five resources with an independent version.
// Command observations instead use the target's status revision.
type Resource interface{ resource() }

type ProjectResource struct{ Project string }
type LeadResource struct{ Project, Role string }
type ConversationResource struct{ Project, Conversation string }
type ClientResource struct{ Client string }
type PermissionResource struct{ Project, Client string }

func (ProjectResource) resource()      {}
func (LeadResource) resource()         {}
func (ConversationResource) resource() {}
func (ClientResource) resource()       {}
func (PermissionResource) resource()   {}

// ResourceRevision computes a token from a resource's own version. For a
// conversation that version is its admission version, not its queue state.
func (k ProjectionKeys) ResourceRevision(i Instance, resource Resource, version uint64) (string, error) {
	if !validID(i.Authority) || version == 0 || version > math.MaxInt64 {
		return "", errInvalid
	}
	var kind, first, second string
	switch r := resource.(type) {
	case ProjectResource:
		kind, first = "project", r.Project
	case LeadResource:
		kind, first, second = "lead", r.Project, r.Role
	case ConversationResource:
		kind, first, second = "conversation", r.Project, r.Conversation
	case ClientResource:
		kind, first = "client", r.Client
	case PermissionResource:
		kind, first, second = "permission", r.Client, r.Project
	default:
		return "", errInvalid
	}
	if !validID(first) || ((kind == "lead" || kind == "conversation" || kind == "permission") && !validID(second)) {
		return "", errInvalid
	}
	f := append(i.fields(), []byte(kind), []byte(first), []byte(second), number(version))
	return mac(k.resource, frame("RESOURCE", f...)), nil
}
