//go:build linux && ataniq_ownerchannel

package hostadapter

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"ataniq/internal/exchange/boundedexec"
	"ataniq/internal/exchange/gitenv"
)

// smallOutput is the output cap of a git run that prints a few lines;
// listOutput is the cap of a listing of refs.
const (
	smallOutput = 4 << 10
	listOutput  = 1 << 20
	maxStderr   = 4 << 10
)

// gitCall runs read-only git commands on one bare repository through gitenv
// and the bounded runner, so every run has the pinned environment and a time
// limit.
type gitCall struct {
	git     gitenv.Git
	dir     string
	timeout time.Duration
}

// run runs one git command and returns its standard output. A non-zero exit
// is an error that names the subcommand and the status, and nothing git
// wrote.
func (g gitCall) run(ctx context.Context, args ...string) ([]byte, error) {
	return g.runCap(ctx, smallOutput, args...)
}

func (g gitCall) runCap(ctx context.Context, max int64, args ...string) ([]byte, error) {
	cmd := gitenv.Cmd{GitDir: g.dir, Args: args}
	argv, err := g.git.Argv(cmd)
	if err != nil {
		return nil, fmt.Errorf("hostadapter: %w", err)
	}
	res, err := boundedexec.Run(ctx, boundedexec.Cmd{
		Argv: argv, Env: g.git.Env(cmd), Timeout: g.timeout, MaxStdout: max, MaxStderr: maxStderr,
	})
	if err != nil {
		var exit *boundedexec.ExitError
		if errors.As(err, &exit) {
			return nil, fmt.Errorf("hostadapter: git %s failed with status %d", args[0], exit.Exit)
		}
		return nil, fmt.Errorf("hostadapter: git %s: %w", args[0], err)
	}
	return res.Stdout, nil
}

// refInfo is one ref as for-each-ref lists it.
type refInfo struct {
	id, typ string
}

// refs reads the refs with exactly these names, in one run. A pattern of
// for-each-ref matches a name and what lies below it, so only the line whose
// name is one asked for counts. A symbolic ref is refused. A name that does
// not exist is not in the answer.
func (g gitCall) refs(ctx context.Context, names ...string) (map[string]refInfo, error) {
	args := append([]string{"for-each-ref", "--format=%(refname) %(objectname) %(objecttype) %(symref)", "--end-of-options"}, names...)
	out, err := g.runCap(ctx, listOutput, args...)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	got := map[string]refInfo{}
	for _, line := range strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, " ")
		if len(f) != 4 || !gitenv.IsObjectID(f[1]) {
			return nil, errors.New("hostadapter: unexpected output of git for-each-ref")
		}
		if !want[f[0]] {
			continue
		}
		if f[3] != "" {
			return nil, errors.New("hostadapter: a symbolic ref is refused")
		}
		if _, dup := got[f[0]]; dup {
			return nil, errors.New("hostadapter: unexpected output of git for-each-ref")
		}
		got[f[0]] = refInfo{id: f[1], typ: f[2]}
	}
	return got, nil
}

// pinsAt returns the names of the pins under prefix that hold commit, sorted.
func (g gitCall) pinsAt(ctx context.Context, prefix, commit string) ([]string, error) {
	out, err := g.runCap(ctx, listOutput, "for-each-ref", "--format=%(refname) %(objectname)", "--end-of-options", prefix)
	if err != nil {
		return nil, err
	}
	var pins []string
	for _, line := range strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		name, id, ok := strings.Cut(line, " ")
		if !ok || !gitenv.IsObjectID(id) {
			return nil, errors.New("hostadapter: unexpected output of git for-each-ref")
		}
		if id == commit && strings.HasPrefix(name, prefix) {
			pins = append(pins, name)
		}
	}
	sort.Strings(pins)
	return pins, nil
}
