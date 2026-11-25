//go:build linux && ataniq_canary

package leaddaemon

import (
	"fmt"

	"ataniq/internal/leadcanary"
	"ataniq/internal/leadcanary/provision"
)

// This file names what a scratch lead's sandbox can reach on the host, from
// the scratch config alone, for a check that must keep a folder out of every
// lead's reach (the owner channel's settings of ataniqd run --owner-settings,
// cmd/ataniqd). Everything here is behind the ataniq_canary tag.

// LeadReach is one host path that the sandbox of a scratch lead can reach: a
// source of a bind of its bind table, or a folder that holds such sources.
type LeadReach struct {
	// Path is the host path as the config names it or as the layout derives
	// it: absolute and clean, not resolved. It may not exist yet (a lead
	// that is not defined has no folder).
	Path string
	// What says what the path is, in a few words, for a refusal.
	What string
}

// leadHostBinds are the host paths that every lead's bind table binds read
// only at the same path inside (profile.RoleHost), the optional ones
// included.
var leadHostBinds = []string{
	"/usr", "/etc/ssl", "/etc/ca-certificates", "/etc/hosts", "/etc/nsswitch.conf",
	"/etc/passwd", "/etc/group", "/etc/localtime",
}

// ScratchLeadReach returns every host path that the sandbox of a lead of the
// scratch config can reach, for every lead the config lists, whether or not
// it is defined yet: the host folders every bind table binds (/usr and a few
// /etc files), the agent install and the probe of the config's sandbox section,
// and, per lead, its folder under leads/ of the state root (its home, its
// provider config, its tree, its git metadata, records, requests and mail, and
// the copies provisioning binds from it: the managed settings, the skills, the
// instruction file, the git identity, the Qargi install, the assignment and
// the projection), its mirror, its mailbox, the folder its project's
// coordinators publish in and its holder's runtime folder (which holds the
// bound resolv.conf and spec folder). A folder that is, lies inside or holds
// one of these is within a lead's reach. It reads no file: the paths come from
// the config and the layout. A test holds the list to the profile's bind
// table.
func ScratchLeadReach(sc leadcanary.ScratchConfig) ([]LeadReach, error) {
	leads, err := sc.Leads()
	if err != nil {
		return nil, err
	}
	out := make([]LeadReach, 0, len(leadHostBinds)+2+6*len(leads))
	for _, p := range leadHostBinds {
		out = append(out, LeadReach{Path: p, What: "a host path every lead's sandbox binds"})
	}
	out = append(out,
		LeadReach{Path: sc.Sandbox.AgentInstall, What: "the agent install every lead's sandbox binds (sandbox.agent_install)"},
		LeadReach{Path: sc.Sandbox.Probe, What: "the probe every lead's sandbox binds (sandbox.probe)"},
	)
	for i, cfg := range leads {
		p, err := provision.PathsOf(cfg)
		if err != nil {
			return nil, err
		}
		key := sc.Entries[i].Key
		for _, r := range []LeadReach{
			{p.LeadDir, "its folder (home, provider config, tree and the copies its sandbox binds)"},
			{p.Mirror, "its mirror"},
			{p.Mailbox, "its mailbox"},
			{p.Published, "its coordinators' published folder"},
			{p.RuntimeFolder, "its holder's runtime folder (the bound resolv.conf and spec folder)"},
		} {
			r.What = fmt.Sprintf("the lead %s: %s", key, r.What)
			out = append(out, r)
		}
	}
	return out, nil
}
