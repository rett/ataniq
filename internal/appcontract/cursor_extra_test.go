package appcontract

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
)

func TestCursorIDFieldsAndBindings(t *testing.T) {
	i := Instance{Authority: "a"}
	values := []struct {
		name, value string
		valid       bool
	}{
		{"empty", "", false}, {"one", "a", true}, {"max", strings.Repeat("x", 128), true},
		{"over", strings.Repeat("x", 129), false}, {"punctuation", "A0._:-", true}, {"space", "a b", false}, {"utf8", "é", false},
	}
	for _, field := range []string{"authority", "events viewer", "project", "conversation", "causes viewer", "causes project", "causes conversation", "causes authority", "client", "cause command", "admin viewer", "actor", "admin command"} {
		for _, v := range values {
			t.Run(field+"/"+v.name, func(t *testing.T) {
				inst := i
				var c cursorContext
				switch field {
				case "authority":
					inst.Authority = v.value
					c = eventsCursor{"v", "p", "q"}
				case "events viewer":
					c = eventsCursor{v.value, "p", "q"}
				case "project":
					c = eventsCursor{"v", v.value, "q"}
				case "conversation":
					c = eventsCursor{"v", "p", v.value}
				case "admin viewer":
					c = adminCursor{v.value, "actor", "cmd"}
				case "actor":
					c = adminCursor{"v", v.value, "cmd"}
				case "admin command":
					c = adminCursor{"v", "actor", v.value}
				default:
					x := causesCursor{viewer: "v", project: "p", conversation: "q", authority: "a", client: "c", command: "cmd", scope: "enqueue", head: 1}
					switch field {
					case "causes viewer":
						x.viewer = v.value
					case "causes project":
						x.project = v.value
					case "causes conversation":
						x.conversation = v.value
					case "causes authority":
						x.authority = v.value
						inst.Authority = v.value
					case "client":
						x.client = v.value
					case "cause command":
						x.command = v.value
					}
					c = x
				}
				_, err := cursorDigest(inst, c)
				if (err == nil) != v.valid {
					t.Fatalf("valid=%v err=%v", v.valid, err)
				}
			})
		}
	}
	codec, _ := newCursorCodec([32]byte{1})
	for _, tc := range []struct {
		name     string
		c, other cursorContext
		gen      uint64
	}{
		{"admin viewer", adminCursor{"v", "a", "c"}, adminCursor{"w", "a", "c"}, 0},
		{"admin actor", adminCursor{"v", "a", "c"}, adminCursor{"v", "b", "c"}, 0},
		{"admin command", adminCursor{"v", "a", "c"}, adminCursor{"v", "a", "d"}, 0},
		{"conversation", eventsCursor{"v", "p", "q"}, eventsCursor{"v", "p", "r"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tok, e := codec.mint(i, tc.c, 1, tc.gen, 1)
			if e != nil {
				t.Fatal(e)
			}
			if p, e := codec.decode(i, tc.other, tok, tc.gen, 1); e == nil || p != 0 {
				t.Fatal(p, e)
			}
		})
	}
	for _, mutate := range []func(*Instance){func(x *Instance) { x.Authority = "b" }, func(x *Instance) { x.StoreID[0]++ }, func(x *Instance) { x.IncarnationID[0]++ }} {
		c := eventsCursor{"v", "p", "q"}
		tok, e := codec.mint(i, c, 1, 1, 1)
		if e != nil {
			t.Fatal(e)
		}
		other := i
		mutate(&other)
		if p, e := codec.decode(other, c, tok, 1, 1); e == nil || p != 0 {
			t.Fatal(p, e)
		}
	}
}

func TestCursorBoundsMatrix(t *testing.T) {
	for _, scope := range []string{"enrollment", "enqueue"} {
		c := causesCursor{viewer: "v", project: "p", conversation: "q", authority: "a", client: "c", command: "x", scope: scope, ack: 2, head: 4}
		for _, q := range []struct {
			pos, gen, tail uint64
			ok             bool
		}{{2, 0, 4, true}, {4, 0, 4, true}, {1, 0, 4, false}, {5, 0, 4, false}, {2, 1, 4, false}, {2, 0, 3, false}, {2, 0, math.MaxUint64, false}} {
			if got := cursorBounds(c, q.pos, q.gen, q.tail); got != q.ok {
				t.Fatalf("%s %+v", scope, q)
			}
		}
	}
	for _, c := range []cursorContext{eventsCursor{"v", "p", "q"}, adminCursor{"v", "a", "c"}} {
		gen := uint64(0)
		if _, ok := c.(eventsCursor); ok {
			gen = 1
		}
		for _, q := range []struct {
			pos, gen, tail uint64
			ok             bool
		}{{1, gen, 1, true}, {math.MaxInt64, gen, math.MaxInt64, true}, {0, gen, 1, false}, {2, gen, 1, false}, {math.MaxInt64 + 1, gen, math.MaxInt64, false}, {1, gen, math.MaxInt64 + 1, false}, {1, math.MaxInt64 + 1, 1, false}} {
			if got := cursorBounds(c, q.pos, q.gen, q.tail); got != q.ok {
				t.Fatalf("%T %+v", c, q)
			}
		}
	}
	for _, gen := range []uint64{1, math.MaxInt64} {
		if !cursorBounds(eventsCursor{}, 1, gen, 1) {
			t.Fatal(gen)
		}
	}
	for _, h := range []uint64{math.MaxInt64, math.MaxInt64 + 1} {
		c := causesCursor{viewer: "v", project: "p", conversation: "q", authority: "a", client: "c", command: "x", scope: "enqueue", ack: h, head: h}
		_, e := cursorDigest(Instance{Authority: "a"}, c)
		if (e == nil) != (h == math.MaxInt64) {
			t.Fatal(h, e)
		}
	}
}

func TestCursorDecoderMalformedAndOwnership(t *testing.T) {
	i := Instance{Authority: "a", StoreID: [16]byte{1}, IncarnationID: [16]byte{2}}
	c := causesCursor{viewer: "v", project: "p", conversation: "q", authority: "a", client: "c", command: "x", anchor: [32]byte{3}, scope: "enqueue", ack: 1, head: 2}
	master := [32]byte{4}
	codec, e := newCursorCodec(master)
	if e != nil {
		t.Fatal(e)
	}
	oldI, oldC := i, c
	tok, e := codec.mint(i, c, 1, 0, 2)
	if e != nil {
		t.Fatal(e)
	}
	master[0]++
	i.StoreID[0]++
	i.IncarnationID[0]++
	c.anchor[0]++
	c.viewer = "other"
	if p, e := codec.decode(oldI, oldC, tok, 0, 2); e != nil || p != 1 {
		t.Fatal(p, e)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(tok)
	for n := range raw {
		b := append([]byte(nil), raw...)
		b[n] ^= 1
		if p, e := codec.decode(oldI, oldC, base64.RawURLEncoding.EncodeToString(b), 0, 2); e == nil || p != 0 {
			t.Fatalf("byte %d: %d %v", n, p, e)
		}
	}
	alpha := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	last := strings.IndexByte(alpha, tok[len(tok)-1])
	noncanonical := tok[:len(tok)-1] + string(alpha[last+1])
	for name, s := range map[string]string{"unused bits": noncanonical, "padding": tok + "=", "newline": tok[:40] + "\n" + tok[41:], "short": tok[:145], "long": tok + "A", "alphabet": tok[:20] + "+" + tok[21:]} {
		t.Run(name, func(t *testing.T) {
			if p, e := codec.decode(oldI, oldC, s, 0, 2); e == nil || p != 0 {
				t.Fatal(p, e)
			}
		})
	}
	var zero cursorCodec
	var nilCodec *cursorCodec
	for _, x := range []*cursorCodec{&zero, nilCodec} {
		if _, e := x.mint(oldI, oldC, 1, 0, 2); e == nil {
			t.Fatal("zero mint")
		}
		if _, e := x.decode(oldI, oldC, tok, 0, 2); e == nil {
			t.Fatal("zero decode")
		}
	}
	copyCodec := *codec
	if p, e := copyCodec.decode(oldI, oldC, tok, 0, 2); e != nil || p != 1 {
		t.Fatal(p, e)
	}
	if _, e := copyCodec.mint(oldI, oldC, 1, 0, 2); e != nil {
		t.Fatal(e)
	}
}

func TestCursorRejectsMalformedFrameLengthsBeforeAuthentication(t *testing.T) {
	i := Instance{Authority: "a"}
	c := eventsCursor{"v", "p", "q"}
	codec, _ := newCursorCodec([32]byte{1})
	d, _ := cursorDigest(i, c)
	epoch, _ := codec.allocator.reserve()
	key, _ := cursorKey(codec.master, i, d, epoch[:])
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	header := append([]byte{2}, epoch[:]...)
	nonce := make([]byte, gcm.NonceSize())
	// Exact frame length with real GCM fixes the plaintext length at 48;
	// the post-authentication plaintext-length defence is unreachable here.
	for _, n := range []int{0, 47, 49} {
		plain := make([]byte, n)
		raw := append(append(append([]byte(nil), header...), nonce...), gcm.Seal(nil, nonce, plain, cursorAAD(i, d, header))...)
		if p, e := codec.decode(i, c, base64.RawURLEncoding.EncodeToString(raw), 1, 1); e == nil || p != 0 {
			t.Fatal(n, p, e)
		}
	}
}

func TestCursorCauseBindingsAndConstructionFailure(t *testing.T) {
	i := Instance{Authority: "a"}
	c := causesCursor{viewer: "v", project: "p", conversation: "q", authority: "a", client: "c", command: "x", anchor: [32]byte{1}, scope: "enrollment", ack: 1, head: 2}
	codec, err := newCursorCodec([32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	token, err := codec.mint(i, c, 1, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*causesCursor){
		"viewer": func(x *causesCursor) { x.viewer = "w" }, "project": func(x *causesCursor) { x.project = "r" },
		"conversation": func(x *causesCursor) { x.conversation = "r" }, "client": func(x *causesCursor) { x.client = "d" },
		"command": func(x *causesCursor) { x.command = "y" }, "anchor": func(x *causesCursor) { x.anchor[31]++ },
		"scope": func(x *causesCursor) { x.scope = "enqueue" }, "ack": func(x *causesCursor) { x.ack = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			other := c
			change(&other)
			if p, e := codec.decode(i, other, token, 0, 2); e == nil || p != 0 {
				t.Fatal(p, e)
			}
		})
	}
	for _, makeAEAD := range []func([]byte) (cipher.AEAD, error){func([]byte) (cipher.AEAD, error) { return nil, errors.New("construction") }, func([]byte) (cipher.AEAD, error) { return nil, nil }} {
		codec.makeAEAD = makeAEAD
		codec.allocator.mu.Lock()
		before := codec.allocator.used
		codec.allocator.mu.Unlock()
		if _, e := codec.mint(i, c, 1, 0, 2); e == nil {
			t.Fatal("mint accepted construction failure")
		}
		codec.allocator.mu.Lock()
		after := codec.allocator.used
		codec.allocator.mu.Unlock()
		if after != before+1 {
			t.Fatal(before, after)
		}
		if p, e := codec.decode(i, c, token, 0, 2); e == nil || p != 0 {
			t.Fatal(p, e)
		}
	}
}

func TestCursorConcurrentBoundaryAndAbandonment(t *testing.T) {
	var entropy byte
	a, e := newCursorAllocatorWithEntropy(func(out *[32]byte) error { entropy++; out[0] = entropy; return nil })
	if e != nil {
		t.Fatal(e)
	}
	a.mu.Lock()
	a.used = cursorEpochLimit - 30
	a.mu.Unlock()
	codec := &cursorCodec{allocator: a, makeAEAD: cursorAEAD}
	i := Instance{Authority: "a"}
	const count = 90
	results := make(chan byte, count)
	var wg sync.WaitGroup
	for j := 0; j < count; j++ {
		wg.Add(1)
		go func(j int) {
			defer wg.Done()
			var c cursorContext
			gen := uint64(0)
			switch j % 3 {
			case 0:
				c = eventsCursor{"v", "p", "q"}
				gen = 1
			case 1:
				c = adminCursor{"v", "a", "c"}
			case 2:
				c = causesCursor{viewer: "v", project: "p", conversation: "q", authority: "a", client: "c", command: "x", scope: "enqueue", head: 1}
			}
			tok, e := codec.mint(i, c, 1, gen, 1)
			if e != nil {
				t.Error(e)
				return
			}
			raw, _ := base64.RawURLEncoding.DecodeString(tok)
			results <- raw[1]
		}(j)
	}
	wg.Wait()
	close(results)
	counts := map[byte]int{}
	for epoch := range results {
		counts[epoch]++
	}
	a.mu.Lock()
	used, epoch := a.used, a.epoch
	a.mu.Unlock()
	if counts[1] != 30 || counts[2] != 60 || len(counts) != 2 || used != 60 || epoch[0] != 2 {
		t.Fatal(counts, used, epoch)
	}
}

// These bounded consumers simulate abandonment; service cancellation and durable
// receipt integration are not qualified by this test.
func TestCursorAbandonedLastReservation(t *testing.T) {
	consumerFailure := errors.New("consumer failed")
	for _, tc := range []struct {
		name    string
		consume func(context.Context, string) error
		want    error
	}{
		{"cancelled", func(ctx context.Context, token string) error {
			if token == "" {
				return errors.New("missing token")
			}
			return ctx.Err()
		}, context.Canceled},
		{"consumer failure", func(_ context.Context, token string) error {
			if token == "" {
				return errors.New("missing token")
			}
			return consumerFailure
		}, consumerFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var entropy byte
			a, err := newCursorAllocatorWithEntropy(func(out *[32]byte) error { entropy++; out[0] = entropy; return nil })
			if err != nil {
				t.Fatal(err)
			}
			codec := &cursorCodec{master: [32]byte{1}, allocator: a, makeAEAD: cursorAEAD}
			i, c := Instance{Authority: "a"}, eventsCursor{"v", "p", "q"}
			a.used = cursorEpochLimit - 1
			oldEpoch := a.epoch
			token, err := codec.mint(i, c, 1, 1, 1)
			if err != nil || a.used != cursorEpochLimit {
				t.Fatal(err, a.used)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := tc.consume(ctx, token); !errors.Is(err, tc.want) {
				t.Fatal(err)
			}
			fresh, err := codec.mint(i, c, 1, 1, 1)
			if err != nil || a.epoch == oldEpoch || a.used != 1 {
				t.Fatal(err, a.epoch, a.used)
			}
			raw, err := base64.RawURLEncoding.DecodeString(fresh)
			if err != nil || string(raw[1:33]) != string(a.epoch[:]) {
				t.Fatal("fresh epoch", err)
			}
			if p, err := codec.decode(i, c, token, 1, 1); err != nil || p != 1 {
				t.Fatal(p, err)
			}
		})
	}
	t.Run("construction failure", func(t *testing.T) {
		var entropy byte
		a, err := newCursorAllocatorWithEntropy(func(out *[32]byte) error { entropy++; out[0] = entropy; return nil })
		if err != nil {
			t.Fatal(err)
		}
		codec := &cursorCodec{allocator: a, makeAEAD: func([]byte) (cipher.AEAD, error) { return nil, errors.New("construction") }}
		i, c := Instance{Authority: "a"}, eventsCursor{"v", "p", "q"}
		a.used = cursorEpochLimit - 1
		oldEpoch := a.epoch
		if token, err := codec.mint(i, c, 1, 1, 1); err == nil || token != "" || a.used != cursorEpochLimit {
			t.Fatal(token, err, a.used)
		}
		codec.makeAEAD = cursorAEAD
		token, err := codec.mint(i, c, 1, 1, 1)
		if err != nil || a.epoch == oldEpoch || a.used != 1 {
			t.Fatal(err, a.epoch, a.used)
		}
		raw, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil || string(raw[1:33]) != string(a.epoch[:]) {
			t.Fatal("fresh epoch", err)
		}
	})
}

func TestCursorRestartDoesNotAdoptReceivedEpoch(t *testing.T) {
	master := [32]byte{1}
	i, c := Instance{Authority: "a"}, eventsCursor{"v", "p", "q"}
	a, err := newCursorCodec(master)
	if err != nil {
		t.Fatal(err)
	}
	token, err := a.mint(i, c, 1, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	b, err := newCursorCodec(master)
	if err != nil {
		t.Fatal(err)
	}
	epoch := b.allocator.epoch
	if epoch == a.allocator.epoch || b.allocator.used != 0 {
		t.Fatal("restart epoch or count")
	}
	if p, err := b.decode(i, c, token, 1, 1); err != nil || p != 1 {
		t.Fatal(p, err)
	}
	if b.allocator.epoch != epoch || b.allocator.used != 0 {
		t.Fatal("received epoch adopted")
	}
	fresh, err := b.mint(i, c, 1, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(fresh)
	if err != nil || string(raw[1:33]) != string(epoch[:]) || b.allocator.epoch != epoch || b.allocator.used != 1 {
		t.Fatal("restart mint", err)
	}
}

func TestCursorRotationFailureRecovery(t *testing.T) {
	var calls byte
	fail := false
	a, err := newCursorAllocatorWithEntropy(func(out *[32]byte) error {
		calls++
		out[0] = calls
		if fail {
			return errors.New("entropy unavailable")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	codec := &cursorCodec{master: [32]byte{1}, allocator: a, makeAEAD: cursorAEAD}
	i, c := Instance{Authority: "a"}, eventsCursor{"v", "p", "q"}
	a.used = cursorEpochLimit - 1
	old, err := codec.mint(i, c, 1, 1, 1)
	if err != nil || a.used != cursorEpochLimit {
		t.Fatal(err, a.used)
	}
	oldEpoch := a.epoch
	fail = true
	if epoch, err := a.reserve(); err == nil || epoch != ([32]byte{}) {
		t.Fatal(epoch, err)
	}
	if token, err := codec.mint(i, c, 1, 1, 1); err == nil || token != "" {
		t.Fatal(token, err)
	}
	if a.epoch != oldEpoch || a.used != cursorEpochLimit || calls != 3 {
		t.Fatal(a.epoch, a.used, calls)
	}
	fail = false
	fresh, err := codec.mint(i, c, 1, 1, 1)
	if err != nil || a.used != 1 || a.epoch == oldEpoch || calls != 4 {
		t.Fatal(err, a.epoch, a.used, calls)
	}
	rawOld, err := base64.RawURLEncoding.DecodeString(old)
	if err != nil {
		t.Fatal(err)
	}
	rawFresh, err := base64.RawURLEncoding.DecodeString(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if string(rawOld[1:33]) != string(oldEpoch[:]) || string(rawFresh[1:33]) != string(a.epoch[:]) {
		t.Fatal("wrong reserved epoch")
	}
	if p, err := codec.decode(i, c, old, 1, 1); err != nil || p != 1 || a.used != 1 {
		t.Fatal(p, err, a.used)
	}
	if _, err := codec.mint(i, c, 1, 1, 1); err != nil || a.used != 2 || calls != 4 {
		t.Fatal(err, a.used, calls)
	}
}

func TestCursorProductionExtremeBounds(t *testing.T) {
	i := Instance{Authority: "a"}
	codec, err := newCursorCodec([32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		c    cursorContext
		gen  uint64
	}{
		{"events", eventsCursor{"v", "p", "q"}, math.MaxInt64},
		{"admin", adminCursor{"v", "a", "c"}, 0},
		{"causes enrollment", causesCursor{viewer: "v", project: "p", conversation: "q", authority: "a", client: "c", command: "x", scope: "enrollment", ack: math.MaxInt64, head: math.MaxInt64}, 0},
		{"causes enqueue", causesCursor{viewer: "v", project: "p", conversation: "q", authority: "a", client: "c", command: "x", scope: "enqueue", ack: math.MaxInt64, head: math.MaxInt64}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token, err := codec.mint(i, tc.c, math.MaxInt64, tc.gen, math.MaxInt64)
			if err != nil {
				t.Fatal(err)
			}
			if p, err := codec.decode(i, tc.c, token, tc.gen, math.MaxInt64); err != nil || p != math.MaxInt64 {
				t.Fatal(p, err)
			}
			for _, v := range []struct{ pos, gen, tail uint64 }{
				{math.MaxInt64 + 1, tc.gen, math.MaxInt64}, {math.MaxInt64, math.MaxInt64 + 1, math.MaxInt64},
				{math.MaxInt64, tc.gen, math.MaxInt64 + 1}, {math.MaxInt64, tc.gen, math.MaxInt64 - 1},
			} {
				if tok, err := codec.mint(i, tc.c, v.pos, v.gen, v.tail); err == nil || tok != "" {
					t.Fatal(v, tok, err)
				}
			}
			for _, v := range []struct{ gen, tail uint64 }{{math.MaxInt64 + 1, math.MaxInt64}, {tc.gen, math.MaxInt64 + 1}, {tc.gen, math.MaxInt64 - 1}} {
				if p, err := codec.decode(i, tc.c, token, v.gen, v.tail); err == nil || p != 0 {
					t.Fatal(v, p, err)
				}
			}
			wrongGen := uint64(1)
			if tc.gen != 0 {
				wrongGen = 0
			}
			if tok, err := codec.mint(i, tc.c, math.MaxInt64, wrongGen, math.MaxInt64); err == nil || tok != "" {
				t.Fatal(tok, err)
			}
			if p, err := codec.decode(i, tc.c, token, wrongGen, math.MaxInt64); err == nil || p != 0 {
				t.Fatal(p, err)
			}
		})
	}
}

func TestCursorCausesZeroInterval(t *testing.T) {
	codec, err := newCursorCodec([32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"enrollment", "enqueue"} {
		t.Run(scope, func(t *testing.T) {
			i := Instance{Authority: "a"}
			c := causesCursor{viewer: "v", project: "p", conversation: "q", authority: "a", client: "c", command: "x", scope: scope}
			token, err := codec.mint(i, c, 0, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			if p, err := codec.decode(i, c, token, 0, 0); err != nil || p != 0 {
				t.Fatal(p, err)
			}
		})
	}
}

func TestCursorCopiedCodecSharesReservations(t *testing.T) {
	var calls byte
	a, err := newCursorAllocatorWithEntropy(func(out *[32]byte) error { calls++; out[0] = calls; return nil })
	if err != nil {
		t.Fatal(err)
	}
	original := cursorCodec{master: [32]byte{1}, allocator: a, makeAEAD: cursorAEAD}
	copyCodec := original
	if copyCodec.allocator != original.allocator {
		t.Fatal("allocator not shared")
	}
	a.used = cursorEpochLimit - 2
	i, c := Instance{Authority: "a"}, eventsCursor{"v", "p", "q"}
	for j, codec := range []*cursorCodec{&original, &copyCodec, &original, &copyCodec} {
		token, err := codec.mint(i, c, 1, 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil {
			t.Fatal(err)
		}
		wantEpoch, wantUsed := byte(1), uint64(cursorEpochLimit-1+j)
		if j >= 2 {
			wantEpoch, wantUsed = 2, uint64(j-1)
		}
		if raw[1] != wantEpoch || a.epoch[0] != wantEpoch || a.used != wantUsed || calls != wantEpoch {
			t.Fatal(j, raw[1], a.epoch, a.used, calls)
		}
	}
}
