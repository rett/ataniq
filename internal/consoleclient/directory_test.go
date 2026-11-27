package consoleclient

import (
	"testing"

	"ataniq/internal/clientapi"
)

func TestScratchDirectoryKeysSurviveDisplayCleaning(t *testing.T) {
	row := func(project string) clientapi.LeadStatus {
		return clientapi.LeadStatus{Kind: clientapi.KindLead, Project: project, Role: project + "/lead", Epoch: 8}
	}
	a, b := row("same"), row("same\u200b")
	view := func(rows ...clientapi.LeadStatus) []Project {
		return buildView(clientapi.HelloResult{}, clientapi.StatusResult{Leads: rows}).Projects
	}
	first, second := view(a, b), view(b, a)
	if len(first) != 2 || len(second) != 2 {
		t.Fatal("lost a project")
	}
	if first[0].Name != first[1].Name || first[0].Lead.Name != first[1].Lead.Name {
		t.Fatal("expected equal display names")
	}
	if first[0].ID == first[1].ID || first[0].Lead.ID == first[1].Lead.ID {
		t.Fatal("equal display names merged identities")
	}
	for i, p := range first {
		q := second[1-i]
		if p.ID != q.ID || p.Lead.ID != q.Lead.ID || q.Lead.Epoch != 8 || p.HomeID != "" || p.Lead.ConversationID != "" {
			t.Fatal("incorrect local inventory identity")
		}
	}
}
