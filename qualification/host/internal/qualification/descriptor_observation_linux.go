//go:build linux && amd64

package qualification

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"syscall"
)

// Identity matches are separate from descriptor numbers. A probe/runtime may
// reuse a number; absence at 512 alone cannot establish capability exclusion.
type DescriptorObservation struct {
	Complete    bool   `json:"complete"`
	FakeMatches []int  `json:"fake_matches"`
	BPFMatches  []int  `json:"bpf_matches"`
	Fake512     string `json:"fake512"`
	BPF513      string `json:"bpf513"`
}

func observeDescriptorIdentities(a *Attempt, o Observation) DescriptorObservation {
	r := DescriptorObservation{FakeMatches: []int{}, BPFMatches: []int{}, Fake512: "absent", BPF513: "absent"}
	// Require one exact declared table, with no unavailable, duplicate or
	// undeclared FD facts. Other inventory families have independent evidence.
	facts := map[string]string{}
	for _, f := range o.Facts {
		if f.Name != "fds" && !strings.HasPrefix(f.Name, "fds.") {
			continue
		}
		if _, duplicate := facts[f.Name]; f.Name == "fds" || duplicate || f.State != "known" || f.Value == nil {
			return r
		}
		facts[f.Name] = *f.Value
	}
	n, err := strconv.Atoi(facts["fds.count"])
	if err != nil || n < 3 || n > MaxEntries || facts["fds.count"] != strconv.Itoa(n) || len(facts) != n+1 {
		return r
	}
	seen := map[int]bool{}
	for j := 0; j < n; j++ {
		var d fdMetadata
		value := facts[fmt.Sprintf("fds.%d", j)]
		count, err := fmt.Sscanf(value, "fd=%d dev=%d ino=%d mode=%o", &d.Number, &d.Device, &d.Inode, &d.Mode)
		if err != nil || count != 4 || d.Number < 0 || seen[d.Number] || value != fmt.Sprintf("fd=%d dev=%d ino=%d mode=%o", d.Number, d.Device, d.Inode, d.Mode) {
			return r
		}
		seen[d.Number] = true
		if d.Number <= 2 {
			name := [3]string{"request", "output", "stderr"}[d.Number]
			id, exists := a.Streams[name]
			if !exists || d.Device != id.Device || d.Inode != id.Inode || d.Mode&syscall.S_IFMT != syscall.S_IFREG {
				return r
			}
		}
		fake := d.Device == a.Fixture.Identity.Device && d.Inode == a.Fixture.Identity.Inode
		bpf := d.Device == a.Snapshots["bpf"].Identity.Device && d.Inode == a.Snapshots["bpf"].Identity.Inode
		if fake {
			r.FakeMatches = append(r.FakeMatches, d.Number)
		}
		if bpf {
			r.BPFMatches = append(r.BPFMatches, d.Number)
		}
		if d.Number == FakeFD {
			r.Fake512 = "reused"
			if fake {
				r.Fake512 = "synthetic-identity"
			}
		}
		if d.Number == BPFFD {
			r.BPF513 = "reused"
			if bpf {
				r.BPF513 = "bpf-identity"
			}
		}
	}
	// Positive anchors are mandatory: an empty or partial table cannot
	// establish absence of the injected capabilities.
	if !seen[0] || !seen[1] || !seen[2] {
		return r
	}
	r.Complete = true
	return r
}

func validateDescriptorObservations(a *Attempt, o Observation) error {
	switch a.Config.Case {
	case "inventory":
		r := observeDescriptorIdentities(a, o)
		if !r.Complete {
			return errors.New("post-exec descriptor inventory incomplete")
		}
		// Bwrap must consume BPF513, independently of fake capability closure.
		// A reused 513 is retained as such and cannot count as verified closure.
		if len(r.BPFMatches) != 0 || r.BPF513 != "absent" {
			return errors.New("post-exec BPF consumption not observed")
		}
		switch a.Config.Role {
		case "candidate":
			if len(r.FakeMatches) != 0 || r.Fake512 != "absent" {
				return errors.New("post-exec synthetic exclusion not observed")
			}
		case "direct":
			if len(r.FakeMatches) != 1 || r.FakeMatches[0] != FakeFD {
				return errors.New("direct synthetic identity not observed exclusively at 512")
			}
		case "weakened-synthetic":
			if len(r.FakeMatches) != len(fakeFDs(a)) {
				return errors.New("weakened synthetic aliases not observed")
			}
			for _, fd := range fakeFDs(a) {
				found := false
				for _, actual := range r.FakeMatches {
					if fd == actual {
						found = true
					}
				}
				if !found {
					return errors.New("weakened synthetic alias missing")
				}
			}
		}
	case "fake-fd":
		if a.Config.Role == "candidate" {
			if o.Status != "observed" || factValue(o, "stage") != "fstat" || o.Errno == nil || *o.Errno != int(syscall.EBADF) {
				return errors.New("post-exec FD512 closure not observed")
			}
		} else if o.Status != "observed" || factValue(o, "digest_match") != "true" {
			return errors.New("synthetic control recovery not observed")
		}
	}
	return nil
}
