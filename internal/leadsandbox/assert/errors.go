package assert

import "fmt"

// Refusal is the error every format check of this package returns: a spec or
// a report line that is refused, or a report that cannot be built. Field names
// the input and Reason says why. A refusal never carries more than a clipped
// quotation of the offending input.
type Refusal struct {
	Field  string
	Reason string
}

// Error implements error.
func (e *Refusal) Error() string {
	return "assert: " + e.Field + ": " + e.Reason
}

func refuse(field, format string, args ...any) error {
	return &Refusal{Field: field, Reason: fmt.Sprintf(format, args...)}
}
