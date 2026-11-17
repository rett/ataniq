package leadmirror

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"ataniq/internal/exchange/boundedexec"
	"ataniq/internal/exchange/gitenv"
	"ataniq/internal/exchange/leadtext"
	"ataniq/internal/exchange/sandboxplan"
	"ataniq/internal/mirror"
)

// Defaults of the options that a caller leaves at zero.
const (
	// DefaultSmallMaxSource is the MaxSource of the oversize case: smaller
	// than any real object store, so the measure is over it.
	DefaultSmallMaxSource int64 = 512
	// DefaultSmallMaxStaged is the MaxStaged of the oversize case: smaller
	// than the content of the fixture branch "big", which is about 300 KiB
	// that does not compress.
	DefaultSmallMaxStaged int64 = 64 << 10
	// DefaultFetchTimeout is the time limit of one fetch.
	DefaultFetchTimeout = 5 * time.Minute
	// DefaultVerifyTimeout is the time limit of one verification run.
	DefaultVerifyTimeout = 5 * time.Minute
)

// maxEntries is the entry limit of the harness's own measures of the mirror.
const maxEntries = 1_000_000

// Options are the inputs of [Run]. Every field but the three limits and the
// timeouts is required.
type Options struct {
	// StateRoot is the scratch state root. It must not exist or must be an
	// empty folder; see [Config.StateRoot] for the rest.
	StateRoot string
	// Role and Project name the scratch lead and project.
	Role, Project string
	// Planner is the only way the cases reach a sandbox. The command passes
	// the template; the tests pass a fake.
	Planner sandboxplan.Planner
	// Git is the git executable, from gitenv.Find or gitenv.New.
	Git gitenv.Git
	// TemplateDigest is recorded in the report. The command passes the hex
	// SHA-256 of the template file.
	TemplateDigest string
	// SmallMaxSource and SmallMaxStaged are the limits of the oversize case.
	// Zero means the default.
	SmallMaxSource, SmallMaxStaged int64
	// FetchTimeout and VerifyTimeout bound one fetch and one verification
	// run. Zero means the default.
	FetchTimeout, VerifyTimeout time.Duration
}

func (o Options) withDefaults() Options {
	if o.SmallMaxSource == 0 {
		o.SmallMaxSource = DefaultSmallMaxSource
	}
	if o.SmallMaxStaged == 0 {
		o.SmallMaxStaged = DefaultSmallMaxStaged
	}
	if o.FetchTimeout == 0 {
		o.FetchTimeout = DefaultFetchTimeout
	}
	if o.VerifyTimeout == 0 {
		o.VerifyTimeout = DefaultVerifyTimeout
	}
	return o
}

func (o Options) validate() error {
	if err := checkStateRootText(o.StateRoot); err != nil {
		return err
	}
	if !sandboxplan.ValidName(o.Role) {
		return fmt.Errorf("%w: role is not a valid name", ErrConfig)
	}
	if !sandboxplan.ValidName(o.Project) {
		return fmt.Errorf("%w: project is not a valid name", ErrConfig)
	}
	if o.Planner == nil {
		return fmt.Errorf("%w: no planner", ErrNoTemplate)
	}
	if o.Git.Path == "" {
		return fmt.Errorf("%w: no git", ErrConfig)
	}
	if o.TemplateDigest == "" {
		return fmt.Errorf("%w: no template digest", ErrNoTemplate)
	}
	if o.SmallMaxSource < 0 || o.SmallMaxStaged < 0 || o.FetchTimeout < 0 || o.VerifyTimeout < 0 {
		return fmt.Errorf("%w: a limit is negative", ErrConfig)
	}
	return nil
}

// recorder wraps the planner. It is the only object the cases hand to the
// mirror and verify packages, and it counts the requests, so that a case can
// say how many requests a call made.
type recorder struct {
	inner    sandboxplan.Planner
	serves   atomic.Int64
	verifies atomic.Int64
}

func (r *recorder) Serve(q sandboxplan.ServeRequest) (sandboxplan.ServePlan, error) {
	r.serves.Add(1)
	return r.inner.Serve(q)
}

func (r *recorder) Verify(q sandboxplan.VerifyRequest) (sandboxplan.VerifyPlan, error) {
	r.verifies.Add(1)
	return r.inner.Verify(q)
}

// runner holds what every case shares.
type runner struct {
	opts Options
	rec  *recorder
	fx   fixtures
	rep  *Report
}

// Run prepares the scratch state root, runs the cases and returns the report.
//
// A refusal before any case runs (a state root that holds anything, a name
// that is not valid, no planner) is an error and nothing is created. After
// that, what the cases observe goes into the report, whatever it is: a fetch
// that fails is a fact. A problem of the harness itself (a fixture that
// cannot be made) is recorded as the fact "<case>.harness-error" and the next
// case runs. Run returns an error for the context only, with the report made
// so far.
func Run(ctx context.Context, opts Options) (*Report, error) {
	opts = opts.withDefaults()
	if err := opts.validate(); err != nil {
		return nil, err
	}
	if err := prepareStateRoot(opts.StateRoot); err != nil {
		return nil, err
	}
	r := &runner{
		opts: opts,
		rec:  &recorder{inner: opts.Planner},
		fx:   fixtures{git: opts.Git},
		rep: &Report{
			Format:   ReportFormat,
			Template: TemplateInfo{SHA256: opts.TemplateDigest},
			Role:     opts.Role,
			Project:  opts.Project,
		},
	}
	tools, err := r.tools(ctx)
	if err != nil {
		return r.rep, err
	}
	r.rep.Tools = tools

	// The case S16 lives in the state root itself; the others in a folder
	// each below <root>/cases.
	steps := []func(context.Context) error{
		func(ctx context.Context) error { return r.caseFetch(ctx) },
		func(ctx context.Context) error { return r.caseOversize(ctx) },
		func(ctx context.Context) error { return r.casePlanted(ctx) },
		func(ctx context.Context) error { return r.caseReviewAndVerify(ctx) },
	}
	for _, step := range steps {
		if err := step(ctx); err != nil {
			return r.rep, err
		}
	}
	return r.rep, nil
}

// tools observes the tool versions.
func (r *runner) tools(ctx context.Context) (Tools, error) {
	c := gitenv.Cmd{Args: []string{"version"}}
	argv, err := r.opts.Git.Argv(c)
	if err != nil {
		return Tools{}, err
	}
	res, err := boundedexec.Run(ctx, boundedexec.Cmd{
		Argv: argv, Env: r.opts.Git.Env(c), Timeout: time.Minute, MaxStdout: 4 << 10, MaxStderr: 4 << 10,
	})
	if err != nil {
		return Tools{}, fmt.Errorf("leadmirror: git version: %w", err)
	}
	return Tools{
		Git:    leadtext.Escape(strings.TrimSpace(string(res.Stdout))),
		Go:     runtime.Version(),
		GOOS:   runtime.GOOS,
		GOARCH: runtime.GOARCH,
	}, nil
}

// prepareStateRoot makes the scratch state root, or accepts an existing empty
// folder. It refuses a symlink, anything that is not a folder, a folder that
// group or others can write, and a folder that holds anything.
func prepareStateRoot(root string) error {
	fi, err := os.Lstat(root)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := os.Mkdir(root, 0o700); err != nil {
			return fmt.Errorf("%w: %w", ErrStateRoot, err)
		}
		return os.Chmod(root, 0o700)
	case err != nil:
		return fmt.Errorf("%w: %w", ErrStateRoot, err)
	case !fi.IsDir():
		return fmt.Errorf("%w: the path is a symlink or not a folder", ErrStateRoot)
	case fi.Mode().Perm()&0o022 != 0:
		return fmt.Errorf("%w: group or others can write the folder", ErrStateRoot)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrStateRoot, err)
	}
	if len(entries) != 0 {
		return fmt.Errorf("%w: the folder holds something (%d entries)", ErrStateRoot, len(entries))
	}
	return nil
}

// caseEnv is one case's state root, with a lead clone and a mirror.
type caseEnv struct {
	root     string // the state root of the case
	canary   string // the canary folder, beside the clone's parents
	upstream string // the fixture upstream repository
	tree     string // the lead's clone
	inside   string // the clone as the serving half sees it, or the host path when unknown
	insideOK bool   // the planner gave the inside path
	m        *mirror.Mirror
}

// newCase makes the state root of one case: the canary folder, the upstream
// fixture, the lead's clone (made by lead, then moved to leads/<role>/tree)
// and a mirror seeded with the upstream branch and tag. root is made if it
// does not exist. When plant is true the clone is made hostile at the end.
func (r *runner) newCase(ctx context.Context, root string, lead func(ctx context.Context, dir string) error, plant bool) (*caseEnv, error) {
	role, project := r.opts.Role, r.opts.Project
	c := &caseEnv{root: root, canary: filepath.Join(root, "canary"), upstream: filepath.Join(root, "fixture", "upstream")}
	if root != r.opts.StateRoot {
		if err := mkdirPrivate(filepath.Dir(root)); err != nil {
			return nil, err
		}
	}
	for _, dir := range []string{
		root, c.canary, filepath.Join(root, "fixture"),
		filepath.Join(root, "leads"), filepath.Join(root, "leads", role),
		filepath.Join(root, "mirrors"), filepath.Join(root, "work"),
	} {
		if err := mkdirPrivate(dir); err != nil {
			return nil, err
		}
	}
	if err := r.fx.buildUpstream(ctx, c.upstream); err != nil {
		return nil, err
	}
	built := filepath.Join(root, "fixture", "lead")
	if err := r.fx.buildLead(ctx, c.upstream, built); err != nil {
		return nil, err
	}
	if lead != nil {
		if err := lead(ctx, built); err != nil {
			return nil, err
		}
	}
	tree, err := mirror.LeadTree(root, role)
	if err != nil {
		return nil, err
	}
	c.tree = tree
	if err := os.Rename(built, tree); err != nil {
		return nil, err
	}
	// Ask the planner once where the clone is seen from inside. The answer
	// is only used to write core.hooksPath; an error is not a fact here,
	// the fetch reports it.
	c.inside = tree
	if p, err := r.rec.Serve(sandboxplan.ServeRequest{Role: role, Project: project, Tree: tree}); err == nil && plainPath(p.Repository) {
		c.inside, c.insideOK = p.Repository, true
	}
	if plant {
		if err := plantHostile(tree, c.canary, c.inside); err != nil {
			return nil, err
		}
	}
	mpath, err := mirror.Path(root, project)
	if err != nil {
		return nil, err
	}
	m, err := mirror.Create(ctx, r.opts.Git, mpath)
	if err != nil {
		return nil, err
	}
	c.m = m
	if err := r.seedMirror(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

// mkdirPrivate makes a folder with mode 0700, or accepts one that exists.
func mkdirPrivate(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		return nil
	}
	return os.Chmod(dir, 0o700)
}

// seedMirror puts the upstream branch and tag of the fixture into the mirror.
// It is the owner's own push: git fetch through gitenv, into the mirror,
// from the harness's upstream fixture. The sandbox plays no part.
func (r *runner) seedMirror(ctx context.Context, c *caseEnv) error {
	g := r.opts.Git
	cmd := gitenv.Cmd{
		GitDir:    c.m.Path(),
		Protocols: []string{"file"},
		Args: []string{
			"fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "--end-of-options", c.upstream,
			"+refs/heads/" + upstreamBranch + ":refs/heads/" + upstreamBranch,
			"+refs/tags/" + upstreamTag + ":refs/tags/" + upstreamTag,
		},
	}
	argv, err := g.Argv(cmd)
	if err != nil {
		return err
	}
	res, err := boundedexec.Run(ctx, boundedexec.Cmd{
		Argv: argv, Env: g.Env(cmd), Timeout: fixtureTimeout, MaxStdout: 1 << 16, MaxStderr: 1 << 16,
	})
	if err != nil {
		return fmt.Errorf("leadmirror: seed the mirror: %w: %s", err, firstLine(res.Stderr))
	}
	return nil
}

func firstLine(b []byte) string {
	b = bytes.TrimSpace(b)
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

// add appends a case to the report and returns it.
func (r *runner) add(id, title string) *Case {
	c := &Case{ID: id, Title: title}
	r.rep.Cases = append(r.rep.Cases, c)
	return c
}

// fact appends a fact to the case.
func (c *Case) fact(id, detail string, observed map[string]any) {
	if observed == nil {
		observed = map[string]any{}
	}
	c.Facts = append(c.Facts, Fact{ID: c.ID + "." + id, Detail: detail, Observed: observed})
}

// harnessError records a problem of the harness itself as a fact, and returns
// the context's error when that is what ended the case.
func (c *Case) harnessError(ctx context.Context, err error) error {
	c.fact("harness-error", "The harness could not make or run something of its own. The facts of this case, if any, are incomplete.",
		map[string]any{"error": leadtext.Escape(err.Error())})
	return ctx.Err()
}

// merge returns a with the entries of b added.
func merge(a, b map[string]any) map[string]any {
	for k, v := range b {
		a[k] = v
	}
	return a
}
