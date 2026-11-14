//go:build linux && amd64

package qualification

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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

func TestFD1FailedCollectionAlwaysPersistsIntegrity(t *testing.T) {
	for _, caseName := range []string{"inventory", "fake-pipe-fd"} {
		for _, changeAt := range []int{0, 1, 2} {
			t.Run(fmt.Sprintf("%s/save%d", caseName, changeAt), func(t *testing.T) {
				x := fakeTypedLaunch(t, "candidate", caseName)
				path := filepath.Join(x.c.Path, x.a.ID)
				i, err := readInjection(filepath.Join(path, "injection.json"))
				if err != nil {
					t.Fatal(err)
				}
				original, err := os.ReadFile(i.FD1Runtime.Artifact.Path)
				if err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(filepath.Join(path, "output"))
				if err != nil {
					t.Fatal(err)
				}
				o, err := DecodeObservation(bytes.NewReader(data))
				if err != nil {
					t.Fatal(err)
				}
				if caseName == "inventory" {
					ep := i.FD1Runtime.Endpoints[1]
					o.Facts = descriptorInventory(append(descriptorStdio(x.a), fdMetadata{999, ep.Identity.Device, ep.Identity.Inode, ep.Mode})).Facts
				} else {
					o.Errno = pointer(int(syscall.EAGAIN))
				}
				var output bytes.Buffer
				if err := EncodeObservation(&output, o); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(path, "output"), output.Bytes(), 0600); err != nil {
					t.Fatal(err)
				}
				copy := *x.a
				copy.FD1Runtime = i.FD1Runtime
				observationErr := validateDescriptorObservations(&copy, o)
				if observationErr == nil {
					t.Fatal("fixture must fail observation validation")
				}
				old := x.c.syncDir
				saves, changed := 0, false
				x.c.syncDir = func(f *os.File) error {
					if x.a.Observation != nil {
						saves++
					}
					// Save(empty -> complete) is finishFast's last boundary,
					// immediately before collection's entry authentication.
					if !changed && (changeAt == 0 && x.a.Phase == "empty" || changeAt > 0 && saves == changeAt) {
						changed = true
						if err := rewriteFrozenFixture(i.FD1Runtime.Artifact.Path, []byte("changed at collection boundary")); err != nil {
							return err
						}
					}
					return old(f)
				}
				err = x.c.run(x.a, x.h)
				if err == nil || !changed || !strings.Contains(err.Error(), fd1IntegrityFailure) {
					t.Fatal("integrity failure accepted or lost", err)
				}
				if changeAt > 0 && (!strings.Contains(err.Error(), observationErr.Error()) || !strings.Contains(x.a.Reason, observationErr.Error())) {
					t.Fatal("original observation error lost", err, x.a.Reason)
				}
				if x.a.Phase != "complete" || !x.a.Empty || !x.a.TargetExited || !x.a.SliceStopped || !x.c.Blocked || x.a.FD1IntegrityFailure != fd1IntegrityFailure {
					t.Fatal("cleanup proof or sticky fence lost")
				}
				if changeAt == 0 && x.a.Observation != nil || changeAt > 0 && !sameJSON(x.a.Observation, &o) {
					t.Fatal("observation adopted or discarded incorrectly")
				}
				if changeAt > 0 && caseName == "inventory" && x.a.DescriptorObservation == nil {
					t.Fatal("raw inventory identities discarded")
				}
				if err := rewriteFrozenFixture(i.FD1Runtime.Artifact.Path, original); err != nil {
					t.Fatal(err)
				}
				reopenFD1Fixture(t, x)
				if !x.c.Blocked || x.a.FD1IntegrityFailure != fd1IntegrityFailure || changeAt > 0 && !sameJSON(x.a.Observation, &o) {
					t.Fatal("restoration erased durable incident/evidence")
				}
				if after, err := os.ReadFile(filepath.Join(path, "output")); err != nil || !bytes.Equal(after, output.Bytes()) {
					t.Fatal("raw failed output changed", err)
				}
				if _, err := x.c.Prepare(x.a.Config); err == nil {
					t.Fatal("new attempt allowed after incident")
				}
				x.c.Close()
				// Completed reconciliation has no host effects.
				r := ReconcileHost(x.c.Path, x.a.ID)
				if !r.NewStartsBlocked || r.Outcome != "inconclusive" || r.ReasonCode != "typed-runtime-integrity-failed" {
					t.Fatal("successful/unfenced report after failed collection", r)
				}
			})
		}
	}
}

func TestFD1AuthenticatedAuthoritySurvivesPreReleaseFailure(t *testing.T) {
	for _, failure := range []string{"effective", "handle", "before-bound", "before-released"} {
		t.Run(failure, func(t *testing.T) {
			x := fakeTypedLaunch(t, "direct", "fake-pipe-fd")
			i, err := readInjection(filepath.Join(x.c.Path, x.a.ID, "injection.json"))
			if err != nil {
				t.Fatal(err)
			}
			original, err := os.ReadFile(i.FD1Runtime.Artifact.Path)
			if err != nil {
				t.Fatal(err)
			}
			check := unchangedFD1Recovery(t, x, i.FD1Runtime)
			cut := errors.New("post-authentication host failure")
			change := func() {
				t.Helper()
				if !sameJSON(x.a.Injector, &i) || !sameJSON(x.a.FD1Runtime, i.FD1Runtime) || !sameJSON(x.a.Target, &i.Process) || x.a.Workload == nil || x.a.Workload.Boundary != i.Boundary || x.a.Workload.Properties["InvocationID"] != i.Invocation {
					t.Fatal("authenticated authority not retained before later effect")
				}
				if err := rewriteFrozenFixture(i.FD1Runtime.Artifact.Path, []byte("changed after authentication")); err != nil {
					t.Fatal(err)
				}
			}
			budget, handle, syncDir := x.h.budget, x.h.handle, x.c.syncDir
			switch failure {
			case "effective", "before-bound":
				x.h.budget = func(*Unit) error {
					change()
					if failure == "effective" {
						return cut
					}
					return nil
				}
			case "handle":
				x.h.handle = func(Process) (*os.File, error) {
					change()
					return nil, cut
				}
			case "before-released":
				x.c.syncDir = func(f *os.File) error {
					if x.a.Injector != nil && x.a.Phase == "launching-service" {
						change() // Bound save has authenticated and written its bytes.
					}
					return syncDir(f)
				}
			}
			err = x.c.run(x.a, x.h)
			if err == nil || x.a.Phase != "cleanup-unknown" || !x.c.Blocked || x.c.fenced || x.a.FD1IntegrityFailure != fd1IntegrityFailure {
				t.Fatal("authenticated failure lost cleanup authority or fence", err)
			}
			if (failure == "effective" || failure == "handle") && !errors.Is(err, cut) {
				t.Fatal("original host failure lost", err)
			}
			x.h.budget, x.h.handle, x.c.syncDir = budget, handle, syncDir
			x.calls = nil
			check()
			// The original receipt file and current artifact no longer authorize
			// adoption. Cleanup must use the already authenticated durable record.
			if err := os.Remove(filepath.Join(x.c.Path, x.a.ID, "injection.json")); err != nil {
				t.Fatal(err)
			}
			reopenFD1Fixture(t, x)
			check()
			if err := x.c.reconcile(x.a, x.h); err != nil {
				t.Fatal("exact original cleanup unavailable", err)
			}
			check()
			if err := rewriteFrozenFixture(i.FD1Runtime.Artifact.Path, original); err != nil {
				t.Fatal(err)
			}
			reopenFD1Fixture(t, x)
			check()
			if x.a.Phase != "complete" || !x.a.Empty || !x.a.TargetExited || !x.a.SliceStopped || !x.c.Blocked || x.a.FD1IntegrityFailure != fd1IntegrityFailure {
				t.Fatal("cleanup/restoration lost incident fence")
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
			if _, err := x.c.Prepare(x.a.Config); err == nil {
				t.Fatal("new launch admitted after incident")
			}
		})
	}
}

func TestFD1CollectionFailureDoesNotInventCleanup(t *testing.T) {
	for _, failure := range []string{"missing-proof", "completion-save", "evidence-save"} {
		t.Run(failure, func(t *testing.T) {
			x := fakeTypedLaunch(t, "direct", "fake-pipe-fd")
			i, err := readInjection(filepath.Join(x.c.Path, x.a.ID, "injection.json"))
			if err != nil {
				t.Fatal(err)
			}
			r := i.FD1Runtime
			original, err := os.ReadFile(r.Artifact.Path)
			if err != nil {
				t.Fatal(err)
			}
			if failure == "missing-proof" {
				bindTypedFixture(t, x)
				if err := rewriteFrozenFixture(r.Artifact.Path, []byte("changed without cleanup proof")); err != nil {
					t.Fatal(err)
				}
				err = x.c.collect(x.a)
				if err == nil || x.a.Phase != "cleanup-unknown" || x.a.Empty || x.a.TargetExited || x.a.SliceStopped || !x.c.Blocked {
					t.Fatal("collection fabricated cleanup", err)
				}
			} else {
				old := x.c.syncFile
				x.c.syncFile = func(f *os.File) error {
					if failure == "completion-save" && x.a.Phase == "empty" || failure == "evidence-save" && x.a.Observation != nil {
						if err := rewriteFrozenFixture(r.Artifact.Path, []byte("changed during failed save")); err != nil {
							return err
						}
						return syscall.EIO
					}
					return old(f)
				}
				err = x.c.run(x.a, x.h)
				if !errors.Is(err, syscall.EIO) || !x.c.Blocked || !x.c.fenced {
					t.Fatal("durability failure hidden", err)
				}
				if failure == "completion-save" && x.a.Phase != "empty" {
					t.Fatal("failed cleanup save published completion")
				}
				x.c.syncFile = old
				// Once writes work, retain the detected incident alongside the
				// existing filesystem failure fence before restoring the artifact.
				if err := x.c.Save(x.a, x.a.Phase, x.a.Reason); err != nil {
					t.Fatal(err)
				}
			}
			if err := rewriteFrozenFixture(r.Artifact.Path, original); err != nil {
				t.Fatal(err)
			}
			if failure != "missing-proof" {
				// Actual durability poisoning deliberately withholds the record
				// on reopen; it must never become ordinary proved cleanup.
				x.c.Close()
				c, err := OpenCampaign(x.c.Path)
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				if !c.Blocked || !c.fenced || c.Records[x.a.ID] != nil {
					t.Fatal("durability-poisoned record admitted after restart")
				}
				data, err := os.ReadFile(filepath.Join(x.c.Path, x.a.ID, "intent.json"))
				var stored Attempt
				if err != nil || decodeHostRecord(bytes.NewReader(data), &stored) != nil || stored.FD1IntegrityFailure != fd1IntegrityFailure {
					t.Fatal("durable incident lost", err)
				}
				return
			}
			reopenFD1Fixture(t, x)
			if !x.c.Blocked || x.a.FD1IntegrityFailure != fd1IntegrityFailure || failure != "missing-proof" && !x.c.fenced {
				t.Fatal("restoration lost integrity/durability fence")
			}
		})
	}
}

func TestFD1ObservedAuthenticationFailureSurvivesImmediateRestoration(t *testing.T) {
	for _, stage := range []string{"release", "collection"} {
		for _, caseName := range []string{"fake-pipe-fd", "inventory"} {
			t.Run(stage+"/"+caseName, func(t *testing.T) {
				x := fakeTypedLaunch(t, "direct", caseName)
				i, err := readInjection(filepath.Join(x.c.Path, x.a.ID, "injection.json"))
				if err != nil {
					t.Fatal(err)
				}
				r := i.FD1Runtime
				original, err := os.ReadFile(r.Artifact.Path)
				if err != nil {
					t.Fatal(err)
				}
				incident := errors.New("observed authentication failure before restoration")
				detected := false
				x.c.authenticateFD1 = func(a *Attempt, runtime *FD1Runtime) error {
					if a.Stage != stage || detected {
						return authenticateFD1Runtime(a, runtime)
					}
					if !sameJSON(a.Injector, &i) || !sameJSON(a.FD1Runtime, r) || !sameJSON(a.Target, &i.Process) || a.Workload == nil {
						t.Fatal("original authenticated authority not retained")
					}
					if err := rewriteFrozenFixture(r.Artifact.Path, []byte("temporary artifact change")); err != nil {
						t.Fatal(err)
					}
					failure := authenticateFD1Runtime(a, runtime)
					if failure == nil {
						t.Fatal("artifact change did not fail authentication")
					}
					// Deterministically restore the very same file immediately after
					// the failed read, before unknown's Save or collect's finalizer.
					if err := rewriteFrozenFixture(r.Artifact.Path, original); err != nil {
						t.Fatal(err)
					}
					if err := authenticateFD1Runtime(a, runtime); err != nil {
						t.Fatal("original artifact not restored", err)
					}
					detected = true
					return errors.Join(incident, failure)
				}
				err = x.c.run(x.a, x.h)
				if !detected || !errors.Is(err, incident) || x.a.FD1IntegrityFailure != fd1IntegrityFailure || !x.c.Blocked || x.c.fenced {
					t.Fatal("observed incident erased by restoration", err)
				}
				if x.a.Observation != nil || x.a.DescriptorReceipt != nil || x.a.DescriptorObservation != nil {
					t.Fatal("failed authentication became case evidence")
				}
				if stage == "release" {
					if x.a.Phase != "launch-unknown" || x.a.Empty || x.a.TargetExited || x.a.SliceStopped {
						t.Fatal("release failure invented cleanup")
					}
					if _, err := os.Stat(filepath.Join(x.c.Path, x.a.ID, "release.json")); !os.IsNotExist(err) {
						t.Fatal("failed authentication published release", err)
					}
				} else if x.a.Phase != "complete" || !x.a.Empty || !x.a.TargetExited || !x.a.SliceStopped {
					t.Fatal("collection discarded independently proved cleanup")
				}
				// Cleanup must use the retained authority, without re-adopting
				// a receipt or dispatching a new service after restart.
				if err := os.Remove(filepath.Join(x.c.Path, x.a.ID, "injection.json")); err != nil {
					t.Fatal(err)
				}
				x.calls = nil
				reopenFD1Fixture(t, x)
				if !x.c.Blocked || x.a.FD1IntegrityFailure != fd1IntegrityFailure || !sameJSON(x.a.Injector, &i) || !sameJSON(x.a.FD1Runtime, r) || !sameJSON(x.a.Target, &i.Process) {
					t.Fatal("restart lost incident or original authority")
				}
				if err := x.c.reconcile(x.a, x.h); err != nil {
					t.Fatal("exact original cleanup unavailable", err)
				}
				for _, unit := range []string{x.a.Service, x.a.Slice} {
					stops := 0
					for _, call := range x.calls {
						if strings.Contains(call, " start ") || strings.Contains(call, "--service-type") || strings.Contains(call, " stop ") && !strings.HasSuffix(call, "-- "+x.a.Service) && !strings.HasSuffix(call, "-- "+x.a.Slice) {
							t.Fatal("new dispatch or unrelated cleanup", call)
						}
						if strings.Contains(call, " stop -- "+unit) {
							stops++
						}
					}
					if stage == "release" && stops != 1 || stage == "collection" && stops != 0 {
						t.Fatal("incorrect original cleanup", unit, stops)
					}
				}
				reopenFD1Fixture(t, x)
				if x.a.Phase != "complete" || !x.c.Blocked || x.a.FD1IntegrityFailure != fd1IntegrityFailure || x.c.collect(x.a) == nil {
					t.Fatal("cleanup/restart erased incident or accepted case")
				}
				if _, err := x.c.Prepare(x.a.Config); err == nil {
					t.Fatal("new launch admitted after restored incident")
				}
				x.c.Close()
				var out bytes.Buffer
				if err := InspectHost(x.c.Path, x.a.ID, &out); err != nil || !bytes.Contains(out.Bytes(), []byte(`"fd1_integrity_failure":"`+fd1IntegrityFailure+`"`)) {
					t.Fatal("durable incident not inspectable", err, out.String())
				}
				report := ReconcileHost(x.c.Path, x.a.ID)
				if report.Outcome != "inconclusive" || !report.NewStartsBlocked || report.ReasonCode != "typed-runtime-integrity-failed" {
					t.Fatal("restoration became successful case", report)
				}
			})
		}
	}
}

func TestFD1NeverAdoptedAuthenticationFailureIsNotTrusted(t *testing.T) {
	x := fakeTypedLaunch(t, "direct", "fake-pipe-fd")
	i, err := readInjection(filepath.Join(x.c.Path, x.a.ID, "injection.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := rewriteFrozenFixture(i.FD1Runtime.Artifact.Path, []byte("changed before adoption")); err != nil {
		t.Fatal(err)
	}
	x.c.authenticateFD1 = func(*Attempt, *FD1Runtime) error {
		t.Fatal("unauthenticated binding reached post-adoption authentication")
		return nil
	}
	if x.c.run(x.a, x.h) == nil || x.a.Injector != nil || x.a.FD1Runtime != nil || x.a.FD1IntegrityFailure != "" || !x.c.Blocked {
		t.Fatal("never-adopted evidence became trusted authority")
	}
	reopenFD1Fixture(t, x)
	if x.a.Injector != nil || x.a.FD1Runtime != nil || x.a.FD1IntegrityFailure != "" || !x.c.Blocked {
		t.Fatal("restart adopted failed evidence")
	}
}
