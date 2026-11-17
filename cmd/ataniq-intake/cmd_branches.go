package main

import (
	"strings"

	"ataniq/internal/exchange/leadtext"
	"ataniq/internal/mirror"
)

// cmdBranches lists the lead branches and the upstream branches.
func (e *env) cmdBranches(args []string) int {
	fs := flagSet("branches")
	var c common
	c.bind(fs)
	var role string
	fs.StringVar(&role, "role", "", "")
	if code, done := e.parse("branches", fs, args); done {
		return code
	}
	if err := e.check(c); err != nil {
		return e.fail("branches", err)
	}
	if role != "" {
		if err := checkRole(role); err != nil {
			return e.fail("branches", err)
		}
	}
	m, err := e.mirrorOf(c)
	if err != nil {
		return e.fail("branches", err)
	}
	roles := []string{role}
	if role == "" {
		roles, err = m.Roles(e.ctx)
		if err != nil {
			return e.fail("branches", err)
		}
	}
	type row struct{ role, branch, id, typ string }
	var rows []row
	for _, r := range roles {
		list, err := m.LeadBranches(e.ctx, r)
		if err != nil {
			return e.fail("branches", err)
		}
		for _, ref := range list {
			_, branch, ok := mirror.SplitLeadRef(ref.Name)
			if !ok {
				continue
			}
			rows = append(rows, row{r, branch, ref.ID, ref.Type})
		}
	}
	up, err := m.Upstream(e.ctx)
	if err != nil {
		return e.fail("branches", err)
	}

	p := &printer{w: e.stdout}
	p.f("%s", heading("Lead branches"))
	for _, r := range rows {
		line := indent + leadtext.Escape(r.role) + "  " + leadtext.Escape(r.branch) + "  " + r.id
		if r.typ != "commit" {
			line += "  (not a commit: " + leadtext.Escape(r.typ) + ")"
		}
		p.f("%s", line)
	}
	if len(rows) == 0 {
		p.f("%s(none)", indent)
	}
	p.f("Upstream branches:")
	n := 0
	for _, ref := range up {
		name, ok := strings.CutPrefix(ref.Name, "refs/heads/")
		if !ok {
			continue
		}
		n++
		p.f("%s%s  %s", indent, leadtext.Escape(name), ref.ID)
	}
	if n == 0 {
		p.f("%s(none)", indent)
	}
	return finish(p)
}
