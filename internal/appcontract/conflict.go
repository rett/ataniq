package appcontract

import "ataniq/internal/appmodel"

// ConflictView is a private observation, separate from appmodel.DomainError.
// A value alone does not establish authority to observe or disclose it.
type ConflictView interface{ conflictView() }
type ConversationAdmissionConflict struct {
	Project      appmodel.ProjectRef
	Conversation appmodel.ConversationRef
	Revision     string
}
type ExactEnqueueStatusConflict struct {
	Project appmodel.ProjectRef
	Key     appmodel.CommandKey
	Status  appmodel.CurrentStatus
}
type AdminResourceConflict struct {
	Resource Resource
	Observed Precondition
}

func (ConversationAdmissionConflict) conflictView() {}
func (ExactEnqueueStatusConflict) conflictView()    {}
func (AdminResourceConflict) conflictView()         {}

// ValidateConflictView enforces shape and local identity without deciding
// whether a caller may see the observation.
func ValidateConflictView(i Instance, view ConflictView) error {
	if !validID(i.Authority) {
		return errInvalid
	}
	switch v := view.(type) {
	case ConversationAdmissionConflict:
		if !localProject(i, v.Project) || !localConversation(i, v.Conversation) {
			return errInvalid
		}
		_, err := rawRevision(v.Revision)
		return err
	case ExactEnqueueStatusConflict:
		if !localProject(i, v.Project) || v.Key.Authority != i.Authority || !validID(v.Key.EnrolledClientID) || !validID(string(v.Key.CommandID)) || v.Status.Command != v.Key || !projectedState(v.Status.State) {
			return errInvalid
		}
		_, err := rawRevision(string(v.Status.Revision))
		return err
	case AdminResourceConflict:
		// A Command observation must use ExactEnqueueStatusConflict instead.
		if _, _, _, err := resourceFields(i, v.Resource); err != nil {
			return err
		}
		_, err := preconditionBytes(v.Observed, true, true)
		return err
	default:
		return errInvalid
	}
}
