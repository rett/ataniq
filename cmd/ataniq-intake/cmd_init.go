package main

import (
	"os"

	"ataniq/internal/mirror"
)

// cmdInit creates the exchange mirror of a project.
func (e *env) cmdInit(args []string) int {
	fs := flagSet("init")
	var c common
	c.bind(fs)
	if code, done := e.parse("init", fs, args); done {
		return code
	}
	if err := e.check(c); err != nil {
		return e.fail("init", err)
	}
	path, err := mirror.Path(c.state, c.project)
	if err != nil {
		return e.fail("init", usagef("%v", err))
	}
	// The mirrors folder is the only folder that init makes besides the
	// mirror. Create checks that it is a real folder that the owner owns.
	parent := mirrorsDir(c.state)
	has, err := exists(parent)
	if err != nil {
		return e.fail("init", err)
	}
	if !has {
		if err := os.Mkdir(parent, 0o700); err != nil {
			return e.fail("init", err)
		}
		if err := os.Chmod(parent, 0o700); err != nil {
			return e.fail("init", err)
		}
	}
	if has, err := exists(path); err != nil {
		return e.fail("init", err)
	} else if has {
		return e.fail("init", refusalf("the mirror of this project exists already"))
	}
	m, err := mirror.Create(e.ctx, e.deps.Git, path)
	if err != nil {
		return e.fail("init", err)
	}
	p := &printer{w: e.stdout}
	p.f("Created the exchange mirror of project %s.", c.project)
	p.f("Mirror: %s", safeText(m.Path()))
	p.f("Next: fetch a lead's branches into it with '%s fetch'.", program)
	return finish(p)
}

// finish returns the exit status after the output was written.
func finish(p *printer) int {
	if p.err != nil {
		return exitFailed
	}
	return exitOK
}
