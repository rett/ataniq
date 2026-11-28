package marshaljson

// The pinned registries express x-when conditions in prose. Shape validation
// has already rejected null for each of the conditional fields checked here.
func fieldsPresent(obj map[string]any, want bool, keys ...string) bool {
	for _, key := range keys {
		if _, present := obj[key]; present != want {
			return false
		}
	}
	return true
}

func (d Document) completeRecord(record map[string]any) bool {
	if d.kind == Status && !fieldsPresent(record, true, "tasks", "agent_branches", "worktrees", "overlap") {
		return false
	}
	if !fieldsPresent(record, d.fields["backend"] == "refs", "provisional") || !completeLead(record["lead"]) {
		return false
	}
	// Only handoff states the project's integrity mode. An inventory row
	// without the triplet cannot establish that integrity was switched off.
	wantIntegrity := d.kind != Handoff || d.fields["integrity"] != "off"
	return fieldsPresent(record, wantIntegrity, "integrity", "integrity_note", "signer")
}

func completeLead(v any) bool {
	lead, _ := v.(map[string]any)
	// A lead with no recorded worktree cannot establish its worktree identity.
	// succeeds is the optional predecessor ID emitted for a succession.
	return lead == nil || fieldsPresent(lead, true, "worktree")
}

func completeSuite(suite map[string]any) bool {
	switch suite["state"] {
	case "unknown":
		return false // The last run could not be read.
	case "not-run":
		return fieldsPresent(suite, false, "sha", "result", "at")
	default:
		return fieldsPresent(suite, true, "sha", "result", "at")
	}
}

func completeEnvironment(env map[string]any) bool {
	check, _ := env["check"].(map[string]any)
	apply, _ := env["apply"].(map[string]any)
	if env["recorded"] != (check != nil || apply != nil) {
		return false
	}
	// J1 says "the record holds it" without a separate presence indicator.
	// A partial recorded check or apply cannot establish complete evidence.
	return (check == nil || fieldsPresent(check, true, "verdict", "exit", "missing", "expected", "time", "doctor_command", "builtin_minimum")) &&
		(apply == nil || fieldsPresent(apply, true, "exit", "time", "command", "allow_root"))
}
