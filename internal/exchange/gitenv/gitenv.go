package gitenv

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Git names the git executable that owner-side code runs.
type Git struct {
	// Path is the absolute path of the git executable.
	Path string
}

// Cmd describes one git command.
type Cmd struct {
	// GitDir is the absolute path of the repository, passed as --git-dir.
	// It may be empty only for the subcommands that need none ("init",
	// "version").
	GitDir string
	// Args is the git subcommand and its arguments. Before the first "--"
	// or "--end-of-options", an option must be written "--name=value" or be
	// a flag that takes no value (see valuelessFlags in gitenv.go);
	// [Git.Argv] refuses any other. The options that name a program are
	// refused in every form.
	Args []string
	// Protocols lists the transports this command may use. The only name
	// known is "file". Empty means no transport.
	Protocols []string
	// Dir is the working folder the caller will give the process. It is
	// optional. When it is an absolute path, repository discovery cannot
	// leave it by walking to a parent folder. Commands with a GitDir do not
	// discover anything, so this matters for "init" and "version" only.
	Dir string
	// UploadPack, for "fetch" only, is the program git starts in place of
	// git-upload-pack, as an argument vector. It is the only way to name
	// one: "--upload-pack" in Args stays refused.
	//
	// [Git.Argv] refuses it for any other subcommand, refuses it unless
	// Protocols is exactly ["file"], requires UploadPack[0] to be an
	// absolute path, and refuses an empty element and a NUL or newline in
	// any element. It requires Args to hold "--end-of-options" (or "--")
	// with only options before it (values joined by "=", as in
	// "--depth=1"), and the first argument after it, the source, to be an
	// absolute local path with no "::" and no "://". Argv quotes every element as one
	// POSIX shell word (single quotes, a quote written as '\'') and puts
	// "--upload-pack=<the words joined by spaces>" right after "fetch".
	//
	// What git does with it: it runs the value through /bin/sh and appends
	// the shell-quoted source path as one more word, so the program gets
	// the elements as its first arguments and the source as its last. The
	// -c settings and GIT_CONFIG_PARAMETERS do not reach the program, as
	// git clears them for a local transport. The program gets this
	// package's environment (plus GIT_EXEC_PATH, GIT_PROTOCOL and the git
	// exec path at the front of PATH). A sandbox wrapper must therefore
	// clear the environment itself and carry its own settings, such as
	// fixed -c hardening for git-upload-pack. The checks of the fetched
	// objects (fsck) run on the receiving side, here, in the owner's git.
	UploadPack []string
}

// fixedPaths are the places Find looks for git. $PATH is never read.
var fixedPaths = []string{"/usr/bin/git", "/bin/git", "/usr/local/bin/git"}

// safePath is the PATH of every command. git finds its helpers by its own
// compiled-in exec path; this only serves what a helper starts.
const safePath = "/usr/bin:/bin"

// homeDir is the HOME of every command. /dev/null is a character device, so
// nothing can be found under it (".gitconfig", ".config/git", ".git-credentials")
// and nothing can be created there.
const homeDir = "/dev/null"

// defaultCeilings stops discovery at the top of the usual roots when the
// caller gives no working folder. A ceiling cannot name a folder the builder
// does not know, so this is weak; the real protection is that every command
// but "init" and "version" passes --git-dir.
const defaultCeilings = "/home:/root:/tmp:/var:/dev/shm:/run:/mnt:/srv:/opt"

// protocolNames are the transports a command may allow.
var protocolNames = []string{"file"}

// subcommands are the git subcommands this feature uses.
var subcommands = []string{
	"init", "version", "fetch", "for-each-ref", "rev-parse", "rev-list",
	"merge-base", "diff-tree", "diff", "log", "cat-file", "update-ref",
	"symbolic-ref", "show-ref", "count-objects", "config",
	"check-ref-format", "ls-tree",
}

// noGitDir are the subcommands that may run without --git-dir.
var noGitDir = []string{"init", "version"}

// refusedOptions name a program to run, or switch on a program set in the
// configuration. git accepts a unique abbreviation of a long option, so a
// prefix of one of these, at least "--" and three letters, is refused too.
var refusedOptions = []string{"--upload-pack", "--receive-pack", "--exec", "--ext-diff"}

// New returns a Git for an absolute path that names an executable regular
// file (a symbolic link to one is accepted).
func New(path string) (Git, error) {
	if path == "" || strings.ContainsRune(path, 0) {
		return Git{}, errors.New("gitenv: git path is empty or holds a NUL")
	}
	if !filepath.IsAbs(path) {
		return Git{}, fmt.Errorf("gitenv: git path %q is not absolute", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return Git{}, fmt.Errorf("gitenv: git path: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Git{}, fmt.Errorf("gitenv: git path %q is not a regular file", path)
	}
	if info.Mode().Perm()&0o111 == 0 {
		return Git{}, fmt.Errorf("gitenv: git path %q is not executable", path)
	}
	return Git{Path: path}, nil
}

// Find tries a fixed list of absolute paths and returns the first that
// [New] accepts. It never reads $PATH.
func Find() (Git, error) {
	var errs []error
	for _, p := range fixedPaths {
		g, err := New(p)
		if err == nil {
			return g, nil
		}
		errs = append(errs, err)
	}
	return Git{}, fmt.Errorf("gitenv: no git in %s: %w", strings.Join(fixedPaths, ", "), errors.Join(errs...))
}

// IsObjectID reports whether s is a full object id: 40 (SHA-1) or 64
// (SHA-256) lower-case hexadecimal digits.
func IsObjectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Argv returns the command line for c: the git path, the fixed -c settings,
// --git-dir=..., then the arguments. It starts nothing. It refuses what the
// package comment lists.
func (g Git) Argv(c Cmd) ([]string, error) {
	if !filepath.IsAbs(g.Path) {
		return nil, errors.New("gitenv: Git has no absolute path; use New or Find")
	}
	if len(c.Args) == 0 {
		return nil, errors.New("gitenv: no git subcommand")
	}
	for i, a := range c.Args {
		if strings.ContainsRune(a, 0) {
			return nil, fmt.Errorf("gitenv: argument %d holds a NUL", i)
		}
	}
	sub := c.Args[0]
	if strings.HasPrefix(sub, "-") {
		return nil, fmt.Errorf("gitenv: %q comes before the subcommand; options there are the builder's alone", sub)
	}
	if !slices.Contains(subcommands, sub) {
		return nil, fmt.Errorf("gitenv: subcommand %q is not allowed", sub)
	}
	if c.GitDir == "" {
		if !slices.Contains(noGitDir, sub) {
			return nil, fmt.Errorf("gitenv: %q needs a GitDir", sub)
		}
	} else {
		if strings.ContainsRune(c.GitDir, 0) {
			return nil, errors.New("gitenv: GitDir holds a NUL")
		}
		if !filepath.IsAbs(c.GitDir) {
			return nil, fmt.Errorf("gitenv: GitDir %q is not absolute", c.GitDir)
		}
	}
	if c.Dir != "" {
		if strings.ContainsRune(c.Dir, 0) || !filepath.IsAbs(c.Dir) {
			return nil, fmt.Errorf("gitenv: Dir %q is not an absolute path", c.Dir)
		}
	}
	protocols, err := cleanProtocols(c.Protocols)
	if err != nil {
		return nil, err
	}
	if err := checkOptions(c.Args[1:]); err != nil {
		return nil, err
	}
	uploadPack, err := uploadPackOption(c, sub, protocols)
	if err != nil {
		return nil, err
	}

	argv := []string{g.Path}
	for _, kv := range settings(protocols) {
		argv = append(argv, "-c", kv)
	}
	if c.GitDir != "" {
		argv = append(argv, "--git-dir="+c.GitDir)
	}
	argv = append(argv, sub)
	if uploadPack != "" {
		argv = append(argv, uploadPack)
	}
	if sub == "diff" {
		// "git diff" runs diff.external and textconv programs by default,
		// and an empty or dummy diff.external does not stop it: git
		// reports "cannot run" and fails. The two options do.
		argv = append(argv, "--no-ext-diff", "--no-textconv")
	}
	return append(argv, c.Args[1:]...), nil
}

// Env returns the whole environment for c, sorted. Nothing is inherited.
// An unknown protocol allows no transport; [Git.Argv] reports it as an error.
func (g Git) Env(c Cmd) []string {
	allow := ""
	if p, err := cleanProtocols(c.Protocols); err == nil {
		allow = strings.Join(p, ":")
	}
	ceiling := defaultCeilings
	if c.Dir != "" && !strings.ContainsRune(c.Dir, 0) && filepath.IsAbs(c.Dir) {
		// Git does not look at a ceiling folder or above, so the parent
		// of the working folder makes the working folder the last stop.
		if parent := filepath.Dir(filepath.Clean(c.Dir)); parent != "/" {
			ceiling = parent
		}
	}
	return []string{
		// An empty list allows no transport; git treats a set but empty
		// GIT_ALLOW_PROTOCOL as "none allowed" (the tests check it).
		"GIT_ALLOW_PROTOCOL=" + allow,
		// Empty askpass programs run nothing; with GIT_TERMINAL_PROMPT=0
		// a credential request fails instead of asking.
		"GIT_ASKPASS=",
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_CEILING_DIRECTORIES=" + ceiling,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_EDITOR=:",
		"GIT_NO_LAZY_FETCH=1",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_PAGER=cat",
		"GIT_PROTOCOL_FROM_USER=0",
		"GIT_TERMINAL_PROMPT=0",
		"HOME=" + homeDir,
		"LC_ALL=C",
		"PATH=" + safePath,
		"SSH_ASKPASS=",
	}
}

// uploadPackOption checks c.UploadPack and returns the option for it, or ""
// when it is unset.
func uploadPackOption(c Cmd, sub string, protocols []string) (string, error) {
	if len(c.UploadPack) == 0 {
		return "", nil
	}
	if sub != "fetch" {
		return "", fmt.Errorf("gitenv: UploadPack is only for fetch, not %q", sub)
	}
	if !slices.Equal(protocols, []string{"file"}) {
		return "", errors.New(`gitenv: UploadPack needs Protocols to be exactly ["file"]`)
	}
	words := make([]string, len(c.UploadPack))
	for i, w := range c.UploadPack {
		switch {
		case w == "":
			return "", fmt.Errorf("gitenv: UploadPack element %d is empty", i)
		case strings.ContainsAny(w, "\x00\n"):
			return "", fmt.Errorf("gitenv: UploadPack element %d holds a NUL or a newline", i)
		}
		words[i] = shellQuote(w)
	}
	if !filepath.IsAbs(c.UploadPack[0]) {
		return "", fmt.Errorf("gitenv: UploadPack program %q is not an absolute path", c.UploadPack[0])
	}
	// Git appends the source to the command, so the source must be a local
	// path and must be named after the end of the options.
	i := slices.IndexFunc(c.Args[1:], func(a string) bool { return a == "--" || a == "--end-of-options" })
	if i < 0 || i+2 >= len(c.Args) {
		return "", errors.New(`gitenv: UploadPack needs "--end-of-options" and then the source in Args`)
	}
	// Before the end of the options only options may stand, so that no
	// other argument can be the repository git runs the program for. An
	// option takes its value after "=" ("--depth=1", not "--depth 1").
	for _, a := range c.Args[1 : i+1] {
		if !strings.HasPrefix(a, "-") || a == "-" {
			return "", fmt.Errorf("gitenv: UploadPack allows only options before the end of options, not %q", a)
		}
	}
	src := c.Args[i+2]
	if !filepath.IsAbs(src) || strings.Contains(src, "::") || strings.Contains(src, "://") {
		return "", fmt.Errorf("gitenv: UploadPack needs the source to be an absolute local path, not %q", src)
	}
	return "--upload-pack=" + strings.Join(words, " "), nil
}

// shellQuote returns s as one POSIX shell word: in single quotes, with each
// single quote written as: close the quotes, a backslash and a quote, open
// the quotes again (like git's sq_quote).
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// cleanProtocols checks the names and returns them sorted, without repeats.
func cleanProtocols(in []string) ([]string, error) {
	var out []string
	for _, p := range in {
		if !slices.Contains(protocolNames, p) {
			return nil, fmt.Errorf("gitenv: unknown protocol %q", p)
		}
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out, nil
}

// valuelessFlags are the options that may stand before the end of the
// options without "=". Each one takes no value, in every subcommand that
// accepts it here, so git never reads the next argument as its value. This
// is the one list to extend: add a flag only when it takes no value in any
// subcommand of [subcommands], and write every other option as
// "--name=value". The flags come from the callers of this package (the
// mirror, the intake check) and from its own tests.
var valuelessFlags = []string{
	// Fetch and update.
	"--no-tags", "--prune", "--no-recurse-submodules", "--no-write-fetch-head",
	"--no-auto-maintenance", "--no-auto-gc", "--all",
	// Init.
	"--bare",
	// Output control.
	"--quiet", "-q", "-v", "-z", "--raw", "--count",
	// Diff and log.
	"-r", "-m", "--root", "--no-renames", "--full-index", "--no-color",
	"--no-ext-diff", "--no-textconv", "--no-mailmap", "--no-notes",
	"--no-decorate", "--no-show-signature", "--first-parent", "--no-walk",
	"--objects",
	// Reading and checking.
	"--verify", "--is-ancestor", "--is-bare-repository", "--no-deref",
	"--stdin", "--list", "--local", "--batch-check", "--batch",
	// Added for the callers and tests of this package. The "config" action
	// flags name the key as a plain argument, not as their value.
	"--get", "--get-all", "--unset", "--show-origin", "--oneline", "--stat",
	"--name-only", "--exit-code", "--update-head-ok", "-e", "--show-signature",
}

// separateValueFlags take exactly the next argument as their value and are
// allowed in that form, because a caller needs it ("config --file /dev/stdin
// --list"). The value must be a plain argument: if it starts with "-" the
// command is refused, so the value can never be a terminator or an option.
// Prefer "--name=value" in new code; add a flag here only when a caller
// cannot.
var separateValueFlags = []string{"--file"}

// checkOptions refuses what must not stand before the end of the options:
// an option that names a program to run (also abbreviated), and any option
// that git could let take the next argument as its value. It stops at the
// first "--" or "--end-of-options", after which git reads no options.
//
// The rule is strict so that no argument depends on what git does with a
// neighbour. An option such as "-o" takes the next argument as its value,
// even when that is "--", and git then reads the argument after it as an
// option. So before the end of the options an option is either written with
// "=" (which holds its value), or is in [valuelessFlags].
func checkOptions(args []string) error {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" || a == "--end-of-options" {
			return nil
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			continue
		}
		if slices.Contains(separateValueFlags, a) {
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++ // the value; git reads it as the value of a
				continue
			}
			return fmt.Errorf("gitenv: option %q needs its value as the next argument, and the value may not start with \"-\"", a)
		}
		name, _, hasValue := strings.Cut(a, "=")
		if strings.HasPrefix(a, "--") && len(name) >= 5 {
			for _, r := range refusedOptions {
				if strings.HasPrefix(r, name) {
					return fmt.Errorf("gitenv: option %q is refused", a)
				}
			}
		}
		if hasValue || slices.Contains(valuelessFlags, a) {
			continue
		}
		return fmt.Errorf("gitenv: option %q before the end of the options is not allowed: write its value with \"=\" (\"--name=value\"), or ask for the flag to be added to the list of valueless flags", a)
	}
	return nil
}

// settings returns the fixed -c settings as key=value strings.
//
// Each key was checked against "git help config" of git 2.56. Changes from
// the first list, and why:
//   - diff.external is not set. An empty or dummy value does not disable the
//     external diff: "git diff" then fails with "cannot run". [Git.Argv]
//     adds --no-ext-diff and --no-textconv to "diff" instead.
//   - core.alternateRefsCommand is set to the empty string. The tests show
//     that git then runs no command and that an alternate object store still
//     works; without it a command from the repository's config runs when a
//     fetch lists the refs of an alternate.
//   - core.protectNTFS and core.protectHFS are added, so the fsck checks of
//     ".git" look-alikes do not depend on the platform.
//   - uploadpack.packObjectsHook is not set. It is read from protected
//     configuration only (the command line, user and system files), never
//     from a repository's config, and an empty value would make
//     git-upload-pack run an empty command. The tests show a hook in the
//     source repository's config is not run.
//   - The settings that name a program (core.sshCommand, gpg.program,
//     gpg.x509.program, gpg.ssh.program) take "false", which exits
//     non-zero, so a use fails instead of running something. core.askPass,
//     core.pager and core.editor take values that start no program.
//
// "url.<base>.insteadOf" and similar come only from configuration files, and
// none is read apart from the repository's own.
func settings(protocols []string) []string {
	s := []string{
		"core.hooksPath=/dev/null",
		"core.fsmonitor=false",
		"core.untrackedCache=false",
		"core.pager=cat",
		"core.attributesFile=/dev/null",
		"core.excludesFile=/dev/null",
		"core.sshCommand=false",
		"core.askPass=",
		"core.editor=:",
		"core.alternateRefsCommand=",
		"core.protectNTFS=true",
		"core.protectHFS=true",
		"credential.helper=",
		"protocol.allow=never",
	}
	for _, p := range protocols {
		s = append(s, "protocol."+p+".allow=always")
	}
	return append(s,
		"protocol.version=2",
		"transfer.fsckObjects=true",
		"fetch.fsckObjects=true",
		"receive.fsckObjects=true",
		"fetch.recurseSubmodules=false",
		"submodule.recurse=false",
		"fetch.writeCommitGraph=false",
		"gc.auto=0",
		"maintenance.auto=false",
		"safe.bareRepository=explicit",
		"advice.detachedHead=false",
		"log.showSignature=false",
		// A bare repository reads HEAD:.mailmap by default, which is lead
		// content, and %aN, %aE and the default log format would print it.
		"log.mailmap=false",
		"mailmap.blob=",
		"mailmap.file=",
		"gpg.program=false",
		// The signature's own format picks the program, not gpg.format.
		"gpg.x509.program=false",
		"gpg.ssh.program=false",
	)
}
