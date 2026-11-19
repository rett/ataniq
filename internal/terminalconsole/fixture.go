package terminalconsole

// Fixture is an explicitly fictional, read-only demonstration snapshot.
type Fixture struct{}

func (Fixture) Snapshot() Projection {
	unqualified := "Unqualified: the console has no live backend."
	return Projection{
		ControlUnavailable: "Live controls require a qualified backend.",
		Operator:           "Fixture operator",
		Gate: Gate{
			Action:   "Merge feature branch into develop",
			Scope:    "Fixture project, one branch",
			Target:   "develop",
			Approver: "Project owner",
			State:    "Pending (fixture, no approval is possible)",
		},
		Contract: []Check{
			{Name: "Verify the project, lead or session", State: "unqualified"},
			{Name: "Reconcile the exclusive input owner", State: "unqualified"},
			{Name: "Qualify detach and recovery", State: "unqualified"},
		},
		Timeline: []TimelineEntry{
			{Time: "09:00", Text: "Fixture lead bound to the fixture project"},
			{Time: "09:02", Text: "Fixture recording started"},
			{Time: "09:05", Text: "Gate raised: merge into develop"},
			{Time: "09:06", Text: "No owner channel is qualified; the gate stays pending"},
		},
		Controls: []Control{
			{Name: "Approve pending gate", Key: "Ctrl+K", Reason: "No grant is honoured until the owner channel is qualified.", Approval: true},
			{Name: "Deny pending gate", Key: "Ctrl+K", Reason: "No grant is honoured until the owner channel is qualified.", Approval: true},
			{Name: "Full attach", Key: "a", Reason: "Full attach needs a qualified holder and an exclusive input owner. " + unqualified},
			{Name: "Send to lead", Key: "Ctrl+K", Reason: "Sending needs a qualified command and receipt path. " + unqualified},
			{Name: "Interrupt lead", Key: "Ctrl+K", Reason: "Interrupting needs a qualified command and receipt path. " + unqualified},
			{Name: "Detach", Key: "Ctrl+K", Reason: "Detach and recovery are not qualified."},
		},
		Projects: []Project{
			{Name: "Northstar (fixture)", Tasks: []string{"Review launch outline", "Collect design questions"}, Leads: []Lead{
				{Name: "Planning lead", Subagents: []string{"Research assistant", "Review assistant"}, Conversation: []string{"Operator: Explore the outline.", "Lead: Fixture conversation for inspection."},
					Session: Session{Provider: "Fixture provider", Runtime: "Fixture recording", Authority: "None (read-only)", Host: "fixture-host", ID: "fixture-session-1", Attachment: "Unqualified (fixture)", Binding: "Bound (fixture)", Execution: "Idle (fixture)"}},
				{Name: "Delivery lead", Conversation: []string{"Fixture conversation: delivery notes."},
					Session: Session{Provider: "Fixture provider", Runtime: "Fixture recording", Authority: "None (read-only)", Host: "fixture-host", ID: "fixture-session-2", Attachment: "Unqualified (fixture)", Binding: "Bound (fixture)", Execution: "Idle (fixture)"}},
			}},
			{Name: "Harbor (fixture)", Tasks: []string{"Organize notes"}, Leads: []Lead{{Name: "Harbor lead", Conversation: []string{"Fixture conversation: project notes."},
				Session: Session{Provider: "Fixture provider", Runtime: "Fixture recording", Authority: "None (read-only)", Host: "fixture-host", ID: "fixture-session-3", Attachment: "Unqualified (fixture)", Binding: "Unbound (fixture)", Execution: "Stopped (fixture)"}}}},
		},
	}
}
