package profile

import "fmt"

// Refusal is the error every check of this package returns. Field names the
// input, table row or value that is refused; Reason says why. A refusal never
// carries the value of an environment variable the profile did not define.
type Refusal struct {
	Field  string
	Reason string
}

// Error implements error.
func (e *Refusal) Error() string {
	return "profile: " + e.Field + ": " + e.Reason
}

func refuse(field, format string, args ...any) error {
	return &Refusal{Field: field, Reason: fmt.Sprintf(format, args...)}
}
