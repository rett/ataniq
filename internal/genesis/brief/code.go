package brief

import "errors"

// Code is the short, fixed word a refusal is known by. Every refusal code
// of the genesis packages (the brief and the question catalog) is declared
// in this file, and no other package declares one.
type Code string

// The refusal codes of the brief.
const (
	// CodeBriefInvalid answers a brief that breaks the rules of the
	// record or its form.
	CodeBriefInvalid Code = "brief_invalid"
	// CodeTooLarge answers a brief, a field, a note or a list over its cap.
	CodeTooLarge Code = "too_large"
	// CodeSecretShaped answers a value or a note that looks like a key, a
	// token or a password. Credentials are never typed on the phone: they
	// are given at the desk, and the brief names them only by reference.
	CodeSecretShaped Code = "secret_shaped"
	// CodeFieldName answers a field name outside the brief's names.
	CodeFieldName Code = "field_name"
	// CodeValueInvalid answers a value, provenance, node, round or time
	// that is not well formed.
	CodeValueInvalid Code = "value_invalid"
	// CodeDomainMismatch answers a domain extension field that does not
	// belong to the brief's kind, or a change of kind while fields of the
	// old kind are still set.
	CodeDomainMismatch Code = "domain_mismatch"
	// CodeNotInferred answers a confirmation of a field that is not set or
	// was not inferred.
	CodeNotInferred Code = "not_inferred"
)

// The refusal codes of the question catalog and its walker.
const (
	// CodeCatalogInvalid answers a catalog that breaks its rules on load.
	CodeCatalogInvalid Code = "catalog_invalid"
	// CodeCatalogMismatch answers a brief that was started under another
	// version of the catalog than the one it is walked with.
	CodeCatalogMismatch Code = "catalog_mismatch"
	// CodeNodeUnknown answers an answer to a node the catalog does not hold.
	CodeNodeUnknown Code = "node_unknown"
	// CodeNodeNotEligible answers an answer to a node whose condition does
	// not hold for the brief.
	CodeNodeNotEligible Code = "node_not_eligible"
	// CodeAnswerInvalid answers an answer the node does not allow.
	CodeAnswerInvalid Code = "answer_invalid"
	// CodeRoundInvalid answers an answer stamped with a round that is not
	// the current or the next one, or past the last round.
	CodeRoundInvalid Code = "round_invalid"
)

// Error is a refusal: a code, and a detail for the owner's eyes.
type Error struct {
	// Code is the refusal code.
	Code Code
	// Detail says what was wrong. It is written by the package and never
	// holds text from the data: a field is named by its place in the form,
	// never by what the data said.
	Detail string
}

// Error returns the code, followed by the detail when there is one.
func (e *Error) Error() string {
	if e.Detail == "" {
		return string(e.Code)
	}
	return string(e.Code) + ": " + e.Detail
}

// CodeOf returns the code of err when err is, or wraps, an [*Error].
func CodeOf(err error) (Code, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e.Code, true
	}
	return "", false
}

func fail(c Code, detail string) *Error { return &Error{Code: c, Detail: detail} }
