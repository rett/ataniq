//go:build linux && amd64

package qualification

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func typedInventoryRows(a *Attempt) []fdMetadata {
	fds := descriptorStdio(a)
	if a.Config.Role != "candidate" {
		for _, e := range a.FD1Runtime.Endpoints {
			fds = append(fds, fdMetadata{e.FD, e.Identity.Device, e.Identity.Inode, e.Mode})
		}
	}
	return fds
}

func typedInventoryObservation(a *Attempt, fds []fdMetadata) Observation {
	o := descriptorInventory(fds)
	o.Version = 1
	o.RunID = a.Config.RunID
	o.RequestID = a.Config.RequestID
	o.Case = CaseInventory
	o.Status = "observed"
	return o
}

func TestFD1TypedInventoryRejections(t *testing.T) {
	for _, role := range []string{"candidate", "direct", "weakened-synthetic"} {
		x := fakeTypedLaunch(t, role, "inventory")
		i, err := readInjection(filepath.Join(x.c.Path, x.a.ID, "injection.json"))
		if err != nil {
			t.Fatal(err)
		}
		a := x.a
		a.FD1Runtime = i.FD1Runtime
		good := typedInventoryObservation(a, typedInventoryRows(a))
		if err := validateDescriptorObservations(a, good); err != nil {
			t.Fatal(err)
		}
		for _, kind := range []string{"alias", "reused-slot", "wrong-type", "wrong-mode", "missing-capability", "duplicate", "missing-row", "missing-stdio", "extra-row", "bpf", "bpf-reused", "incomplete", "binding-absent"} {
			fds := typedInventoryRows(a)
			ep := a.FD1Runtime.Endpoints[1]
			switch kind {
			case "alias":
				fds = append(fds, fdMetadata{999, ep.Identity.Device, ep.Identity.Inode, ep.Mode})
			case "reused-slot":
				if role != "candidate" {
					fds = fds[:len(fds)-3]
					fds = append(fds, fdMetadata{FD1PipeFD, 99, 999, syscall.S_IFREG})
				} else {
					fds = append(fds, fdMetadata{FD1PipeFD, 99, 999, syscall.S_IFREG})
				}
			case "wrong-type", "wrong-mode":
				row := fdMetadata{ep.FD, ep.Identity.Device, ep.Identity.Inode, syscall.S_IFREG | 0600}
				if kind == "wrong-mode" {
					row.Mode = ep.Mode | 0001
				}
				if role == "candidate" {
					fds = append(fds, row)
				} else {
					fds[4] = row
				}
			case "missing-capability":
				if role == "candidate" {
					fds = append(fds, fdMetadata{ep.FD, 99, 999, syscall.S_IFIFO})
				} else {
					fds = fds[:len(fds)-1]
				}
			case "duplicate":
				fds = append(fds, fds[0])
			case "missing-stdio":
				fds = fds[1:]
			case "bpf":
				id := a.Snapshots["bpf"].Identity
				fds = append(fds, fdMetadata{BPFFD, id.Device, id.Inode, syscall.S_IFREG})
			case "bpf-reused":
				fds = append(fds, fdMetadata{BPFFD, 99, 999, syscall.S_IFREG})
			}
			o := typedInventoryObservation(a, fds)
			switch kind {
			case "missing-row":
				o.Facts = o.Facts[:len(o.Facts)-1]
			case "extra-row":
				o.Facts = append(o.Facts, known("fds.127", "fd=999 dev=99 ino=999 mode=100600"))
			case "incomplete":
				o.Facts = append(o.Facts, Fact{Name: "fds", State: "unknown"})
			case "binding-absent":
				a.FD1Runtime = nil
			}
			if validateDescriptorObservations(a, o) == nil {
				t.Fatal("typed inventory admitted", role, kind)
			}
			a.FD1Runtime = i.FD1Runtime
		}
	}
}

func TestFD1IncompleteOperationsKeepFactsButRejectProof(t *testing.T) {
	for _, caseName := range []string{"fake-fd", "fake-dir-fd", "fake-pipe-fd", "fake-socket-fd"} {
		for _, role := range []string{"direct", "weakened-synthetic", "candidate"} {
			x := fakeTypedLaunch(t, role, caseName)
			a := x.a
			i, _ := readInjection(filepath.Join(x.c.Path, a.ID, "injection.json"))
			a.FD1Runtime = i.FD1Runtime
			data, _ := os.ReadFile(filepath.Join(x.c.Path, a.ID, "output"))
			good, err := DecodeObservation(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			if err := validateDescriptorObservations(a, good); err != nil {
				t.Fatal(err)
			}
			for _, stage := range []string{"fd-identity", "binding-read", "channel-read", "channel-frame", "postread-flags", "pread"} {
				o := good
				o.Facts = append(append([]Fact{}, good.Facts...), known("stage", stage))
				o.Status = "inconclusive"
				o.Reason = "incomplete fixture"
				n := int(syscall.EAGAIN)
				o.Errno = &n
				before, _ := json.Marshal(o)
				if validateDescriptorObservations(a, o) == nil {
					t.Fatal("incomplete proof admitted", caseName, role, stage)
				}
				after, _ := json.Marshal(o)
				if !bytes.Equal(before, after) {
					t.Fatal("intermediate evidence erased")
				}
			}
			if role == "candidate" {
				for _, n := range []int{int(syscall.EAGAIN), int(syscall.EACCES)} {
					o := good
					o.Errno = &n
					if validateDescriptorObservations(a, o) == nil {
						t.Fatal("unrelated denial proved closure")
					}
				}
			} else {
				for j := range good.Facts {
					o := good
					o.Facts = append([]Fact{}, good.Facts...)
					o.Facts[j] = known(o.Facts[j].Name, "false")
					name := o.Facts[j].Name
					required := name == "identity_match" || name == "digest_match" && (caseName == "fake-fd" || caseName == "fake-dir-fd") || (name == "opened" || name == "read_success") && caseName == "fake-dir-fd" || (name == "read_success" || name == "frame_match" || name == "eof") && (caseName == "fake-pipe-fd" || caseName == "fake-socket-fd")
					if required && validateDescriptorObservations(a, o) == nil {
						t.Fatal("missing recovery fact admitted", caseName, name)
					}
				}
			}
		}
	}
}

func TestFD1FailedObservationSurvivesCleanupAndRestart(t *testing.T) {
	for _, caseName := range []string{"inventory", "fake-pipe-fd"} {
		x := fakeTypedLaunch(t, "candidate", caseName)
		data, _ := os.ReadFile(filepath.Join(x.c.Path, x.a.ID, "output"))
		o, err := DecodeObservation(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		if caseName == "inventory" {
			i, _ := readInjection(filepath.Join(x.c.Path, x.a.ID, "injection.json"))
			ep := i.FD1Runtime.Endpoints[1]
			fds := append(descriptorStdio(x.a), fdMetadata{999, ep.Identity.Device, ep.Identity.Inode, ep.Mode})
			o.Facts = descriptorInventory(fds).Facts
		} else {
			o.Status = "inconclusive"
			o.Reason = "reused slot"
			o.Errno = nil
			o.Facts = []Fact{known("identity_match", "false"), known("stage", "fd-identity")}
		}
		var buf bytes.Buffer
		if err := EncodeObservation(&buf, o); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(x.c.Path, x.a.ID, "output"), buf.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		if err := x.c.run(x.a, x.h); err == nil {
			t.Fatal("failed observation admitted")
		}
		if x.a.Phase != "complete" || !x.a.Empty || !sameJSON(x.a.Observation, &o) {
			t.Fatal("failed evidence not retained")
		}
		x.c.Close()
		c, err := OpenCampaign(x.c.Path)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		if c.Blocked || c.Records[x.a.ID] == nil || !sameJSON(c.Records[x.a.ID].Observation, &o) {
			t.Fatal("failed evidence lost on restart")
		}
	}
}
