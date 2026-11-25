package agentsession

import (
	"errors"
	"fmt"
)

var ErrInput = errors.New("agentsession: invalid input")

type InputError struct {
	Member string
	Reason InputReason
}

func (e *InputError) Error() string {
	return ErrInput.Error() + ": " + e.Member + ": " + string(e.Reason)
}
func (e *InputError) Unwrap() error              { return ErrInput }
func invalid(path string, why InputReason) error { return &InputError{path, why} }
func enum[T interface {
	~string
	Valid() bool
}](path string, v T) error {
	if v.Valid() {
		return nil
	}
	if v == "" {
		return invalid(path, "empty-value")
	}
	return invalid(path, "unknown-value")
}
func index(path string, i int) string { return fmt.Sprintf("%s[%d]", path, i) }
