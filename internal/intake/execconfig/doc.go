// Package execconfig is the set of repository paths that could make code run
// as the owner when a tree is next used without a sandbox, and the matcher
// that tests a path against that set.
//
// A lead's branch is untrusted. Before its work enters the owner's upstream
// branch, the intake lists every changed path that this package matches, and
// the owner signs each one off separately. The set covers agent settings and
// hooks, MCP servers, direnv, editor tasks, build entry points, git hooks,
// attributes and submodules, CI workflows and toolchain selection. Every rule
// carries a short reason that the owner reads, and a Source that says where
// the rule comes from: the design, the feature scope, or a close relative
// added here. Design, Scope and Relatives return those three lists.
//
// # Pattern syntax
//
// Built-in rules and project additions use one syntax. A pattern is
// split at "/" into elements.
//
//   - "name" matches any path that has an element "name", at any depth,
//     the last element included. A tracked file can become a folder and the
//     reverse, so both are matched.
//   - "name/" matches the same paths as "name". The matcher sees no file
//     modes, and a symbolic link or a submodule is one path with no
//     trailing "/" that stands for a folder, so a folder rule cannot ask for
//     a folder. ".claude", "scripts" and "x/.devcontainer" match the rules
//     ".claude/", "scripts/" and ".devcontainer/".
//   - A leading "/" anchors the pattern at the repository root.
//   - "*" matches any run of bytes inside one element and "?" matches one
//     character inside one element. Nothing else is special: brackets and
//     backslashes are refused.
//   - A pattern with several elements ("a/b", "a/b/") matches a run of
//     consecutive path elements, with the same rules.
//
// # How a path is matched
//
// Matching works on the path bytes as git stores them, split at "/". The
// path is never cleaned, resolved or converted to UTF-8, and nothing touches
// the file system. Matching is ASCII case-insensitive, because a checkout may
// live on a case-insensitive file system. Only A to Z fold; other bytes
// compare exactly. Each element is also tried without its trailing dots and
// spaces (".claude." and ".mcp.json " match like ".claude" and ".mcp.json"),
// and the element "git~1" also matches as ".git".
//
// Limit: no other folding is done. Other NTFS short names, alternate data
// streams, and Unicode normalization and ignorable code points (HFS+) are
// out of scope. A caller that needs them must reject such paths before
// matching.
//
// Symbolic links: what a link points at is the caller's business, not the
// matcher's. A link named like a rule is matched by its own name, but a
// link with another name ("cfg" pointing at ".claude") is not matched, and
// neither are the paths below it. The intake must flag symbolic link
// entries, and what they alias, itself.
//
// Odd paths are handled conservatively, and never panic:
//
//   - Empty elements (a leading "/", "//", a trailing "/") are skipped.
//   - If a path has a leading "/", an empty element inside it, or a "." or
//     ".." element, anchored rules are applied as if they were not anchored,
//     so the odd form can never hide a path from a rule.
//   - Newlines, NUL and bytes that are not UTF-8 are ordinary bytes of an
//     element. They match only where a pattern matches them.
//   - A path with no element at all (empty, or only slashes) matches
//     nothing. A caller must reject such a path as invalid.
//
// # Order
//
// Match returns the first matching rule, in the order of Rules: the design
// rules, then the feature scope rules, then the relatives, each list in the
// order of its function, then project additions in the order given to With.
//
// The package uses strings only. It reads no file and runs nothing. It does
// not decide what happens to a matched path; the intake does.
package execconfig
