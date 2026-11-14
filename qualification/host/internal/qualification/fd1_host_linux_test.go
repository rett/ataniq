//go:build linux && amd64

package qualification

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func typedHostFixture(t *testing.T, role, caseName string) *hostFixture {
	t.Helper()
	x := testHost(t)
	a := x.a
	a.DescriptorPolicy = FinalDescriptorPolicy
	a.Config.DescriptorProfile, a.Config.Role, a.Config.Case = FD1TypedProfile, role, caseName
	if role == "candidate" {
		a.Config.DirectControl = strings.Repeat("d", 32)
		a.Control = &ControlBinding{a.Config.DirectControl, a.Config.RunID, a.Boot, a.Created, 300, strings.Repeat("d", 64)}
	}
	path := filepath.Join(x.c.Path, a.ID)
	if err := prepareFD1Static(a, path); err != nil {
		t.Fatal(err)
	}
	a.Command, a.TargetCommand = command(a, path), targetCommand(a, path)
	a.Request = hostRequest(a)
	request, _ := json.Marshal(a.Request)
	if err := os.WriteFile(filepath.Join(path, "request.json"), request, 0400); err != nil {
		t.Fatal(err)
	}
	a.PlanSHA256 = planDigest(a)
	if err := x.c.Save(a, "prepared", ""); err != nil {
		t.Fatal(err)
	}
	return x
}

// Fake controller fixtures never create kernel channels or launch subjects.
func fakeFD1Runtime(t *testing.T, a *Attempt) *FD1Runtime {
	t.Helper()
	rq := typedRequest(CaseFakeDirFD)
	rq.RunID, rq.RequestID = a.Config.RunID, a.Config.RequestID
	rq.Payload.TypedFD.AttemptID, rq.Payload.TypedFD.Nonce = a.ID, a.FixtureNonce
	rq.Payload.TypedFD.Leaf = &a.FD1Static.Challenge
	b := typedBindings(rq)
	b.PlanSHA256 = a.PlanSHA256
	b.Directory.Device, b.Directory.Inode = a.FD1Static.Directory.Identity.Device, a.FD1Static.Directory.Identity.Inode
	b.Regular.Device, b.Regular.Inode, b.Regular.SHA256 = a.Fixture.Identity.Device, a.Fixture.Identity.Inode, a.Fixture.SHA256
	b.Regular.Size = pointer(int64(len(fixtureBytes(a.FixtureNonce))))
	r := &FD1Runtime{Bindings: b, SourcesClosed: true}
	for _, o := range fd1Objects(b) {
		r.Endpoints = append(r.Endpoints, FD1Endpoint{o.FD, FileIdentity{o.Device, o.Inode, uint32(os.Getuid())}, fd1Kind(o.FD) | 0600, fd1ExpectedFlags(o.FD)})
	}
	data, _ := json.Marshal(b)
	path := filepath.Join(a.Config.Campaign, a.ID, "fixtures", FD1BindingName)
	if err := exclusiveFile(path, data, 0400); err != nil {
		t.Fatal(err)
	}
	var err error
	r.Artifact, _, err = subject(path, false, false, true, MaxFD1BindingBytes)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestFD1HostSelectionAndStaticPlan(t *testing.T) {
	for _, role := range []string{"direct", "candidate", "weakened-synthetic"} {
		for _, caseName := range []string{"inventory", "fake-fd", "fake-dir-fd", "fake-pipe-fd", "fake-socket-fd"} {
			x := typedHostFixture(t, role, caseName)
			a := x.a
			path := filepath.Join(x.c.Path, a.ID)
			if err := a.Config.validate(); err != nil {
				t.Fatal(err)
			}
			if err := a.Request.Validate(); err != nil {
				t.Fatal(err)
			}
			if err := validatePlan(a, path); err != nil {
				t.Fatal(err)
			}
			if err := verifyFD1Static(a); err != nil {
				t.Fatal(err)
			}
			args := strings.Join(a.TargetCommand, " ")
			if strings.Contains(args, a.FD1Static.Directory.Path) || strings.Contains(args, a.FD1Static.Leaf.Path) {
				t.Fatal("capability tree mounted", args)
			}
			if role != "direct" && strings.Count(args, "--ro-bind "+filepath.Join(path, "fixtures", FD1BindingName)+" /fixtures/"+FD1BindingName) != 1 {
				t.Fatal("artifact bind missing", args)
			}
			for _, name := range []string{"hold", "fake-file"} {
				c := a.Config
				c.Case = name
				if c.validate() == nil {
					t.Fatal("mixed typed case accepted")
				}
			}
			c := a.Config
			c.DescriptorProfile = "unknown"
			if c.validate() == nil {
				t.Fatal("unknown profile accepted")
			}
			if typedFDCase(a.Request.Case) {
				c.DescriptorProfile = ""
				if c.validate() == nil {
					t.Fatal("typed case without profile")
				}
			}
			if role == "candidate" {
				c = a.Config
				c.DirectControl = ""
				if c.validate() == nil {
					t.Fatal("unscoped candidate")
				}
			}
			for _, mutate := range []func(*Attempt){
				func(a *Attempt) { a.FD1Static = nil },
				func(a *Attempt) { a.FD1Static.MetadataName = "other.json" },
				func(a *Attempt) { a.FD1Static.Challenge.Inode++ },
				func(a *Attempt) { a.Config.DescriptorProfile = "" },
			} {
				data, _ := json.Marshal(a)
				var bad Attempt
				json.Unmarshal(data, &bad)
				mutate(&bad)
				if validatePlan(&bad, path) == nil {
					t.Fatal("changed static plan accepted")
				}
			}
		}
	}
}

func TestFD1RuntimeStandaloneAndImmutablePlan(t *testing.T) {
	x := typedHostFixture(t, "direct", "fake-dir-fd")
	a := x.a
	before, _ := json.Marshal(a.Request)
	digest := a.PlanSHA256
	r := fakeFD1Runtime(t, a)
	if a.Injector != nil || a.FD1Runtime != nil {
		t.Fatal("fixture adopted receipt")
	}
	if err := authenticateFD1Runtime(a, r); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*FD1Runtime){
		func(r *FD1Runtime) { r.SourcesClosed = false },
		func(r *FD1Runtime) { r.Bindings.PlanSHA256 = strings.Repeat("a", 64) },
		func(r *FD1Runtime) { r.Bindings.RunID = "deadbeef" },
		func(r *FD1Runtime) { r.Bindings.RequestID = "deadbeef" },
		func(r *FD1Runtime) { r.Bindings.AttemptID = "deadbeef" },
		func(r *FD1Runtime) { r.Bindings.Profile = "unknown" },
		func(r *FD1Runtime) { r.Bindings.Nonce = "deadbeef" },
		func(r *FD1Runtime) { r.Bindings.Directory.Leaf.Inode++ },
		func(r *FD1Runtime) { r.Bindings.Pipe.PeerClosed = pointer(false) },
		func(r *FD1Runtime) { r.Bindings.Socket.PeerClosed = nil },
		func(r *FD1Runtime) { r.Endpoints[1].Identity.UID++ },
		func(r *FD1Runtime) { r.Endpoints[1].Flags &^= syscall.O_NONBLOCK },
		func(r *FD1Runtime) { r.Endpoints[2].Mode = syscall.S_IFIFO },
		func(r *FD1Runtime) { r.Endpoints = r.Endpoints[:3] },
		func(r *FD1Runtime) { r.Artifact.SHA256 = strings.Repeat("0", 64) },
		func(r *FD1Runtime) { r.Artifact.Identity.Inode++ },
	} {
		data, _ := json.Marshal(r)
		var bad FD1Runtime
		json.Unmarshal(data, &bad)
		mutate(&bad)
		if authenticateFD1Runtime(a, &bad) == nil {
			t.Fatal("bad runtime authenticated")
		}
	}
	a.FD1Runtime = r
	after, _ := json.Marshal(a.Request)
	if !bytes.Equal(before, after) || planDigest(a) != digest {
		t.Fatal("runtime binding changed immutable plan")
	}
	a.Config.DescriptorProfile = ""
	if validateFD1Runtime(a, r) == nil {
		t.Fatal("legacy accepted typed runtime")
	}
	if err := os.Remove(r.Artifact.Path); err != nil {
		t.Fatal(err)
	}
	a.Config.DescriptorProfile = FD1TypedProfile
	if authenticateFD1Runtime(a, r) == nil {
		t.Fatal("missing artifact accepted")
	}
}
