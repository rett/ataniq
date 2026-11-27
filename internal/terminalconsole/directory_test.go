package terminalconsole

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestCF01(t *testing.T) {
	project := func(home string) Project {
		return Project{HomeID: home, ID: "project", Name: "shared", Leads: []Lead{
			{ID: "lead", ConversationID: "thread", Name: "shared", Epoch: 7, Conversation: []string{home + " conversation", "next", "last"}},
		}}
	}
	east, west := project("east"), project("west")
	enter := tea.KeyPressMsg{Code: tea.KeyEnter}
	t.Run("EqualNamesTwoHomes", func(t *testing.T) {
		src := &fakeSource{p: Projection{Projects: []Project{east, west}}}
		r := newSourceRig(t, src, 70, 20, Options{Label: testLabel})
		r.typ("j")
		r.key(enter)
		r.key(enter)
		r.typ("jj")
		renamed := project("west")
		renamed.Name, renamed.Leads[0].Name = "renamed project", "renamed lead"
		src.set(Projection{Projects: []Project{renamed, east}}, nil)
		r.refresh(time.Second)
		if r.m.project != 0 || r.m.page != "conversation" || r.m.scroll != 2 || !strings.Contains(r.view(), "west conversation") {
			t.Fatal(r.view())
		}
		back := tea.KeyPressMsg{Code: tea.KeyEscape}
		r.key(back)
		r.key(back)
		r.typ("j")
		r.key(enter)
		r.key(enter)
		if r.m.scroll != 0 || !strings.Contains(r.view(), "east conversation") {
			t.Fatal(r.view())
		}
		r.key(back)
		r.key(back)
		r.typ("k")
		r.key(enter)
		r.key(enter)
		if r.m.scroll != 2 {
			t.Fatalf("resume position %d, want 2", r.m.scroll)
		}
	})
	t.Run("EpochChange", func(t *testing.T) {
		src := &fakeSource{p: Projection{Projects: []Project{west}}}
		r := newSourceRig(t, src, 70, 20, Options{Label: testLabel})
		r.key(enter)
		r.key(enter)
		r.typ("jj")
		next := project("west")
		next.Leads[0].Epoch++
		src.set(Projection{Projects: []Project{next}}, nil)
		r.refresh(time.Second)
		r.key(enter)
		if r.m.page != "leads" || r.m.lead >= 0 || len(r.m.resume) != 0 {
			t.Fatal(r.view())
		}
		r.typ("j")
		r.key(enter)
		if r.m.page != "conversation" || r.m.scroll != 0 {
			t.Fatal(r.view())
		}
	})
	t.Run("Offline", func(t *testing.T) {
		src := &fakeSource{p: Projection{Projects: []Project{east, west}}}
		r := newSourceRig(t, src, 140, 45, Options{Label: testLabel})
		r.typ("jjj")
		r.key(enter)
		src.set(Projection{Projects: []Project{east}, OfflineHomes: []string{"west"}}, nil)
		r.refresh(time.Second)
		r.key(enter)
		if r.m.project != -1 || !strings.Contains(r.view(), "[stale] west") || len(r.m.treeRows()) != 4 {
			t.Fatal(r.view())
		}
		r.typ("j")
		r.key(enter)
		if p, _, _ := r.m.selected(); p != "east / shared" {
			t.Fatal(r.view())
		}
		src.set(Projection{Projects: []Project{west, east}}, nil)
		r.refresh(time.Second)
		if p, _, _ := r.m.selected(); p != "east / shared" || strings.Contains(r.view(), "stale") {
			t.Fatal(r.view())
		}
		src.set(Projection{Offline: true, Problem: "no daemon answers"}, nil)
		r.refresh(time.Second)
		r.typ("L")
		if r.m.leadsOpen() || len(r.m.controls) != 0 || r.m.project >= 0 ||
			!strings.Contains(r.view(), "[stale] east") || !strings.Contains(r.view(), "[stale] west") {
			t.Fatal(r.view())
		}
		// A successful empty home is authoritative. Its cached rows must go.
		src.set(Projection{OfflineHomes: []string{"east"}}, nil)
		r.refresh(time.Second)
		if len(r.m.projects) != 1 || r.m.projects[0].HomeID != "east" || !r.m.projects[0].Offline {
			t.Fatalf("unexpected cached rows: %+v", r.m.projects)
		}
	})
}
