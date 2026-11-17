package command

// maxNameLen is the longest identifier, kind or project name.
const maxNameLen = 64

// ValidName reports whether s is a command id, a message kind or a project
// name: one to 64 bytes of lower-case letters and digits, plus dot,
// underscore and hyphen, and the first byte is a letter or digit. This is
// the grammar [a-z0-9][a-z0-9._-]{0,63}. Every name that reaches a header
// goes through it, so a header cannot be forged through a name.
func ValidName(s string) bool {
	if len(s) == 0 || len(s) > maxNameLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == '-':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}
