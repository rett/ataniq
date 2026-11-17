package main

import (
	"fmt"
	"path/filepath"

	"ataniq/internal/exchange/safefs"
	"ataniq/internal/exchange/sandboxplan"
)

// loadTemplate reads the sandbox template at path and returns it as a
// planner. The file is the owner's. It is opened through safefs, so a
// symlink as the file is refused, and at most sandboxplan.MaxTemplateBytes
// are read. The template says which commands run and the command trusts it:
// it is not a statement that the commands confine anything.
func loadTemplate(path string) (sandboxplan.Planner, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("sandbox template: %w", err)
	}
	root, err := safefs.OpenRoot(filepath.Dir(abs))
	if err != nil {
		return nil, fmt.Errorf("sandbox template: cannot open its folder: %w", err)
	}
	defer root.Close()
	data, err := root.ReadFile(filepath.Base(abs), sandboxplan.MaxTemplateBytes)
	if err != nil {
		return nil, fmt.Errorf("sandbox template: cannot read it: %w", err)
	}
	t, err := sandboxplan.ParseTemplate(data)
	if err != nil {
		return nil, err
	}
	return t, nil
}

// planner loads the sandbox template named by --sandbox. A template that
// cannot be used is a refusal: the command never runs anything unsandboxed.
// The file must not be in the state root, where a lead could change it.
func (e *env) planner(path, state string) (sandboxplan.Planner, error) {
	if path == "" {
		return nil, usagef("--sandbox is required: the command never runs anything unsandboxed")
	}
	if e.deps.LoadPlanner == nil {
		return nil, refusalf("no sandbox template loader: nothing was run")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, usagef("--sandbox: %v", err)
	}
	if err := notInState("sandbox", filepath.Dir(abs), state); err != nil {
		return nil, err
	}
	p, err := e.deps.LoadPlanner(abs)
	if err != nil {
		return nil, refusalf("no usable sandbox template, nothing was run: %v", err)
	}
	if p == nil {
		return nil, refusalf("no usable sandbox template, nothing was run")
	}
	return p, nil
}
