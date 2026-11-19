//go:build linux && ataniq_canary

package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"ataniq/internal/leadcanary"
	"ataniq/internal/leadcanary/provision"
)

// This file holds the two subcommands the attended run script uses for what a
// shell cannot do safely: read the config and check the sandbox inputs file.
// Neither changes anything on the host.

// showLines gives the values of the config that the run script needs, as
// "key=value" lines in a fixed order. Every value is a name, a number or a
// path: the environment of the agent, the git identity and the profile digest
// are not among them, and no file is read but the config. The agent's argument
// vector is printed (agent_argc, then agent_argv_0 and so on), because the
// script shows the model argument for the owner to confirm; a secret belongs
// in no argument. A value that holds a control character is refused, so that a
// line is always one line.
func showLines(cfg leadcanary.Config) ([]string, error) {
	layoutRole, unit, err := leadcanary.Names(cfg.Role)
	if err != nil {
		return nil, err
	}
	p, err := provision.PathsOf(cfg)
	if err != nil {
		return nil, err
	}
	u := func(n uint64) string { return strconv.FormatUint(n, 10) }
	pairs := [][2]string{
		{"role", cfg.Role.String()},
		{"project", cfg.Role.Project()},
		{"layout_role", layoutRole},
		{"unit", unit},
		{"state_root", cfg.StateRoot},
		{"runtime_root", cfg.RuntimeRoot},
		{"marker", p.Marker},
		{"holder_executable", cfg.Holder.Executable},
		{"holder_state_dir", p.Holder},
		{"holder_runtime_dir", p.RuntimeFolder},
		{"evidence", cfg.EvidencePath()},
		{"sandbox_inputs", cfg.Holder.SandboxInputs},
		{"inject", strconv.FormatBool(cfg.Inject)},
		{"limit_memory_max_bytes", u(cfg.Limits.MemoryMaxBytes)},
		{"limit_swap_max_bytes", u(cfg.Limits.SwapMaxBytes)},
		{"limit_tasks_max", u(cfg.Limits.TasksMax)},
		{"limit_memory_floor_bytes", u(cfg.Limits.MemoryFloorBytes)},
		{"limit_memory_share_percent", strconv.Itoa(cfg.Limits.MemorySharePercent)},
		{"agent_kind", string(cfg.Agent.Kind)},
		{"agent_runtime", cfg.Agent.Runtime},
		{"agent_runtime_version", cfg.Agent.RuntimeVersion},
		{"agent_footer_mode", cfg.Agent.FooterMode},
		{"agent_argc", strconv.Itoa(len(cfg.Agent.Argv))},
	}
	for i, a := range cfg.Agent.Argv {
		pairs = append(pairs, [2]string{"agent_argv_" + strconv.Itoa(i), a})
	}
	pairs = append(pairs, [][2]string{
		{"mirror", p.Mirror},
		{"upstream_branch", cfg.Provision.UpstreamBranch},
		{"lead_dir", p.LeadDir},
		{"lead_home", p.Home},
		{"lead_config_dir", p.ConfigDir},
		{"lead_tree", p.Tree},
		{"replay_dir", p.ReplayDir},
		{"replay_switch", p.ReplaySwitch},
		{"source_managed_settings", p.ManagedSettings},
		{"source_lead_skills", p.LeadSkills},
		{"source_instruction_file", p.InstructionFile},
		{"source_git_identity_dir", p.GitIdentityDir},
		{"source_qargi_install", p.QargiInstall},
		{"source_assignment_dir", p.AssignmentDir},
		{"source_projection_dir", p.ProjectionDir},
	}...)
	lines := make([]string, 0, len(pairs))
	for _, kv := range pairs {
		if strings.ContainsFunc(kv[1], func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return nil, fmt.Errorf("the value of %s holds a control character", kv[0])
		}
		lines = append(lines, kv[0]+"="+kv[1])
	}
	return lines, nil
}

// show prints showLines of the config.
func show(cfg leadcanary.Config, out io.Writer) error {
	lines, err := showLines(cfg)
	if err != nil {
		return err
	}
	for _, l := range lines {
		fmt.Fprintln(out, l)
	}
	return nil
}

// checkInputs checks the sandbox inputs file against the config with
// provision.CheckInputs and prints one line when they agree. A refusal names
// the field and never a value of the file.
func checkInputs(cfg leadcanary.Config, file string, out io.Writer) error {
	if err := provision.CheckInputs(cfg, file); err != nil {
		return err
	}
	fmt.Fprintln(out, "checked sandbox inputs: they match the config")
	return nil
}
