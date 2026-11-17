//go:build linux

package mountpoints

import "bytes"

// The two sections of a git configuration file that make git read another
// file as configuration: include.path and includeIf.<condition>.path. A
// relative path of either is resolved next to the file that holds it, so a
// copy of .git/config in Ataniq's folder would still name files a lead can
// write in its own tree.
const (
	sectionInclude   = "include"
	sectionIncludeIf = "includeif"
)

// findInclude scans the text of a git configuration file for a section header
// named include or includeIf (any case, with or without a subsection: [include],
// [includeIf "gitdir:..."], [include.path]) and returns the line of the first
// one and the canonical spelling of its name. It interprets nothing else: no
// key, no value, no other section. It reads the text once, front to back, and
// is as long as the text; the caller bounds the text before it calls.
//
// It follows only as much of git's syntax as it needs to tell a header from
// what is not one: a comment (# or ;) runs to the end of the line; a key line
// is a name, then possibly "=" and a value that runs to the end of the line
// unless a backslash before the newline continues it, a double quote hides a
// comment character, and a "[" in a value is part of the value; a header is
// "[", the name, then a subsection or the rest, up to "]". More than one
// header on a line is read as git reads it, and a UTF-8 byte order mark at the
// start of the text is skipped, as git skips it. A line git would refuse as a bad
// line is scanned the same way and its "[include]" is still found, which
// over-refuses and never the reverse.
func findInclude(data []byte) (line int, name string) {
	line = 1
	// git skips a UTF-8 byte order mark at the start of the file.
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	n := len(data)
	for i := 0; i < n; {
		switch c := data[i]; {
		case c == '\n':
			line++
			i++
		case c == ' ' || c == '\t' || c == '\r':
			i++
		case c == '#' || c == ';':
			i = skipLine(data, i)
		case c == '[':
			i++
			for i < n && (data[i] == ' ' || data[i] == '\t') {
				i++
			}
			start := i
			for i < n && !isHeaderNameEnd(data[i]) {
				i++
			}
			switch word := string(bytes.ToLower(data[start:i])); word {
			case sectionInclude:
				return line, "include"
			case sectionIncludeIf:
				return line, "includeIf"
			}
			i = skipHeader(data, i)
		default:
			// A key: its name, then an optional value after "=".
			for i < n && data[i] != '=' && data[i] != '\n' {
				i++
			}
			if i < n && data[i] == '=' {
				i, line = skipValue(data, i+1, line)
			}
		}
	}
	return 0, ""
}

// isHeaderNameEnd says a byte ends the name of a section: white space, the
// closing bracket, the quote that opens a subsection, the dot of the old
// subsection form, or the end of the line.
func isHeaderNameEnd(c byte) bool {
	switch c {
	case ' ', '\t', '\r', '\n', ']', '"', '.':
		return true
	}
	return false
}

// skipLine returns the index of the newline that ends the line at i, or the
// end of the data. The newline itself is left for the caller to count.
func skipLine(data []byte, i int) int {
	if j := bytes.IndexByte(data[i:], '\n'); j >= 0 {
		return i + j
	}
	return len(data)
}

// skipHeader moves past the rest of a section header: a quoted subsection (in
// which a backslash escapes the next byte) and the closing bracket. It stops
// at a newline, which ends a header that was never closed.
func skipHeader(data []byte, i int) int {
	n := len(data)
	for i < n && data[i] != ']' && data[i] != '\n' {
		if data[i] == '"' {
			i++
			for i < n && data[i] != '"' && data[i] != '\n' {
				if data[i] == '\\' && i+1 < n && data[i+1] != '\n' {
					i++
				}
				i++
			}
			if i < n && data[i] == '"' {
				i++
			}
			continue
		}
		i++
	}
	if i < n && data[i] == ']' {
		i++
	}
	return i
}

// skipValue moves past the value that starts at i. It runs to the end of the
// line, except that a backslash before the newline (or before a carriage
// return and a newline) continues it on the next line, a double quote opens a
// span in which "#" and ";" do not start a comment, and a "#" or ";" outside
// one starts a comment that ends the value. It returns the index of the
// newline that ends it (left for the caller to count) and the line number
// after the lines it continued over.
func skipValue(data []byte, i, line int) (int, int) {
	n := len(data)
	quoted := false
	for i < n {
		switch c := data[i]; {
		case c == '\n':
			return i, line
		case c == '\\' && i+1 < n && data[i+1] == '\n':
			line++
			i += 2
		case c == '\\' && i+2 < n && data[i+1] == '\r' && data[i+2] == '\n':
			line++
			i += 3
		case c == '\\' && i+1 < n:
			i += 2
		case c == '"':
			quoted = !quoted
			i++
		case !quoted && (c == '#' || c == ';'):
			return skipLine(data, i), line
		default:
			i++
		}
	}
	return n, line
}
