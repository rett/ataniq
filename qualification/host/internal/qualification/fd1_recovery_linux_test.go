//go:build linux && amd64

package qualification

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func reopenFD1Fixture(t *testing.T, x *hostFixture) {
	t.Helper()
	x.c.Close()
	c, err := OpenCampaign(x.c.Path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	x.c = c
	x.a = c.Records[x.a.ID]
	if x.a == nil {
		t.Fatal("original authority lost on restart")
	}
}

func bindTypedFixture(t *testing.T, x *hostFixture) {
	t.Helper()
	a := x.a
	i, err := readInjection(filepath.Join(x.c.Path, a.ID, "injection.json"))
	if err != nil || validateInjection(a, i, x.service) != nil || authenticateFD1Runtime(a, i.FD1Runtime) != nil {
		t.Fatal("invalid fixture injection", err)
	}
	a.Parent, a.Workload, a.Injector = x.parent, x.service, &i
	a.FD1Runtime, a.Target, a.PopulatedLive = i.FD1Runtime, &i.Process, true
	if err := x.c.Save(a, "bound", ""); err != nil {
		t.Fatal(err)
	}
}

// Capture both immutable bytes and per-attempt runtime identities, never those
// from another attempt. All host effects below use deterministic transcripts.
func unchangedFD1Recovery(t *testing.T, x *hostFixture, r *FD1Runtime) func() {
	t.Helper()
	requestPath := filepath.Join(x.c.Path, x.a.ID, "request.json")
	request, err := os.ReadFile(requestPath)
	if err != nil {
		t.Fatal(err)
	}
	plan := x.a.PlanSHA256
	runtime, _ := json.Marshal(r)
	return func() {
		t.Helper()
		after, err := os.ReadFile(requestPath)
		if err != nil || !bytes.Equal(request, after) || plan != x.a.PlanSHA256 || plan != planDigest(x.a) || !sameJSON(x.a.Request, hostRequest(x.a)) {
			t.Fatal("cleanup changed immutable plan/request", err)
		}
		bound, _ := json.Marshal(x.a.FD1Runtime)
		if !bytes.Equal(runtime, bound) || !sameJSON(x.a.FD1Runtime, x.a.Injector.FD1Runtime) {
			t.Fatal("cleanup changed original runtime identities")
		}
		if _, err := os.Stat(filepath.Join(x.c.Path, x.a.ID, "release.json")); !os.IsNotExist(err) {
			t.Fatal("cleanup published release", err)
		}
		if x.a.Observation != nil || x.a.DescriptorReceipt != nil || x.a.DescriptorObservation != nil {
			t.Fatal("cleanup manufactured qualification evidence")
		}
		for _, call := range x.calls {
			if strings.Contains(call, " start ") || strings.Contains(call, "--service-type") || strings.Contains(call, " stop ") && !strings.HasSuffix(call, "-- "+x.a.Service) && !strings.HasSuffix(call, "-- "+x.a.Slice) {
				t.Fatal("replacement launch or unrelated stop", call)
			}
		}
	}
}

func TestFD1LostAckReconcileReopensWithOriginalBinding(t *testing.T) {
	for _, interruption := range []string{"none", "before-service-stop", "after-service-stop"} {
		t.Run(interruption, func(t *testing.T) {
			x := fakeTypedLaunch(t, "direct", "inventory")
			x.ackError = true
			if x.c.run(x.a, x.h) == nil || x.a.Phase != "launch-unknown" {
				t.Fatal("lost start acknowledgement not retained")
			}
			i, err := readInjection(filepath.Join(x.c.Path, x.a.ID, "injection.json"))
			if err != nil {
				t.Fatal(err)
			}
			check := unchangedFD1Recovery(t, x, i.FD1Runtime)
			x.calls = nil // Only cleanup effects after the original, uncertain start.
			reopenFD1Fixture(t, x)
			if !x.c.Blocked || x.a.Injector != nil || x.a.FD1Runtime != nil {
				t.Fatal("uncertain start was adopted before reconciliation")
			}
			if interruption != "none" {
				call := x.h.call
				cut := errors.New("controller interrupted")
				x.h.call = func(path string, args []string) (string, error) {
					if strings.Contains(strings.Join(args, " "), " stop -- "+x.a.Service) {
						if interruption == "after-service-stop" {
							if _, err := call(path, args); err != nil {
								t.Fatal(err)
							}
						}
						panic(cut)
					}
					return call(path, args)
				}
				func() {
					defer func() {
						if recover() != cut {
							t.Fatal("did not interrupt cancellation at durable boundary")
						}
					}()
					_ = x.c.reconcile(x.a, x.h)
				}()
				x.h.call = call
				reopenFD1Fixture(t, x)
				if x.a.Phase != "cancelling" || !x.c.Blocked {
					t.Fatal("cancellation not restartable")
				}
				check()
			}
			if err := x.c.reconcile(x.a, x.h); err != nil {
				t.Fatal(err)
			}
			check()
			reopenFD1Fixture(t, x)
			check()
			if x.a.Phase != "complete" || !x.a.Empty || !x.a.TargetExited || !x.a.SliceStopped || x.c.Blocked {
				t.Fatal("exact original cleanup not durably complete")
			}
			for _, unit := range []string{x.a.Service, x.a.Slice} {
				stops := 0
				for _, call := range x.calls {
					if strings.Contains(call, " stop -- "+unit) {
						stops++
					}
				}
				if stops != 1 {
					t.Fatal("original unit not stopped exactly once", unit, stops)
				}
			}
		})
	}
}

func TestFD1CleanupAdoptionRequiresAuthenticatedArtifact(t *testing.T) {
	for _, bad := range []string{"missing", "changed", "flags", "peer-close", "invocation"} {
		t.Run(bad, func(t *testing.T) {
			x := fakeTypedLaunch(t, "direct", "inventory")
			x.ackError = true
			if x.c.run(x.a, x.h) == nil {
				t.Fatal("lost acknowledgement accepted")
			}
			path := filepath.Join(x.c.Path, x.a.ID, "injection.json")
			i, err := readInjection(path)
			if err != nil {
				t.Fatal(err)
			}
			switch bad {
			case "missing":
				err = os.Remove(i.FD1Runtime.Artifact.Path)
			case "changed":
				err = rewriteFrozenFixture(i.FD1Runtime.Artifact.Path, []byte("changed"))
			case "flags":
				i.FD1Runtime.Endpoints[1].Flags = 0
			case "peer-close":
				i.FD1Runtime.Bindings.Pipe.PeerClosed = pointer(false)
			case "invocation":
				i.Invocation = strings.Repeat("c", 32)
			}
			if err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(i)
			if err := rewriteFrozenFixture(path, b); err != nil {
				t.Fatal(err)
			}
			reopenFD1Fixture(t, x)
			x.calls = nil
			if x.c.reconcile(x.a, x.h) == nil || !x.c.Blocked || x.a.Injector != nil || x.a.FD1Runtime != nil {
				t.Fatal("adopted unauthenticated binding")
			}
			for _, call := range x.calls {
				if strings.Contains(call, " stop ") || strings.Contains(call, " start ") || strings.Contains(call, "--service-type") {
					t.Fatal("unproved authority caused effect", call)
				}
			}
			reopenFD1Fixture(t, x)
			if !x.c.Blocked {
				t.Fatal("failed adoption lost fence")
			}
		})
	}
}

func TestFD1ArtifactIntegrityFailureKeepsCleanupAuthority(t *testing.T) {
	for _, bad := range []string{"removed", "changed", "replaced"} {
		t.Run(bad, func(t *testing.T) {
			x := fakeTypedLaunch(t, "direct", "fake-pipe-fd")
			bindTypedFixture(t, x)
			r := x.a.FD1Runtime
			check := unchangedFD1Recovery(t, x, r)
			original, err := os.ReadFile(r.Artifact.Path)
			if err != nil {
				t.Fatal(err)
			}
			switch bad {
			case "removed":
				err = os.Remove(r.Artifact.Path)
			case "changed":
				err = rewriteFrozenFixture(r.Artifact.Path, []byte("changed"))
			case "replaced":
				err = os.Rename(r.Artifact.Path, r.Artifact.Path+".original")
				if err == nil {
					err = exclusiveFile(r.Artifact.Path, original, 0400)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if bad == "changed" {
				if x.c.Save(x.a, "released", "") == nil || x.a.Phase != "bound" || !x.c.Blocked || x.c.fenced {
					t.Fatal("changed artifact released or poisoned original cleanup authority")
				}
			}
			reopenFD1Fixture(t, x)
			if !x.c.Blocked || x.a.FD1IntegrityFailure != fd1IntegrityFailure {
				t.Fatal("integrity failure not visible/fenced")
			}
			if err := x.c.reconcile(x.a, x.h); err != nil {
				t.Fatal("original stop authority unavailable", err)
			}
			check()
			if x.a.Phase != "complete" || !x.c.Blocked {
				t.Fatal("cleanup erased integrity fence")
			}
			// Even restoring exact original bytes/inode after cleanup cannot silently
			// turn the failed attempt into successful qualification or clear its fence.
			if bad == "changed" {
				if err := rewriteFrozenFixture(r.Artifact.Path, original); err != nil {
					t.Fatal(err)
				}
			}
			reopenFD1Fixture(t, x)
			check()
			if x.a.FD1IntegrityFailure != fd1IntegrityFailure || !x.c.Blocked || authenticateFD1Runtime(x.a, r) == nil || x.c.collect(x.a) == nil {
				t.Fatal("integrity failure recovered into accepted evidence")
			}
			if _, err := prepareDescriptorExec(x.a); err == nil {
				t.Fatal("integrity failure admitted by final wrapper")
			}
			candidate := *x.a
			candidate.Config.Role = "candidate"
			candidate.Config.DirectControl = x.a.ID
			if validateControl(&candidate, x.a, x.a.Created) == nil {
				t.Fatal("integrity failure accepted as control")
			}
			if _, err := x.c.Prepare(x.a.Config); err == nil {
				t.Fatal("new attempt accepted after integrity failure")
			}
			x.c.Close()
			var out bytes.Buffer
			if err := InspectHost(x.c.Path, x.a.ID, &out); err != nil || !bytes.Contains(out.Bytes(), []byte(`"fd1_integrity_failure":"`+fd1IntegrityFailure+`"`)) {
				t.Fatal("failed original record not inspectable", err, out.String())
			}
		})
	}
}

func TestFD1CleanupSaveRejectsInconsistentRuntime(t *testing.T) {
	for _, phase := range []string{"launch-unknown", "cleanup-unknown", "cancelling", "empty", "complete"} {
		for _, bad := range []string{"attempt-missing", "injector-missing", "both-missing", "mismatch", "provenance", "integrity-marker"} {
			t.Run(phase+"/"+bad, func(t *testing.T) {
				x := fakeTypedLaunch(t, "direct", "inventory")
				bindTypedFixture(t, x)
				path := filepath.Join(x.c.Path, x.a.ID, "intent.json")
				before, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				switch bad {
				case "attempt-missing":
					x.a.FD1Runtime = nil
				case "injector-missing":
					x.a.Injector = nil
				case "both-missing":
					x.a.Injector, x.a.FD1Runtime = nil, nil
				case "mismatch":
					r := *x.a.FD1Runtime
					r.Artifact.SHA256 = strings.Repeat("0", 64)
					x.a.FD1Runtime = &r
				case "provenance":
					x.a.Injector.Invocation = strings.Repeat("c", 32)
				case "integrity-marker":
					x.a.FD1IntegrityFailure = "unrecognized"
				}
				if x.c.Save(x.a, phase, "") == nil || !x.c.Blocked {
					t.Fatal("inconsistent cleanup record saved")
				}
				after, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatal("inconsistent save replaced original authority", err)
				}
			})
		}
	}
}

func TestFD1CompletedArtifactFailurePersistsFence(t *testing.T) {
	x := fakeTypedLaunch(t, "direct", "fake-pipe-fd")
	if err := x.c.run(x.a, x.h); err != nil {
		t.Fatal(err)
	}
	candidate := *x.a
	candidate.Config.Role = "candidate"
	candidate.Config.DirectControl = x.a.ID
	if err := validateControl(&candidate, x.a, x.a.Created); err != nil {
		t.Fatal("successful control fixture invalid", err)
	}
	r := x.a.FD1Runtime
	original, err := os.ReadFile(r.Artifact.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := rewriteFrozenFixture(r.Artifact.Path, []byte("changed")); err != nil {
		t.Fatal(err)
	}
	reopenFD1Fixture(t, x)
	if !x.c.Blocked || validateControl(&candidate, x.a, x.a.Created) == nil {
		t.Fatal("previous success overrode current integrity failure")
	}
	x.calls = nil
	if err := x.c.reconcile(x.a, x.h); err != nil || len(x.calls) != 0 {
		t.Fatal("completed cleanup caused new host effects", err, x.calls)
	}
	if err := rewriteFrozenFixture(r.Artifact.Path, original); err != nil {
		t.Fatal(err)
	}
	reopenFD1Fixture(t, x)
	if !x.c.Blocked || x.a.FD1IntegrityFailure != fd1IntegrityFailure || validateControl(&candidate, x.a, x.a.Created) == nil || !sameJSON(x.a.FD1Runtime, r) {
		t.Fatal("artifact restoration silently cleared the recorded integrity failure")
	}
}

func TestLegacyRecordRejectsTypedIntegrityFailure(t *testing.T) {
	x := testHost(t)
	x.a.FD1IntegrityFailure = fd1IntegrityFailure
	if validateStored(x.a) == nil || x.c.Save(x.a, "cleanup-unknown", "") == nil {
		t.Fatal("historical profile accepted typed integrity evidence")
	}
}

func TestFD1CollectionSaveCannotHideArtifactFailure(t *testing.T) {
	for _, changeAt := range []int{1, 2} {
		t.Run(string(rune('0'+changeAt)), func(t *testing.T) {
			x := fakeTypedLaunch(t, "direct", "fake-pipe-fd")
			old := x.c.syncDir
			saves := 0
			x.c.syncDir = func(f *os.File) error {
				if x.a.Observation != nil {
					saves++
					if saves == changeAt {
						if err := rewriteFrozenFixture(x.a.FD1Runtime.Artifact.Path, []byte("changed during evidence save")); err != nil {
							return err
						}
					}
				}
				return old(f)
			}
			if err := x.c.run(x.a, x.h); err == nil {
				t.Fatal("artifact failure during collection returned success")
			}
			if x.a.Phase != "complete" || x.a.FD1IntegrityFailure != fd1IntegrityFailure || !x.c.Blocked || x.a.Observation == nil {
				t.Fatal("failed collection lost cleanup, observation or integrity fence")
			}
			reopenFD1Fixture(t, x)
			if !x.c.Blocked || x.a.FD1IntegrityFailure != fd1IntegrityFailure || x.a.Observation == nil {
				t.Fatal("failed collection not durably inspectable/fenced")
			}
		})
	}
}
