package terminalconsole

// Fixture is an explicitly fictional, read-only demonstration snapshot.
type Fixture struct{}

func (Fixture) Snapshot() Projection {
	return Projection{ControlUnavailable: "Live controls require a qualified backend.", Projects: []Project{
		{Name: "Northstar (fixture)", Tasks: []string{"Review launch outline", "Collect design questions"}, Leads: []Lead{
			{Name: "Planning lead", Subagents: []string{"Research assistant", "Review assistant"}, Conversation: []string{"Operator: Explore the outline.", "Lead: Fixture conversation for inspection."}},
			{Name: "Delivery lead", Conversation: []string{"Fixture conversation: delivery notes."}},
		}},
		{Name: "Harbor (fixture)", Tasks: []string{"Organize notes"}, Leads: []Lead{{Name: "Harbor lead", Conversation: []string{"Fixture conversation: project notes."}}}},
	}}
}
