package clientapi

import (
	"strconv"
	"strings"
)

// OpDirectoryList reads the rows visible to the verified principal. Use the
// target advertising this operation in a v2 hello. If a changed directory
// makes the target stale, obtain a fresh hello.
const OpDirectoryList = "directory.list"

// DirectoryTarget identifies one assigned project lead at its home and epoch.
type DirectoryTarget struct {
	Authority      string `json:"authority"`
	ProjectID      string `json:"project_id"`
	LeadRole       string `json:"lead_role"`
	HomeGeneration uint64 `json:"home_generation"`
	Revision       uint64 `json:"revision"`
	LaunchEpoch    uint64 `json:"launch_epoch"`
}

// ID encodes the six fields in declaration order after "lead/". Each UTF-8
// field has a decimal byte length, a colon and its bytes. Unsigned integers
// use decimal without leading zeroes. Clients compare the entire opaque ID.
func (t DirectoryTarget) ID() string {
	var b strings.Builder
	b.WriteString("lead/")
	for _, field := range []string{t.Authority, t.ProjectID, t.LeadRole,
		strconv.FormatUint(t.HomeGeneration, 10), strconv.FormatUint(t.Revision, 10), strconv.FormatUint(t.LaunchEpoch, 10)} {
		b.WriteString(strconv.Itoa(len(field)))
		b.WriteByte(':')
		b.WriteString(field)
	}
	return b.String()
}

// DirectoryRow carries the opaque target and its qualified identity. The
// display name can repeat. ConversationID stays empty and ConversationAvailable
// false until the home has an authoritative conversation binding.
//
// Launched tells whether the lead has been launched at least once. When it is
// false the lead has no launch epoch yet and LaunchEpoch is 0 only as a
// placeholder: a client must not read it as an epoch. When it is true,
// LaunchEpoch is the epoch of the latest launch and is at least 1.
type DirectoryRow struct {
	DirectoryTarget
	Target                string `json:"target"`
	Launched              bool   `json:"launched"`
	DisplayName           string `json:"display_name"`
	ConversationID        string `json:"conversation_id"`
	ConversationAvailable bool   `json:"conversation_available"`
}

// DirectoryListResult contains only this principal's locally assigned leads.
type DirectoryListResult struct {
	Rows []DirectoryRow `json:"rows"`
}
