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

func TestCursorAuthenticatedMalformedLengths(t *testing.T) {
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
	for _, scenario := range []string{"cancelled", "consumer failure"} {
		t.Run(scenario, func(t *testing.T) {
			a.mu.Lock()
			before := a.used
			a.mu.Unlock()
			tok, e := codec.mint(i, eventsCursor{"v", "p", "q"}, 1, 1, 1)
			if e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithCancel(context.Background())
			var consumeErr error
			if scenario == "cancelled" {
				cancel()
				consumeErr = ctx.Err()
			} else {
				consumeErr = errors.New("consumer failed")
				cancel()
			}
			if consumeErr == nil || tok == "" {
				t.Fatal("consumer did not abandon")
			}
			a.mu.Lock()
			after := a.used
			a.mu.Unlock()
			if after != before+1 {
				t.Fatal(before, after)
			}
		})
	}
}
