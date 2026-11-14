//go:build linux && amd64

package qualification

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func rewriteFrozenFixture(path string, data []byte) error {
	if err := os.Chmod(path, 0600); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0400); err != nil {
		return err
	}
	return os.Chmod(path, 0400)
}

func fakeTypedLaunch(t *testing.T, role, caseName string) *hostFixture {
	t.Helper()
	x := typedHostFixture(t, role, caseName)
	a := x.a
	r := fakeFD1Runtime(t, a)
	path := filepath.Join(x.c.Path, a.ID)
	i, err := readInjection(filepath.Join(path, "injection.json"))
	if err != nil {
		t.Fatal(err)
	}
	i.DescriptorPolicy = a.DescriptorPolicy
	i.FD1Runtime = r
	i.Descriptors = descriptorsForBinding(a, r)
	data, _ := json.Marshal(i)
	if err := rewriteFrozenFixture(filepath.Join(path, "injection.json"), data); err != nil {
		t.Fatal(err)
	}
	copy := *a
	copy.Injector = &i
	copy.FD1Runtime = r
	receipt := descriptorReceipt(&copy)
	data, _ = json.Marshal(receipt)
	if err := exclusiveFile(filepath.Join(path, "descriptor-policy.json"), data, 0400); err != nil {
		t.Fatal(err)
	}
	o := Observation{Version: 1, RunID: a.Config.RunID, RequestID: a.Config.RequestID, Case: a.Request.Case, Status: "observed", Facts: []Fact{}}
	if caseName == "inventory" {
		fds := descriptorStdio(a)
		if role != "candidate" {
			for _, e := range r.Endpoints {
				fds = append(fds, fdMetadata{e.FD, e.Identity.Device, e.Identity.Inode, e.Mode})
			}
		}
		o.Facts = descriptorInventory(fds).Facts
	} else if role == "candidate" {
		n := int(syscall.EBADF)
		o.Errno = &n
		o.Facts = []Fact{known("stage", "fstat")}
	} else {
		o.Facts = []Fact{known("identity_match", "true"), known("opened", "true"), known("read_success", "true"), known("digest_match", "true"), known("frame_match", "true"), known("eof", "true")}
	}
	var output bytes.Buffer
	if err := EncodeObservation(&output, o); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "output"), output.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return x
}

func TestFD1ControllerNeverReleasesPartialEvidence(t *testing.T) {
	for _, scenario := range []string{"missing", "hash", "replacement", "artifact-type", "extra-key", "run", "attempt", "profile", "nonce", "plan", "leaf", "owner", "type", "flags", "peer-close", "sources-close", "missing-endpoint", "wrong-invocation", "durable-save", "changed-post-bind", "release-uncertain"} {
		t.Run(scenario, func(t *testing.T) {
			x := fakeTypedLaunch(t, "direct", "fake-dir-fd")
			path := filepath.Join(x.c.Path, x.a.ID)
			i, err := readInjection(filepath.Join(path, "injection.json"))
			if err != nil {
				t.Fatal(err)
			}
			r := i.FD1Runtime
			switch scenario {
			case "missing":
				os.Remove(r.Artifact.Path)
			case "hash":
				r.Artifact.SHA256 = strings.Repeat("0", 64)
			case "replacement":
				b, e := os.ReadFile(r.Artifact.Path)
				if e != nil {
					t.Fatal(e)
				}
				if e = os.Rename(r.Artifact.Path, r.Artifact.Path+".old"); e != nil {
					t.Fatal(e)
				}
				if e = exclusiveFile(r.Artifact.Path, b, 0400); e != nil {
					t.Fatal(e)
				}
			case "artifact-type":
				os.Remove(r.Artifact.Path)
				if e := os.Mkdir(r.Artifact.Path, 0700); e != nil {
					t.Fatal(e)
				}
			case "extra-key":
				b, _ := json.Marshal(r.Bindings)
				b = append(b[:len(b)-1], []byte(`,"extra":true}`)...)
				if e := rewriteFrozenFixture(r.Artifact.Path, b); e != nil {
					t.Fatal(e)
				}
				r.Artifact, _, err = subject(r.Artifact.Path, false, false, true, MaxFD1BindingBytes)
				if err != nil {
					t.Fatal(err)
				}
			case "run":
				r.Bindings.RunID = "deadbeef"
			case "attempt":
				r.Bindings.AttemptID = "deadbeef"
			case "profile":
				r.Bindings.Profile = "unknown"
			case "nonce":
				r.Bindings.Nonce = "deadbeef"
			case "plan":
				r.Bindings.PlanSHA256 = strings.Repeat("0", 64)
			case "leaf":
				r.Bindings.Directory.Leaf.Inode++
			case "owner":
				r.Endpoints[1].Identity.UID++
			case "type":
				r.Endpoints[2].Mode = syscall.S_IFIFO
			case "flags":
				r.Endpoints[1].Flags = 0
			case "peer-close":
				r.Bindings.Socket.PeerClosed = pointer(false)
			case "sources-close":
				r.SourcesClosed = false
			case "missing-endpoint":
				r.Endpoints = r.Endpoints[:3]
			case "wrong-invocation":
				i.Invocation = strings.Repeat("c", 32)
			case "durable-save":
				old := x.c.syncFile
				x.c.syncFile = func(f *os.File) error {
					if x.a.Injector != nil {
						return syscall.EIO
					}
					return old(f)
				}
			case "changed-post-bind":
				old := x.c.syncDir
				x.c.syncDir = func(f *os.File) error {
					if x.a.Phase == "bound" {
						if e := rewriteFrozenFixture(r.Artifact.Path, []byte("changed")); e != nil {
							return e
						}
					}
					return old(f)
				}
			case "release-uncertain":
				if e := exclusiveFile(filepath.Join(path, "release.json"), []byte("unknown release"), 0400); e != nil {
					t.Fatal(e)
				}
			}
			b, _ := json.Marshal(i)
			if err := rewriteFrozenFixture(filepath.Join(path, "injection.json"), b); err != nil {
				t.Fatal(err)
			}
			if err := x.c.run(x.a, x.h); err == nil {
				t.Fatal("partial binding released")
			}
			if !x.c.Blocked {
				t.Fatal("uncertainty did not fence new starts")
			}
			b, err = os.ReadFile(filepath.Join(path, "release.json"))
			if scenario == "release-uncertain" {
				if string(b) != "unknown release" {
					t.Fatal("release replaced")
				}
			} else if !os.IsNotExist(err) {
				t.Fatal("published release", string(b), err)
			}
		})
	}
}

func TestFD1ControllerCollectionAndRestart(t *testing.T) {
	for _, role := range []string{"direct", "candidate", "weakened-synthetic"} {
		for _, caseName := range []string{"inventory", "fake-fd", "fake-dir-fd", "fake-pipe-fd", "fake-socket-fd"} {
			t.Run(role+"/"+caseName, func(t *testing.T) {
				x := fakeTypedLaunch(t, role, caseName)
				before, _ := json.Marshal(x.a.Request)
				digest := x.a.PlanSHA256
				requestBefore, err := os.ReadFile(filepath.Join(x.c.Path, x.a.ID, "request.json"))
				if err != nil {
					t.Fatal(err)
				}
				if err := x.c.run(x.a, x.h); err != nil {
					t.Fatal(err)
				}
				after, _ := json.Marshal(x.a.Request)
				requestAfter, _ := os.ReadFile(filepath.Join(x.c.Path, x.a.ID, "request.json"))
				if !bytes.Equal(before, after) || !bytes.Equal(requestBefore, requestAfter) || x.a.PlanSHA256 != digest || planDigest(x.a) != digest {
					t.Fatal("plan/request changed after runtime binding")
				}
				if x.a.FD1Runtime == nil || x.a.DescriptorReceipt == nil || x.a.Phase != "complete" {
					t.Fatal("binding evidence lost")
				}
				x.c.Close()
				c, err := OpenCampaign(x.c.Path)
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				if c.Blocked || c.Records[x.a.ID] == nil {
					t.Fatal("typed evidence not readable")
				}
				if err := os.Remove(x.a.FD1Runtime.Artifact.Path); err != nil {
					t.Fatal(err)
				}
				if _, err := c.read(x.a.ID); err == nil {
					t.Fatal("missing recorded artifact reconstructed")
				}
			})
		}
	}
}

func TestFD1TypedControlScope(t *testing.T) {
	for _, caseName := range []string{"inventory", "fake-fd", "fake-dir-fd", "fake-pipe-fd", "fake-socket-fd"} {
		x := fakeTypedLaunch(t, "direct", caseName)
		if err := x.c.run(x.a, x.h); err != nil {
			t.Fatal(err)
		}
		d := x.a
		a := *d
		a.Config.Role = "candidate"
		a.Config.DirectControl = d.ID
		if err := validateControl(&a, d, d.Created); err != nil {
			t.Fatal(err)
		}
		for _, scenario := range []string{"stale", "case", "profile", "hash", "boot", "run", "cleanup", "proof", "receipt", "frame"} {
			b, _ := json.Marshal(d)
			var bad Attempt
			json.Unmarshal(b, &bad)
			switch scenario {
			case "stale":
				bad.Created -= 301
			case "case":
				bad.Config.Case = "hold"
			case "profile":
				bad.Config.DescriptorProfile = ""
			case "hash":
				s := bad.Subjects["driver"]
				s.SHA256 = strings.Repeat("0", 64)
				bad.Subjects["driver"] = s
			case "boot":
				bad.Boot = "11111111-1111-1111-1111-111111111111"
			case "run":
				bad.Config.RunID = "deadbeef"
			case "cleanup":
				bad.Empty = false
			case "proof":
				bad.Observation = nil
			case "receipt":
				bad.DescriptorReceipt = nil
			case "frame":
				bad.Observation.Status = "inconclusive"
				bad.Observation.Reason = "short frame"
			}
			if validateControl(&a, &bad, d.Created) == nil {
				t.Fatal("invalid typed control", caseName, scenario)
			}
		}
		if err := validateControl(&a, nil, d.Created); err == nil {
			t.Fatal("absent direct proof")
		}
	}
}

func TestFD1LostLaunchAckRetainsCleanupFence(t *testing.T) {
	x := fakeTypedLaunch(t, "direct", "inventory")
	x.ackError = true
	if err := x.c.run(x.a, x.h); err == nil {
		t.Fatal("lost ack accepted")
	}
	if x.a.Phase != "launch-unknown" || !x.c.Blocked {
		t.Fatal("unknown launch not fenced")
	}
	if _, err := os.Stat(filepath.Join(x.c.Path, x.a.ID, "release.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("released after lost ack")
	}
	x.c.Close()
	c, err := OpenCampaign(x.c.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if !c.Blocked || c.Records[x.a.ID] == nil {
		t.Fatal("restart lost launch uncertainty")
	}
}

func TestFD1ExpiredControlCannotRelease(t *testing.T) {
	x := fakeTypedLaunch(t, "candidate", "fake-pipe-fd")
	old := x.h.budget
	x.h.budget = func(u *Unit) error { *x.a.Control.DirectCreated -= 301; return old(u) }
	if err := x.c.run(x.a, x.h); err == nil {
		t.Fatal("stale control released")
	}
	if _, err := os.Stat(filepath.Join(x.c.Path, x.a.ID, "release.json")); !os.IsNotExist(err) {
		t.Fatal("release published", err)
	}
	if !x.c.Blocked {
		t.Fatal("stale control not fenced")
	}
}

func typedHostFixture(t *testing.T, role, caseName string) *hostFixture {
	t.Helper()
	x := testHost(t)
	a := x.a
	a.DescriptorPolicy = FinalDescriptorPolicy
	a.Config.DescriptorProfile, a.Config.Role, a.Config.Case = FD1TypedProfile, role, caseName
	if role == "candidate" {
		a.Config.DirectControl = strings.Repeat("d", 32)
		a.Control = &ControlBinding{a.Config.DirectControl, a.Config.RunID, a.Boot, a.Created, 300, strings.Repeat("d", 64), pointer(a.Created)}
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
		func(r *FD1Runtime) { r.Endpoints[0].Flags |= 0x200000 },
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

func TestFD1FixedRoleAndVacancyTable(t *testing.T) {
	for _, role := range []string{"candidate", "direct", "weakened-synthetic"} {
		a := &Attempt{Config: HostConfig{DescriptorProfile: FD1TypedProfile, Role: role}}
		for _, fd := range []int{0, 1, 2, 3, 64, 128, 256, 384, 512, 513, 768, 1000, 1001} {
			want := fd <= 2 || fd == 513 && role != "direct" || (fd == 128 || fd == 256 || fd == 384 || fd == 512) && role != "candidate"
			if permittedForAttempt(a, fd) != want {
				t.Fatal("wrong fixed role", role, fd)
			}
		}
		for _, fd := range injectionSlots(a) {
			if vacantSlots(uint64(fd), nil, injectionSlots(a)) == nil || vacantSlots(1024, map[int]bool{fd: true}, injectionSlots(a)) == nil {
				t.Fatal("typed slot limit/collision accepted", fd)
			}
		}
		if err := vacantSlots(1024, nil, injectionSlots(a)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPrepareFD1FreezesChallengeBeforeRequest(t *testing.T) {
	for _, caseName := range []string{"inventory", "fake-fd", "fake-dir-fd", "fake-pipe-fd", "fake-socket-fd"} {
		x := testHost(t)
		config := x.a.Config
		config.Case = caseName
		config.DescriptorProfile = FD1TypedProfile
		config.Campaign = t.TempDir()
		if err := os.Chmod(config.Campaign, 0700); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"driver", "probe", "bwrap", "bpf"} {
			data := launcherBytes("", 0, "")
			if name == "bpf" {
				data = make([]byte, 8)
			}
			if err := os.Chmod(selected(config)[name], 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(selected(config)[name], data, 0500); err != nil {
				t.Fatal(err)
			}
		}
		c, err := OpenCampaign(config.Campaign)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		a, err := c.Prepare(config)
		if err != nil {
			t.Fatal(err)
		}
		if a.FD1Static == nil || a.FD1Runtime != nil || a.Injector != nil {
			t.Fatal("runtime identity adopted in static plan")
		}
		b, err := os.ReadFile(filepath.Join(c.Path, a.ID, "request.json"))
		if err != nil {
			t.Fatal(err)
		}
		want, _ := json.Marshal(hostRequest(a))
		if !bytes.Equal(b, want) {
			t.Fatal("request not frozen")
		}
		if _, err := c.read(a.ID); err != nil {
			t.Fatal(err)
		}
		if err := verifyFD1Static(a); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(c.Path, a.ID, "fixtures", FD1BindingName)); !os.IsNotExist(err) {
			t.Fatal("artifact prepared before channel creation")
		}
	}
}
