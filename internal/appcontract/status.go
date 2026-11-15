package appcontract

import (
	"math"

	"ataniq/internal/appmodel"
)

// LossRange records one scope's baseline, acknowledgement and effective head.
// Cutoff is present for a terminal enqueue and equals the frozen head.
type LossRange struct {
	Baseline, Acknowledged, Upper uint64
	Cutoff                        *uint64
}

func (r LossRange) valid() bool {
	return r.Baseline <= r.Acknowledged && r.Acknowledged <= r.Upper && r.Upper <= math.MaxInt64 &&
		(r.Cutoff == nil || *r.Cutoff == r.Upper)
}

func optionalNumber(n *uint64) []byte {
	if n == nil {
		return []byte{0}
	}
	return append([]byte{1}, number(*n)...)
}

// EnqueueStatus contains precisely the additional status inputs for an enqueue.
type EnqueueStatus struct {
	Project             string
	Enrollment, Enqueue LossRange
}

// StatusRevision binds the base state and, for an enqueue, both loss ranges.
// Held is a projection of queued state and is not a valid base state.
func (k ProjectionKeys) StatusRevision(i Instance, key appmodel.CommandKey, version uint64, state appmodel.State, enqueue *EnqueueStatus) (string, error) {
	if !validID(i.Authority) || key.Authority != i.Authority || !validID(key.EnrolledClientID) || !validID(string(key.CommandID)) || version == 0 || version > math.MaxInt64 {
		return "", errInvalid
	}
	flag := byte(0)
	if enqueue == nil {
		if state != appmodel.Applied {
			return "", errInvalid
		}
	} else {
		flag = 1
		if !validID(enqueue.Project) || !enqueue.Enrollment.valid() || !enqueue.Enqueue.valid() ||
			(state != appmodel.Queued && state != appmodel.Cancelled) ||
			(state == appmodel.Cancelled) != (enqueue.Enrollment.Cutoff != nil && enqueue.Enqueue.Cutoff != nil) ||
			(enqueue.Enrollment.Cutoff == nil) != (enqueue.Enqueue.Cutoff == nil) {
			return "", errInvalid
		}
	}
	f := append(i.fields(), []byte(key.EnrolledClientID), []byte(key.CommandID), number(version), []byte(state), []byte{flag})
	if enqueue != nil {
		f = append(f, []byte(enqueue.Project))
		for _, scope := range []struct {
			name, project string
			r             LossRange
		}{
			{"enrollment", "", enqueue.Enrollment}, {"enqueue", enqueue.Project, enqueue.Enqueue},
		} {
			f = append(f, []byte(scope.name), []byte(key.EnrolledClientID), []byte(scope.project), number(scope.r.Baseline), number(scope.r.Acknowledged), number(scope.r.Upper), optionalNumber(scope.r.Cutoff))
		}
	}
	return mac(k.status, frame("STATUS", f...)), nil
}
