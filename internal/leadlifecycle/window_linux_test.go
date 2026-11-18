package leadlifecycle

import (
	"math"
	"testing"
	"time"

	"ataniq/internal/leadregistry"
)

// withOptions replaces the controller by one built with the rig's options and
// the changes of mut.
func (r *rig) withOptions(mut func(*Options)) error {
	r.t.Helper()
	o := Options{Registry: r.reg, Manager: r.aud, Clock: r.clock, Preflight: r.pre, Holder: r.holder, Quiesce: r.quiet}
	mut(&o)
	ctl, err := New(o)
	if err != nil {
		return err
	}
	r.ctl = ctl
	return nil
}

func TestWindowArithmeticCannotOverflow(t *testing.T) {
	for name, mut := range map[string]func(*Options){
		"maximum window, default margin": func(o *Options) { o.LateStartWindow = time.Duration(math.MaxInt64) },
		"maximum window and margin": func(o *Options) {
			o.LateStartWindow = time.Duration(math.MaxInt64)
			o.LateStartMargin = time.Duration(math.MaxInt64)
		},
		"window and margin that add up past the limit": func(o *Options) {
			o.LateStartWindow = time.Duration(math.MaxInt64/2 + 1)
			o.LateStartMargin = time.Duration(math.MaxInt64/2 + 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			role := replyLostRole(t, r)
			noErr(t, r.withOptions(mut))
			// The sum of the two durations wraps to a negative number; a window
			// that wrapped into the past would close at once.
			for _, step := range []time.Duration{pastTheWindow, 24 * time.Hour, 365 * 24 * time.Hour} {
				r.advance(step)
				rev := r.revision()
				rr, _ := r.reconcile().Find(role)
				if rr.Action != ActionPending || r.revision() != rev {
					t.Fatalf("after %v: report %+v", step, rr)
				}
			}
			if e := r.get(role); e.Intent.Outcome != leadregistry.OutcomePending {
				t.Fatalf("entry %+v", e)
			}
			_, err := r.stop(role)
			isErr(t, err, ErrIntentPending)
		})
	}
}

func TestWindowClosesAtTheEndOfTheWindowAndMargin(t *testing.T) {
	r := newRig(t)
	in := leadregistry.Intent{At: t0}
	noErr(t, r.withOptions(func(o *Options) {}))
	if got, want := r.ctl.windowClosesAt(in), t0.Add(DefaultLateStartWindow+DefaultLateStartMargin); !got.Equal(want) {
		t.Fatalf("closes at %v, want %v", got, want)
	}
	noErr(t, r.withOptions(func(o *Options) { o.LateStartWindow = time.Minute; o.LateStartMargin = time.Second }))
	if got, want := r.ctl.windowClosesAt(in), t0.Add(61*time.Second); !got.Equal(want) {
		t.Fatalf("closes at %v, want %v", got, want)
	}
	noErr(t, r.withOptions(func(o *Options) {
		o.LateStartWindow = time.Duration(math.MaxInt64)
		o.LateStartMargin = time.Duration(math.MaxInt64)
	}))
	if got := r.ctl.windowClosesAt(in); !got.After(t0.Add(50 * 365 * 24 * time.Hour)) {
		t.Fatalf("closes at %v", got)
	}
}

func TestNoLateStartMargin(t *testing.T) {
	r := newRig(t)
	if err := r.withOptions(func(o *Options) { o.NoLateStartMargin = true; o.LateStartMargin = time.Second }); err == nil {
		t.Fatal("a margin and no margin were both accepted")
	}
	role := replyLostRole(t, r)
	noErr(t, r.withOptions(func(o *Options) { o.NoLateStartMargin = true }))
	if r.ctl.margin != 0 {
		t.Fatalf("margin %v", r.ctl.margin)
	}
	// With the default margin the intent would still be open here.
	r.advance(DefaultLateStartWindow - time.Second)
	if rr, _ := r.reconcile().Find(role); rr.Action != ActionPending {
		t.Fatalf("report %+v", rr)
	}
	r.advance(time.Second + time.Nanosecond)
	rr, _ := r.reconcile().Find(role)
	if rr.Action != ActionStopped {
		t.Fatalf("report %+v", rr)
	}
	// Zero still means the default when the option is not set.
	noErr(t, r.withOptions(func(o *Options) {}))
	if r.ctl.margin != DefaultLateStartMargin {
		t.Fatalf("margin %v", r.ctl.margin)
	}
}
