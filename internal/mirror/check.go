package mirror

import (
	"strings"
)

// checkConfig reads the output of "git config --list -z" and returns the
// reason why the configuration is not one that [Create] writes, or "" when it
// is fine. The listing has one "key\nvalue" entry per setting, ended by NUL.
// git lower-cases the section and the variable name and keeps the subsection,
// so an entry written as "[Core] HooksPath" arrives as "core.hookspath".
//
// The reasons name only a section and a variable from the fixed list, never a
// subsection or a value, because a hand-edited file may hold any text there.
// The list is the one in the package comment: settings that name a program to
// run, read another file, rewrite a URL or open a remote. Other sections are
// not looked at.
func checkConfig(listing []byte) string {
	bare := false
	sawBare := false
	for _, entry := range strings.Split(string(listing), "\x00") {
		if entry == "" {
			continue
		}
		key, value, hasValue := strings.Cut(entry, "\n")
		section, rest, _ := strings.Cut(key, ".")
		name := rest
		sub := ""
		if i := strings.LastIndexByte(rest, '.'); i >= 0 {
			sub, name = rest[:i], rest[i+1:]
		}
		switch section {
		case "include", "includeif":
			return "the config has an include"
		case "url":
			return "the config has a url rewrite"
		case "filter":
			return "the config has a filter"
		case "remote":
			return "the config has a remote"
		case "alias":
			return "the config has an alias"
		case "core":
			switch name {
			case "hookspath", "fsmonitor", "sshcommand", "alternaterefscommand", "worktree":
				if sub == "" {
					return "the config sets core." + name
				}
			case "bare":
				if sub == "" {
					sawBare, bare = true, !hasValue || isTrue(value)
				}
			}
		case "uploadpack":
			if name == "packobjectshook" && sub == "" {
				return "the config sets uploadpack.packObjectsHook"
			}
		case "diff":
			if sub == "" && name == "external" {
				return "the config sets diff.external"
			}
			if sub != "" && (name == "command" || name == "textconv") {
				return "the config sets a diff command or textconv"
			}
		}
	}
	if !sawBare || !bare {
		return "the repository is not bare"
	}
	return ""
}

// isTrue reports whether a git boolean value means true. The other values
// git knows (false, no, off, 0 and the empty string) count as false, and so
// does an unknown value.
func isTrue(v string) bool {
	switch strings.ToLower(v) {
	case "true", "yes", "on", "1":
		return true
	}
	return false
}
