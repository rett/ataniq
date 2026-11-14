//go:build linux && amd64

package qualification

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"syscall"
	"testing"
	"time"
)

type r1FakeClock struct{ now time.Time }

func (c *r1FakeClock) Now() time.Time { return c.now }
func (c *r1FakeClock) Sleep(d time.Duration) {
	if d < 0 {
		panic("negative sleep")
	}
	c.now = c.now.Add(d)
}

type r1FakeStore struct {
	files              map[string]r1File
	id                 R1ObjectIdentity
	read               func(string) error
	checkErr, closeErr error
	closed             bool
}

func (s *r1FakeStore) Directory() R1ObjectIdentity { return s.id }
func (s *r1FakeStore) Check() error                { return s.checkErr }
func (s *r1FakeStore) Close() error                { s.closed = true; return s.closeErr }
func (s *r1FakeStore) Read(name string, _ time.Time) (r1File, error) {
	if s.read != nil {
		if err := s.read(name); err != nil {
			return r1File{}, err
		}
	}
	f, ok := s.files[name]
	if !ok {
		return r1File{}, errR1Absent
	}
	return f, nil
}

type r1FakeMemory struct {
	page, size, calls, last int
	released                bool
	errAt                   int
	clock                   *r1FakeClock
	touchDelay              time.Duration
	releaseErr              error
	onTouch                 func()
}

func (m *r1FakeMemory) Size() int     { return m.size }
func (m *r1FakeMemory) PageSize() int { return m.page }
func (m *r1FakeMemory) TouchPage(off int) error {
	if off != m.calls*m.page || m.released {
		panic("wrong touch stride or released mapping")
	}
	if m.errAt >= 0 && m.calls == m.errAt {
		return syscall.ENOMEM
	}
	m.last = off
	m.calls++
	m.clock.Sleep(m.touchDelay)
	if m.onTouch != nil {
		m.onTouch()
	}
	return nil
}
func (m *r1FakeMemory) Release() error { m.released = true; return m.releaseErr }

type r1FakeChild struct {
	id                         R1ActorIdentity
	clock                      *r1FakeClock
	readyDelay                 time.Duration
	readyErr, aliveErr, endErr error
	endDelay                   time.Duration
	ends                       []string
	deadlines                  []time.Time
	code                       int
	unknown                    bool
	identityUnavailable        bool
}

func (c *r1FakeChild) Ready(d time.Time) (R1ActorIdentity, error) {
	c.clock.Sleep(c.readyDelay)
	if c.identityUnavailable {
		return R1ActorIdentity{}, c.readyErr
	}
	return c.id, c.readyErr
}
func (c *r1FakeChild) Alive() error { return c.aliveErr }
func (c *r1FakeChild) End(token string, d time.Time) (R1ChildResult, error) {
	c.ends = append(c.ends, token)
	c.deadlines = append(c.deadlines, d)
	left := d.Sub(c.clock.Now())
	if left < 0 {
		left = 0
	}
	delay := c.endDelay
	if delay > left {
		delay = left
	}
	c.clock.Sleep(delay)
	r := R1ChildResult{Identity: &c.id, Termination: "unknown", Reap: "unknown", Token: token}
	if c.identityUnavailable {
		r.Identity = nil
	}
	if !c.unknown {
		r.Termination, r.Reap, r.ExitCode = "exited", "reaped", &c.code
	}
	return r, c.endErr
}

type r1Fixture struct {
	d                       r1Deps
	clock                   *r1FakeClock
	store                   *r1FakeStore
	memory                  *r1FakeMemory
	child                   *r1FakeChild
	request                 []byte
	binding                 R1Binding
	release, finish         []byte
	out                     bytes.Buffer
	frames                  []R1Evidence
	spawned, allocated      int
	onFrame                 func(R1Evidence)
	autoRelease, autoFinish bool
}

func r1JSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
func newR1Fixture(t *testing.T) *r1Fixture {
	t.Helper()
	f := &r1Fixture{clock: &r1FakeClock{time.Unix(1, 0)}, autoRelease: true, autoFinish: true}
	r := R1Request{1, "01234567", "89abcdef", "aabbccdd", R1Profile, "1234abcd"}
	f.request = append(r1JSON(r), '\n', ' ')
	f.store = &r1FakeStore{files: map[string]r1File{}, id: R1ObjectIdentity{1, 2}}
	c := R1Context{r, syntheticDigest(f.request), strings.Repeat("a", 64), f.store.id}
	f.store.files[R1ContextName] = r1File{r1JSON(c), "context-subject"}
	f.binding = R1Binding{r, c.RequestSHA256, c.PlanSHA256, syntheticDigest(r1JSON(c))}
	f.release = append(r1JSON(R1Release{f.binding, "release", "ready", 1}), '\n')
	f.finish = append(r1JSON(R1Finish{f.binding, syntheticDigest(f.release), "finish", "retained", 2}), '\n')
	f.memory = &r1FakeMemory{page: 4096, size: R1MemoryBytes, errAt: -1, clock: f.clock}
	mainID := R1ActorIdentity{10, 100, R1ObjectIdentity{1, 3}, R1ObjectIdentity{1, 4}}
	f.child = &r1FakeChild{id: mainID, clock: f.clock}
	f.child.id.PID = 11
	f.d = r1Deps{clock: f.clock, read: bytes.NewReader(f.request).Read,
		write: func(b []byte) (int, error) {
			n, err := f.out.Write(b)
			var e R1Evidence
			if json.Unmarshal(b, &e) == nil && e.Stage != "" && e.Stage != "context" && e.Stage != "request" {
				if !e.valid() {
					t.Fatalf("invalid emitted evidence: %s", b)
				}
				f.frames = append(f.frames, e)
				if e.Stage == "ready" && f.autoRelease {
					f.store.files[R1ReleaseName] = r1File{f.release, "release-subject"}
				}
				if e.Stage == "retained" {
					if f.memory.released {
						t.Fatal("mapping released before retained evidence")
					}
					if f.autoFinish {
						f.store.files[R1FinishName] = r1File{f.finish, "finish-subject"}
					}
				}
				if f.onFrame != nil {
					f.onFrame(e)
				}
			}
			return n, err
		},
		open:  func() (r1Control, error) { return f.store, nil },
		self:  func() (R1ActorIdentity, error) { return mainID, nil },
		spawn: func() (r1Child, error) { f.spawned++; return f.child, nil },
		allocate: func() (r1Memory, error) {
			f.allocated++
			if len(f.frames) < 2 || f.frames[1].Stage != "pressure-start" {
				t.Fatal("allocation before validated pressure release")
			}
			return f.memory, nil
		},
	}
	return f
}
func (f *r1Fixture) last(t *testing.T) R1Failure {
	t.Helper()
	if len(f.frames) == 0 || f.frames[len(f.frames)-1].Failure == nil {
		t.Fatalf("missing failure: %s", f.out.String())
	}
	return *f.frames[len(f.frames)-1].Failure
}

func TestR1FixedActorEvidenceAndRetention(t *testing.T) {
	f := newR1Fixture(t)
	if err := runR1(f.d); err != nil {
		t.Fatal(err, f.out.String())
	}
	if f.spawned != 1 || f.allocated != 1 || !f.memory.released || !f.store.closed || f.memory.calls != R1MemoryBytes/4096 || f.memory.last != R1MemoryBytes-4096 {
		t.Fatal("accounting/cleanup", f.memory)
	}
	if len(f.frames) != 4 || len(f.child.ends) != 1 || f.child.ends[0] != "finish" {
		t.Fatal(f.frames, f.child.ends)
	}
	for n, line := range bytes.Split(bytes.TrimSpace(f.out.Bytes()), []byte{'\n'}) {
		e, err := DecodeR1Evidence(line)
		if err != nil || e.Sequence != n || e.R1Binding != f.binding {
			t.Fatal(n, err, e)
		}
	}
	c := f.frames[3].Completion
	if c.ReleaseSHA256 != syntheticDigest(f.release) || c.FinishSHA256 != syntheticDigest(f.finish) || !c.RetainedUntilFinish || c.Child.Reap != "reaped" {
		t.Fatal(c)
	}
	// This successful low-limit/survival protocol is deliberately admissible;
	// only a later independent host oracle can classify enforcement.
	if strings.Contains(f.out.String(), "pass") || strings.Contains(f.out.String(), "support") {
		t.Fatal("self qualification")
	}
}

func TestR1StartupRejectsBeforeEffects(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*r1Fixture)
	}{
		{"request-hash", func(f *r1Fixture) { f.d.read = bytes.NewReader(bytes.TrimSpace(f.request)).Read }},
		{"bad-request", func(f *r1Fixture) { f.d.read = strings.NewReader(`{"version":1}`).Read }},
		{"missing-context", func(f *r1Fixture) { delete(f.store.files, R1ContextName) }},
		{"malformed-context", func(f *r1Fixture) { f.store.files[R1ContextName] = r1File{[]byte(`{}`), "bad"} }},
		{"directory-mismatch", func(f *r1Fixture) { f.store.id.Inode++ }},
		{"stale-context", func(f *r1Fixture) {
			c, _ := DecodeR1Context(f.store.files[R1ContextName].Data)
			c.Challenge = "eeeeeeee"
			f.store.files[R1ContextName] = r1File{r1JSON(c), "new"}
		}},
		{"context-permission", func(f *r1Fixture) { f.store.read = func(string) error { return syscall.EACCES } }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newR1Fixture(t)
			tc.change(f)
			if runR1(f.d) == nil || f.spawned != 0 || f.allocated != 0 {
				t.Fatal("startup effects")
			}
			if _, err := DecodeR1StartupFailure(f.out.Bytes()); err != nil {
				t.Fatal(err, f.out.String())
			}
		})
	}
}

func TestR1ControlTransitionsBeforeEffects(t *testing.T) {
	for _, tc := range []struct {
		name       string
		change     func(*r1Fixture)
		allocation int
		stage      string
	}{
		{"early-finish-start", func(f *r1Fixture) { f.store.files[R1FinishName] = r1File{f.finish, "finish"} }, 0, "context"},
		{"stale-release-start", func(f *r1Fixture) { f.store.files[R1ReleaseName] = r1File{f.release, "release"} }, 0, "context"},
		{"early-finish-ready", func(f *r1Fixture) {
			f.onFrame = func(e R1Evidence) {
				if e.Stage == "ready" {
					f.store.files[R1FinishName] = r1File{f.finish, "finish"}
				}
			}
		}, 0, "release"},
		{"early-finish-pressure", func(f *r1Fixture) {
			f.onFrame = func(e R1Evidence) {
				if e.Stage == "pressure-start" {
					f.store.files[R1FinishName] = r1File{f.finish, "finish"}
				}
			}
		}, 0, "pressure-start"},
		{"release-wrong-binding", func(f *r1Fixture) {
			r, _ := DecodeR1Release(f.release)
			r.PlanSHA256 = strings.Repeat("b", 64)
			f.release = r1JSON(r)
		}, 0, "release"},
		{"release-malformed", func(f *r1Fixture) { f.release = []byte(`{}`) }, 0, "release"},
		{"release-read-error", func(f *r1Fixture) {
			f.store.read = func(name string) error {
				if name == R1ReleaseName && len(f.frames) > 0 {
					return syscall.EIO
				}
				return nil
			}
		}, 0, "release"},
		{"context-replaced", func(f *r1Fixture) {
			f.onFrame = func(e R1Evidence) {
				if e.Stage == "ready" {
					x := f.store.files[R1ContextName]
					x.Subject = "replaced"
					f.store.files[R1ContextName] = x
				}
			}
		}, 0, "release"},
		{"context-content-drift", func(f *r1Fixture) {
			f.onFrame = func(e R1Evidence) {
				if e.Stage == "ready" {
					x := f.store.files[R1ContextName]
					x.Data = append(x.Data, ' ')
					f.store.files[R1ContextName] = x
				}
			}
		}, 0, "release"},
		{"directory-drift", func(f *r1Fixture) {
			f.onFrame = func(e R1Evidence) {
				if e.Stage == "ready" {
					f.store.checkErr = syscall.ESTALE
				}
			}
		}, 0, "release"},
		{"release-replaced", func(f *r1Fixture) {
			f.onFrame = func(e R1Evidence) {
				if e.Stage == "pressure-start" {
					x := f.store.files[R1ReleaseName]
					x.Subject = "replaced"
					f.store.files[R1ReleaseName] = x
				}
			}
		}, 0, "pressure-start"},
		{"finish-malformed", func(f *r1Fixture) { f.finish = []byte(`{}`) }, 1, "finish"},
		{"finish-wrong-release", func(f *r1Fixture) {
			x, _ := DecodeR1Finish(f.finish)
			x.ReleaseSHA256 = strings.Repeat("c", 64)
			f.finish = r1JSON(x)
		}, 1, "finish"},
		{"finish-wrong-stage", func(f *r1Fixture) { x, _ := DecodeR1Finish(f.finish); x.ExpectedStage = "ready"; f.finish = r1JSON(x) }, 1, "finish"},
		{"early-finish-touch", func(f *r1Fixture) {
			f.memory.onTouch = func() { f.store.files[R1FinishName] = r1File{f.finish, "finish"} }
		}, 1, "touch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newR1Fixture(t)
			tc.change(f)
			if runR1(f.d) == nil {
				t.Fatal("false success")
			}
			failure := f.last(t)
			if f.allocated != tc.allocation || failure.Stage != tc.stage {
				t.Fatal(f.allocated, failure)
			}
			if f.allocated > 0 && !f.memory.released {
				t.Fatal("mapping not cleaned")
			}
			if len(f.frames) > 0 && f.frames[len(f.frames)-1].Completion != nil {
				t.Fatal("false completion")
			}
		})
	}
}

func TestR1ExpectedMissingWaitsBounded(t *testing.T) {
	for _, finish := range []bool{false, true} {
		t.Run(map[bool]string{false: "release", true: "finish"}[finish], func(t *testing.T) {
			f := newR1Fixture(t)
			f.autoRelease = finish
			f.autoFinish = false
			start := f.clock.Now()
			if runR1(f.d) == nil {
				t.Fatal("missing accepted")
			}
			e := f.last(t)
			if e.Error != "deadline" || f.clock.Now().Sub(start) != 2*time.Second || f.allocated != map[bool]int{false: 0, true: 1}[finish] {
				t.Fatal(e, f.clock.Now().Sub(start), f.allocated)
			}
			if finish && (!f.memory.released || len(f.frames) != 4) {
				t.Fatal("retention/failure observations lost")
			}
		})
	}
}

func TestR1TouchAccountingDeadlineAndCleanup(t *testing.T) {
	for _, tc := range []struct {
		name    string
		setup   func(*r1Fixture)
		touched int
		code    string
	}{
		{"partial-touch-error", func(f *r1Fixture) { f.memory.errAt = 3 }, 3 * 4096, "touch"},
		{"touch-deadline", func(f *r1Fixture) { f.memory.touchDelay = R1TouchWait }, 4096, "deadline"},
		{"allocation-error", func(f *r1Fixture) { f.d.allocate = func() (r1Memory, error) { return nil, syscall.ENOMEM } }, 0, "allocation"},
		{"wrong-cap", func(f *r1Fixture) { f.memory.size++ }, 0, "allocation"},
		{"wrong-page", func(f *r1Fixture) { f.memory.page = 3000 }, 0, "allocation"},
		{"unmap-error", func(f *r1Fixture) { f.memory.releaseErr = syscall.EINVAL }, R1MemoryBytes, "cleanup"},
		{"close-error", func(f *r1Fixture) { f.store.closeErr = syscall.EIO }, R1MemoryBytes, "cleanup"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newR1Fixture(t)
			tc.setup(f)
			if runR1(f.d) == nil {
				t.Fatal("false success")
			}
			e := f.last(t)
			if e.TouchedBytes != tc.touched || e.Error != tc.code {
				t.Fatal(e)
			}
			if tc.code == "cleanup" && e.Cleanup != "failed" {
				t.Fatal(e)
			}
		})
	}
}

func TestR1ActualFailurePhaseEvidence(t *testing.T) {
	rejectWrite := func(f *r1Fixture, stage string) {
		write := f.d.write
		f.d.write = func(b []byte) (int, error) {
			var e R1Evidence
			_ = json.Unmarshal(b, &e)
			if e.Stage == stage {
				return 0, io.ErrClosedPipe
			}
			return write(b)
		}
	}
	for _, tc := range []struct {
		name, stage       string
		sequence, touched int
		release, finish   bool
		cleanup           string
		setup             func(*r1Fixture)
	}{
		{"context", "context", 0, 0, false, false, "not-allocated", func(f *r1Fixture) { f.store.checkErr = syscall.EIO }},
		{"child-ready", "child-ready", 0, 0, false, false, "not-allocated", func(f *r1Fixture) { f.child.readyErr = io.ErrUnexpectedEOF }},
		{"ready", "ready", 0, 0, false, false, "not-allocated", func(f *r1Fixture) { rejectWrite(f, "ready") }},
		{"release-wait", "release", 1, 0, false, false, "not-allocated", func(f *r1Fixture) { f.autoRelease = false }},
		{"release-rejected", "release", 1, 0, false, false, "not-allocated", func(f *r1Fixture) { f.release = []byte(`{}`) }},
		{"release-consumed", "release", 1, 0, true, false, "not-allocated", func(f *r1Fixture) {
			reads := 0
			f.store.read = func(name string) error {
				if name == R1ReleaseName && len(f.frames) > 0 {
					reads++
					if reads == 2 {
						x := f.store.files[name]
						x.Subject = "replaced"
						f.store.files[name] = x
					}
				}
				return nil
			}
		}},
		{"pressure-start", "pressure-start", 1, 0, true, false, "not-allocated", func(f *r1Fixture) { rejectWrite(f, "pressure-start") }},
		{"allocate-none", "allocate", 1, 0, true, false, "not-allocated", func(f *r1Fixture) {
			f.d.allocate = func() (r1Memory, error) { return nil, syscall.ENOMEM }
		}},
		{"allocate-cleanup-error", "allocate", 1, 0, true, false, "failed", func(f *r1Fixture) {
			f.memory.releaseErr = syscall.EIO
			f.d.allocate = func() (r1Memory, error) { return f.memory, syscall.ENOMEM }
		}},
		{"touch-zero", "touch", 1, 0, true, false, "released", func(f *r1Fixture) { f.memory.errAt = 0 }},
		{"touch-partial", "touch", 1, 3 * 4096, true, false, "released", func(f *r1Fixture) { f.memory.errAt = 3 }},
		{"touch-partial-cleanup-error", "touch", 1, 3 * 4096, true, false, "failed", func(f *r1Fixture) { f.memory.errAt = 3; f.memory.releaseErr = syscall.EIO }},
		{"retained", "retained", 2, R1MemoryBytes, true, false, "released", func(f *r1Fixture) { rejectWrite(f, "retained") }},
		{"finish-wait", "finish", 2, R1MemoryBytes, true, false, "released", func(f *r1Fixture) { f.autoFinish = false }},
		{"finish-rejected", "finish", 2, R1MemoryBytes, true, false, "released", func(f *r1Fixture) { f.finish = []byte(`{}`) }},
		{"finish-consumed", "finish", 2, R1MemoryBytes, true, true, "released", func(f *r1Fixture) {
			reads := 0
			f.store.read = func(name string) error {
				if name == R1FinishName && len(f.frames) >= 3 {
					reads++
					if reads == 2 {
						return syscall.EIO
					}
				}
				return nil
			}
		}},
		{"cleanup", "cleanup", 2, R1MemoryBytes, true, true, "failed", func(f *r1Fixture) { f.memory.releaseErr = syscall.EINVAL }},
		{"child-reap", "child-reap", 3, R1MemoryBytes, true, true, "released", func(f *r1Fixture) { f.child.unknown = true; f.child.endErr = syscall.ECHILD }},
		{"completion", "completion", 3, R1MemoryBytes, true, true, "released", func(f *r1Fixture) { rejectWrite(f, "completion") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newR1Fixture(t)
			tc.setup(f)
			if runR1(f.d) == nil {
				t.Fatal("failure became success")
			}
			failure := f.last(t)
			e := f.frames[len(f.frames)-1]
			if failure.Stage != tc.stage || e.Sequence != tc.sequence || failure.TouchedBytes != tc.touched || failure.Cleanup != tc.cleanup {
				t.Fatal("failure phase/accounting lost", e)
			}
			if (failure.ReleaseSHA256 != "") != tc.release || (failure.FinishSHA256 != "") != tc.finish ||
				(tc.release && failure.ReleaseSHA256 != syntheticDigest(f.release)) || (tc.finish && failure.FinishSHA256 != syntheticDigest(f.finish)) {
				t.Fatal("consumed hashes lost", failure)
			}
			var out bytes.Buffer
			if err := EncodeR1Evidence(&out, e); err != nil {
				t.Fatal("actual failure cannot encode", err)
			}
			if _, err := DecodeR1Evidence(out.Bytes()); err != nil {
				t.Fatal("actual failure cannot decode", err)
			}
			for _, frame := range f.frames {
				if frame.Completion != nil {
					t.Fatal("false completion")
				}
			}
		})
	}
}

func TestR1ChildFailuresNeverComplete(t *testing.T) {
	for _, tc := range []struct {
		name       string
		setup      func(*r1Fixture)
		allocation int
	}{
		{"not-ready", func(f *r1Fixture) { f.child.readyErr = io.ErrUnexpectedEOF }, 0},
		{"unsupported-metadata", func(f *r1Fixture) { f.child.readyErr = errR1Metadata; f.child.identityUnavailable = true }, 0},
		{"wrong-executable", func(f *r1Fixture) { f.child.id.Executable.Inode++ }, 0},
		{"wrong-pidns", func(f *r1Fixture) { f.child.id.PIDNamespace.Inode++ }, 0},
		{"same-pid", func(f *r1Fixture) { f.child.id.PID = 10 }, 0},
		{"child-early-loss", func(f *r1Fixture) { f.child.aliveErr = io.EOF }, 0},
		{"child-lost-retained", func(f *r1Fixture) {
			f.onFrame = func(e R1Evidence) {
				if e.Stage == "retained" {
					f.child.aliveErr = io.EOF
				}
			}
		}, 1},
		{"reap-unknown", func(f *r1Fixture) { f.child.endErr = syscall.ECHILD; f.child.unknown = true }, 1},
		{"nonzero-exit", func(f *r1Fixture) { f.child.code = 22 }, 1},
		{"cleanup-budget", func(f *r1Fixture) {
			f.child.endDelay = time.Second
			f.child.endErr = errR1Deadline
			f.child.unknown = true
		}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newR1Fixture(t)
			tc.setup(f)
			if runR1(f.d) == nil {
				t.Fatal("false success")
			}
			e := f.last(t)
			if f.allocated != tc.allocation || (e.Child.Identity == nil) != f.child.identityUnavailable {
				t.Fatal(e)
			}
			if tc.name == "unsupported-metadata" && e.Error != "metadata-unsupported" {
				t.Fatal(e)
			}
			if tc.name == "cleanup-budget" {
				if len(f.child.deadlines) != 2 || !f.child.deadlines[0].Equal(f.child.deadlines[1]) || f.clock.Now().Sub(time.Unix(1, 0)) != R1ReapWait {
					t.Fatal(f.child.deadlines, f.clock.now)
				}
			}
		})
	}
}

func TestR1DeadlineComposition(t *testing.T) {
	c := &r1FakeClock{time.Unix(1, 0)}
	overall := c.Now().Add(R1MainLifetime)
	for _, span := range []time.Duration{R1ReadyWait, R1ReleaseWait, R1TouchWait, R1FinishWait, R1ReapWait} {
		d := r1Deadline(c, overall, span)
		if d.After(overall) {
			t.Fatal("deadline escaped overall")
		}
		c.Sleep(span)
	}
	if c.Now().Sub(time.Unix(1, 0)) != 7500*time.Millisecond {
		t.Fatal("fixed budget sum")
	}
	if d := r1Deadline(c, overall, R1ReadyWait); !d.Equal(overall) {
		t.Fatal("overall cap not composed")
	}
	c.Sleep(time.Second)
	if !r1Expired(c, overall) || r1Pause(c, overall) != errR1Deadline {
		t.Fatal("past deadline accepted")
	}
	f := newR1Fixture(t)
	f.child.readyDelay = R1ReadyWait
	if runR1(f.d) == nil || f.allocated != 0 || f.last(t).Error != "deadline" {
		t.Fatal("readiness bound")
	}
}

func TestR1PartialIOAndFailurePreservesFrames(t *testing.T) {
	c := &r1FakeClock{time.Unix(1, 0)}
	d := c.Now().Add(time.Second)
	var out bytes.Buffer
	write := func(b []byte) (int, error) {
		if len(b) > 2 {
			b = b[:2]
		}
		return out.Write(b)
	}
	if err := r1Write(c, d, write, []byte("abcdef")); err != nil || out.String() != "abcdef" {
		t.Fatal(err, out.String())
	}
	read := bytes.NewReader([]byte("abcdef"))
	b, err := r1Read(c, d, func(p []byte) (int, error) {
		if len(p) > 2 {
			p = p[:2]
		}
		return read.Read(p)
	}, 6)
	if err != nil || string(b) != "abcdef" {
		t.Fatal(err, string(b))
	}
	if _, err := r1Read(c, d, bytes.NewReader([]byte("abcdefg")).Read, 6); err == nil {
		t.Fatal("cap accepted")
	}
	for _, retry := range []error{syscall.EAGAIN, syscall.EINTR, nil} {
		c.now = time.Unix(1, 0)
		if err := r1Write(c, d, func([]byte) (int, error) { return 0, retry }, []byte("x")); err != errR1Deadline {
			t.Fatal(err)
		}
	}
	f := newR1Fixture(t)
	original := f.d.write
	f.d.write = func(b []byte) (int, error) {
		var e R1Evidence
		_ = json.Unmarshal(b, &e)
		if e.Stage == "retained" {
			return 0, io.ErrClosedPipe
		}
		return original(b)
	}
	if runR1(f.d) == nil {
		t.Fatal("write failure hidden")
	}
	e := f.last(t)
	if len(f.frames) != 3 || f.frames[0].Stage != "ready" || f.frames[1].Stage != "pressure-start" || e.Error != "io" || !f.memory.released {
		t.Fatal(f.frames, e)
	}
}

func TestR1ChildTokensEOFAndIndependentLifetime(t *testing.T) {
	for _, tc := range []struct {
		name, frame string
		code        int
		elapsed     time.Duration
	}{
		{"finish", r1FinishToken, 0, 0}, {"abort", r1AbortToken, 20, 0}, {"wrong", "finish\n", 23, 0},
		{"trailing", r1FinishToken + "x", 23, 0}, {"eof", "", 22, R1ChildLifetime}, {"partial-eof", "ataniq-", 22, R1ChildLifetime},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &r1FakeClock{time.Unix(1, 0)}
			start := c.Now()
			code := r1ChildTokens(c, start.Add(R1ChildLifetime), strings.NewReader(tc.frame).Read)
			if code != tc.code || c.Now().Sub(start) != tc.elapsed {
				t.Fatal(code, c.Now().Sub(start))
			}
		})
	}
	c := &r1FakeClock{time.Unix(1, 0)}
	code := r1ChildTokens(c, c.Now().Add(R1ChildLifetime), func([]byte) (int, error) { return 0, syscall.EAGAIN })
	if code != 21 || c.Now().Sub(time.Unix(1, 0)) != R1ChildLifetime {
		t.Fatal(code, c.now)
	}
}

func TestR1ChildHandshakeFailures(t *testing.T) {
	id := R1ActorIdentity{11, 100, R1ObjectIdentity{1, 3}, R1ObjectIdentity{1, 4}}
	for _, b := range [][]byte{[]byte(`{}`), []byte(`{"version":1,"status":"observed","identity":null}`), r1JSON(r1Handshake{1, "observed", nil}), r1JSON(r1Handshake{2, "observed", &id})} {
		if _, err := r1DecodeHandshake(b); err == nil {
			t.Fatal(string(b))
		}
	}
	if _, err := r1DecodeHandshake(r1JSON(r1Handshake{1, "unsupported", nil})); !errors.Is(err, errR1Metadata) {
		t.Fatal(err)
	}
	for _, unsupported := range []bool{false, true} {
		c := &r1FakeClock{time.Unix(1, 0)}
		var out bytes.Buffer
		closed := false
		code := runR1Child(c, strings.NewReader(r1FinishToken).Read, out.Write, func() (R1ActorIdentity, error) {
			if unsupported {
				return R1ActorIdentity{}, syscall.EACCES
			}
			return id, nil
		}, func() error { closed = true; return nil })
		h, err := r1DecodeHandshake(out.Bytes())
		if !closed || (!unsupported && (code != 0 || err != nil || *h.Identity != id)) || (unsupported && (code != 24 || !errors.Is(err, errR1Metadata))) {
			t.Fatal(code, h, err)
		}
	}
}

func TestR1ConsumedFinishReplacementAndOverallDeadline(t *testing.T) {
	f := newR1Fixture(t)
	seen := 0
	f.store.read = func(name string) error {
		if name == R1FinishName {
			if x, ok := f.store.files[name]; ok {
				seen++
				if seen == 2 {
					x.Subject = "replacement"
					f.store.files[name] = x
				}
			}
		}
		return nil
	}
	if runR1(f.d) == nil || f.last(t).Stage != "finish" || !f.memory.released || len(f.child.ends) != 1 || f.child.ends[0] != "abort" {
		t.Fatal("finish replacement accepted", f.out.String())
	}
	f = newR1Fixture(t)
	f.autoRelease = false
	f.child.readyDelay = 1900 * time.Millisecond
	f.d.overall = f.clock.Now().Add(2400 * time.Millisecond)
	start := f.clock.Now()
	if runR1(f.d) == nil || f.last(t).Error != "deadline" || f.allocated != 0 || f.clock.Now().Sub(start) != 2400*time.Millisecond {
		t.Fatal("overall deadline escaped", f.out.String(), f.clock.Now().Sub(start))
	}
}

func TestR1InputAndPrivateHandshakeIOBounds(t *testing.T) {
	f := newR1Fixture(t)
	f.d.read = func([]byte) (int, error) { return 0, syscall.EAGAIN }
	if runR1(f.d) == nil || f.spawned != 0 || f.allocated != 0 || f.clock.Now().Sub(time.Unix(1, 0)) != R1ReadyWait {
		t.Fatal("unbounded input")
	}
	startup, err := DecodeR1StartupFailure(f.out.Bytes())
	if err != nil || startup.Error != "deadline" || startup.Stage != "request" {
		t.Fatal(startup, err)
	}
	c := &r1FakeClock{time.Unix(1, 0)}
	var output bytes.Buffer
	id := R1ActorIdentity{11, 100, R1ObjectIdentity{1, 3}, R1ObjectIdentity{1, 4}}
	closed := false
	code := runR1Child(c, strings.NewReader(r1FinishToken).Read, func(b []byte) (int, error) {
		if len(b) > 3 {
			b = b[:3]
		}
		return output.Write(b)
	}, func() (R1ActorIdentity, error) { return id, nil }, func() error { closed = true; return nil })
	if code != 0 || !closed {
		t.Fatal(code)
	}
	if _, err := r1DecodeHandshake(output.Bytes()); err != nil {
		t.Fatal(err)
	}
	c.now = time.Unix(1, 0)
	closed = false
	code = runR1Child(c, strings.NewReader(r1FinishToken).Read, func([]byte) (int, error) { return 0, syscall.EAGAIN }, func() (R1ActorIdentity, error) { return id, nil }, func() error { closed = true; return nil })
	if code != 23 || !closed || c.Now().Sub(time.Unix(1, 0)) != R1ReadyWait {
		t.Fatal(code, c.now)
	}
	code = runR1Child(c, strings.NewReader(r1FinishToken).Read, output.Write, func() (R1ActorIdentity, error) { return id, nil }, func() error { return syscall.EIO })
	if code != 23 {
		t.Fatal("handshake close failure hidden")
	}
}

func TestR1InputMutationAfterAllocationFailsBeforeTouch(t *testing.T) {
	f := newR1Fixture(t)
	old := f.d.allocate
	f.d.allocate = func() (r1Memory, error) {
		m, err := old()
		x := f.store.files[R1ContextName]
		x.Subject = "replaced"
		f.store.files[R1ContextName] = x
		return m, err
	}
	if runR1(f.d) == nil || f.memory.calls != 0 || !f.memory.released {
		t.Fatal("touch followed context drift")
	}
}

func TestR1StartupCloseFailureIsEvidence(t *testing.T) {
	f := newR1Fixture(t)
	delete(f.store.files, R1ContextName)
	f.store.closeErr = syscall.EIO
	if runR1(f.d) == nil {
		t.Fatal("false success")
	}
	e, err := DecodeR1StartupFailure(f.out.Bytes())
	if err != nil || e.Cleanup != "failed" || e.Error != "control" || f.spawned != 0 {
		t.Fatal(e, err)
	}
}
