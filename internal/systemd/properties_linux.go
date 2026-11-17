package systemd

import (
	"errors"
	"strconv"
	"strings"
)

// ParseProperties parses the output of "systemctl show --property=<required>"
// into a map. required is the comma-separated list that was asked for: every
// key must be one of them, appear once and carry "=", and every required key
// must be present. Output longer than max bytes is refused. Values are kept as
// printed, and an empty value is a value. The parse proves the shape of the
// output, not that the unit is the one the caller meant.
func ParseProperties(s, required string, max int) (map[string]string, error) {
	if len(s) > max {
		return nil, errors.New("show output excessive")
	}
	p := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(s, "\n"), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			return nil, errors.New("malformed show output")
		}
		if _, ok = p[key]; ok {
			return nil, errors.New("duplicate show property")
		}
		if !strings.Contains(","+required+",", ","+key+",") {
			return nil, errors.New("unexpected show property")
		}
		p[key] = value
	}
	for _, key := range strings.Split(required, ",") {
		if _, ok := p[key]; !ok {
			return nil, errors.New("missing show property")
		}
	}
	return p, nil
}

// NoJob reports whether the parsed properties carry a Job property that is
// empty or "0", meaning no job is queued for the unit. A set without a Job
// property is not "no job": the answer is false.
func NoJob(p map[string]string) bool { v, ok := p["Job"]; return ok && (v == "" || v == "0") }

// Reasons ParseJobReply fails.
var (
	// ErrJobReply means the reply is not a single object path of a job.
	ErrJobReply = errors.New("job reply not recognised")
	// ErrJobNumber means the object path does not end in a decimal job number.
	ErrJobNumber = errors.New("job number invalid")
)

const jobPrefix = `"/org/freedesktop/systemd1/job/`

// ParseJobReply reads the job number out of the reply to a StartTransientUnit
// call as "busctl call" prints it: the type letter "o" and a quoted job object
// path. The number says only that the manager accepted the request. It does
// not say that the unit started, finished or belongs to the caller.
func ParseJobReply(reply string) (uint64, error) {
	f := strings.Fields(reply)
	if len(f) != 2 || f[0] != "o" || !strings.HasPrefix(f[1], jobPrefix) || !strings.HasSuffix(f[1], `"`) {
		return 0, ErrJobReply
	}
	n, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimPrefix(f[1], jobPrefix), `"`), 10, 64)
	if err != nil {
		return 0, ErrJobNumber
	}
	return n, nil
}
