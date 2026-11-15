package appcontract

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"strings"
	"sync"
	"testing"
)

type cursorVector struct {
	Input struct {
		Viewer, Kind, Project, Conversation string
		Position, Generation                uint64
		Epoch                               string `json:"epoch_hex"`
		Nonce                               string `json:"nonce_hex"`
		Extra                               json.RawMessage
	}
	ContextPreimage struct {
		Hex    string
		Length int
		SHA256 string
	} `json:"context_preimage"`
	Context    string `json:"context_hex"`
	Info       string `json:"info_hex"`
	Key        string `json:"key_hex"`
	Header     string `json:"header_hex"`
	AAD        string `json:"aad_hex"`
	Plaintext  string `json:"plaintext_hex"`
	Ciphertext string `json:"ciphertext_hex"`
	Tag        string `json:"tag_hex"`
	Frame      string `json:"frame_hex"`
	Token      string `json:"frame_base64url"`
}

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, e := hex.DecodeString(s)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func expectHex(t *testing.T, name string, b []byte, want string) {
	t.Helper()
	if hex.EncodeToString(b) != want {
		t.Fatalf("%s mismatch", name)
	}
}
func TestCursorVectors(t *testing.T) {
	b, err := os.ReadFile("testdata/cursors-v2.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Shared struct {
			Authority   string
			Store       string `json:"store_id_hex"`
			Incarnation string `json:"incarnation_id_hex"`
			Master      string `json:"master_hex"`
		}
		Cursors map[string]cursorVector
	}
	if err = json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Cursors) != 7 {
		t.Fatal(len(f.Cursors))
	}
	i := Instance{Authority: f.Shared.Authority}
	copy(i.StoreID[:], unhex(t, f.Shared.Store))
	copy(i.IncarnationID[:], unhex(t, f.Shared.Incarnation))
	var master [32]byte
	copy(master[:], unhex(t, f.Shared.Master))
	codec, err := newCursorCodec(master)
	if err != nil {
		t.Fatal(err)
	}
	for name, v := range f.Cursors {
		t.Run(name, func(t *testing.T) {
			var c cursorContext
			tail := v.Input.Position
			switch v.Input.Kind {
			case "events-queue":
				c = eventsCursor{v.Input.Viewer, v.Input.Project, v.Input.Conversation}
			case "admin-lookup":
				var x struct{ Actor, Command string }
				if err := json.Unmarshal(v.Input.Extra, &x); err != nil {
					t.Fatal(err)
				}
				c = adminCursor{v.Input.Viewer, x.Actor, x.Command}
			case "causes":
				var x struct {
					TargetAuthority string `json:"target_authority"`
					TargetClient    string `json:"target_client"`
					TargetCommand   string `json:"target_command"`
					Anchor          string `json:"anchored_status"`
					Scope           string
					Ack, Head       uint64
				}
				if err := json.Unmarshal(v.Input.Extra, &x); err != nil {
					t.Fatal(err)
				}
				var anchor [32]byte
				copy(anchor[:], unhex(t, x.Anchor))
				c = causesCursor{v.Input.Viewer, v.Input.Project, v.Input.Conversation, x.TargetAuthority, x.TargetClient, x.TargetCommand, anchor, x.Scope, x.Ack, x.Head}
				tail = x.Head
			default:
				t.Fatal(v.Input.Kind)
			}
			fields, err := c.cursorFields(i)
			if err != nil {
				t.Fatal(err)
			}
			pre := frame("CURSOR-CONTEXT", append(i.fields(), fields...)...)
			expectHex(t, "context preimage", pre, v.ContextPreimage.Hex)
			if len(pre) != v.ContextPreimage.Length {
				t.Fatal("preimage length")
			}
			sum := sha256.Sum256(pre)
			expectHex(t, "context hash", sum[:], v.Context)
			expectHex(t, "preimage hash", sum[:], v.ContextPreimage.SHA256)
			info := frame("CURSOR-KEY-V2", append(i.fields(), sum[:])...)
			expectHex(t, "info", info, v.Info)
			epoch := unhex(t, v.Input.Epoch)
			key, err := cursorKey(master, i, sum, epoch)
			if err != nil {
				t.Fatal(err)
			}
			expectHex(t, "key", key, v.Key)
			header := append([]byte{2}, epoch...)
			expectHex(t, "header", header, v.Header)
			aad := cursorAAD(i, sum, header)
			expectHex(t, "aad", aad, v.AAD)
			plain := append(number(v.Input.Position), number(v.Input.Generation)...)
			plain = append(plain, sum[:]...)
			expectHex(t, "plaintext", plain, v.Plaintext)
			block, err := aes.NewCipher(key)
			if err != nil {
				t.Fatal(err)
			}
			gcm, err := cipher.NewGCM(block)
			if err != nil {
				t.Fatal(err)
			}
			encrypted := gcm.Seal(nil, unhex(t, v.Input.Nonce), plain, aad)
			expectHex(t, "ciphertext", encrypted[:48], v.Ciphertext)
			expectHex(t, "tag", encrypted[48:], v.Tag)
			raw := append(header, unhex(t, v.Input.Nonce)...)
			raw = append(raw, encrypted...)
			expectHex(t, "frame", raw, v.Frame)
			if base64.RawURLEncoding.EncodeToString(raw) != v.Token {
				t.Fatal("token")
			}
			pos, err := codec.decode(i, c, v.Token, v.Input.Generation, tail)
			if err != nil || pos != v.Input.Position {
				t.Fatalf("decode %d %v", pos, err)
			}
		})
	}
}

func TestCursorProductionAndBounds(t *testing.T) {
	i := Instance{Authority: "a"}
	c := eventsCursor{"viewer", "project", "conversation"}
	codec, err := newCursorCodec([32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	token, err := codec.mint(i, c, 7, 1, 9)
	if err != nil {
		t.Fatal(err)
	}
	other, err := codec.mint(i, c, 7, 1, 9)
	if err != nil || token == other {
		t.Fatal("random nonce", err)
	}
	if p, e := codec.decode(i, c, token, 1, 9); e != nil || p != 7 {
		t.Fatal(p, e)
	}
	for _, s := range []string{"", token + "=", token + "\n", token[:145], strings.Repeat("A", 100000), token[:145] + "B"} {
		if _, e := codec.decode(i, c, s, 1, 9); e == nil {
			t.Fatal("accepted malformed token")
		}
	}
	raw, _ := base64.RawURLEncoding.DecodeString(token)
	for _, n := range []int{0, 1, 33, 45, 108} {
		b := append([]byte(nil), raw...)
		b[n] ^= 1
		if _, e := codec.decode(i, c, base64.RawURLEncoding.EncodeToString(b), 1, 9); e == nil {
			t.Fatal("accepted mutation", n)
		}
	}
	for _, q := range []struct {
		i         Instance
		c         cursorContext
		gen, tail uint64
	}{{Instance{Authority: "b"}, c, 1, 9}, {i, eventsCursor{"other", "project", "conversation"}, 1, 9}, {i, eventsCursor{"viewer", "other", "conversation"}, 1, 9}, {i, adminCursor{"viewer", "actor", "cmd"}, 0, 9}, {i, c, 2, 9}, {i, c, 1, 6}, {i, c, 1, math.MaxUint64}} {
		if _, e := codec.decode(q.i, q.c, token, q.gen, q.tail); e == nil {
			t.Fatal("accepted wrong context/bounds")
		}
	}
	if _, e := codec.mint(i, c, math.MaxUint64, 1, math.MaxInt64); e == nil {
		t.Fatal("overflow")
	}
	if _, e := codec.mint(i, c, 1, 0, 9); e == nil {
		t.Fatal("generation")
	}
	if _, e := codec.mint(i, causesCursor{viewer: "v", project: "p", conversation: "q", authority: "a", client: "c", command: "x", scope: "enrollment", ack: 2, head: 1}, 0, 0, 1); e == nil {
		t.Fatal("bad anchor")
	}
}

func TestCursorAuthenticatedInvalidPlaintext(t *testing.T) {
	i := Instance{Authority: "a"}
	c := eventsCursor{"viewer", "p", "q"}
	codec, err := newCursorCodec([32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	digest, err := cursorDigest(i, c)
	if err != nil {
		t.Fatal(err)
	}
	epoch, err := codec.allocator.reserve()
	if err != nil {
		t.Fatal(err)
	}
	key, err := cursorKey(codec.master, i, digest, epoch[:])
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cursorAEAD(key)
	if err != nil {
		t.Fatal(err)
	}
	header := append([]byte{2}, epoch[:]...)
	for _, p := range []struct {
		position, generation uint64
		wrongContext         bool
	}{{0, 1, false}, {math.MaxUint64, 1, false}, {1, math.MaxUint64, false}, {1, 1, true}} {
		plain := append(number(p.position), number(p.generation)...)
		plain = append(plain, digest[:]...)
		if p.wrongContext {
			plain[16] ^= 1
		}
		raw := append(append([]byte(nil), header...), aead.Seal(nil, nil, plain, cursorAAD(i, digest, header))...)
		if pos, e := codec.decode(i, c, base64.RawURLEncoding.EncodeToString(raw), 1, math.MaxInt64); e == nil || pos != 0 {
			t.Fatal("accepted invalid authenticated plaintext", p)
		}
	}
}

func TestCursorReservations(t *testing.T) {
	var n byte
	entropy := func(e *[32]byte) error { n++; e[0] = n; return nil }
	a, err := newCursorAllocatorWithEntropy(entropy)
	if err != nil {
		t.Fatal(err)
	}
	a.used = cursorEpochLimit - 1
	old, err := a.reserve()
	if err != nil || old[0] != 1 {
		t.Fatal(old, err)
	}
	fresh, err := a.reserve()
	if err != nil || fresh[0] != 2 || a.used != 1 {
		t.Fatal(fresh, err)
	}
	// Simulate a value copy without copying a mutex that has already been used.
	copyA := cursorAllocator{self: a.self, epoch: a.epoch, used: a.used, entropy: a.entropy}
	if _, err := copyA.reserve(); err == nil {
		t.Fatal("copied allocator")
	}
	var zero cursorAllocator
	if _, err := zero.reserve(); err == nil {
		t.Fatal("zero allocator")
	}
	var wg sync.WaitGroup
	const count = 200
	for j := 0; j < count; j++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := a.reserve(); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if a.used != count+1 {
		t.Fatal(a.used)
	}
	i := Instance{Authority: "a"}
	c := eventsCursor{"v", "p", "q"}
	codec := &cursorCodec{allocator: a, makeAEAD: cursorAEAD}
	token, err := codec.mint(i, c, 1, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	before := a.used
	codec.makeAEAD = func([]byte) (cipher.AEAD, error) { return nil, errors.New("failure") }
	if _, err = codec.mint(i, c, 1, 1, 1); err == nil || a.used != before+1 {
		t.Fatal("reservation not burned")
	}
	codec.makeAEAD = cursorAEAD
	a.used = cursorEpochLimit
	if _, err = a.reserve(); err != nil {
		t.Fatal(err)
	}
	epoch := a.epoch
	used := a.used
	if p, e := codec.decode(i, c, token, 1, 1); e != nil || p != 1 || a.epoch != epoch || a.used != used {
		t.Fatal("old token or adoption", p, e)
	}
	bad := func(*[32]byte) error { return errors.New("entropy") }
	if _, e := newCursorAllocatorWithEntropy(bad); e == nil {
		t.Fatal("construction failure")
	}
	a.entropy = bad
	a.used = cursorEpochLimit
	if _, e := a.reserve(); e == nil || a.used != cursorEpochLimit || a.epoch != epoch {
		t.Fatal("rotation failure")
	}
}

func TestCursorDelayedAndConcurrentContexts(t *testing.T) {
	var next byte
	a, err := newCursorAllocatorWithEntropy(func(e *[32]byte) error { next++; e[0] = next; return nil })
	if err != nil {
		t.Fatal(err)
	}
	i := Instance{Authority: "a"}
	started, release := make(chan struct{}), make(chan struct{})
	codec := &cursorCodec{allocator: a, makeAEAD: func(key []byte) (cipher.AEAD, error) { close(started); <-release; return cursorAEAD(key) }}
	result := make(chan string, 1)
	go func() {
		token, e := codec.mint(i, eventsCursor{"v", "p", "q"}, 1, 1, 1)
		if e != nil {
			t.Error(e)
		}
		result <- token
	}()
	<-started
	a.mu.Lock()
	a.used = cursorEpochLimit
	a.mu.Unlock()
	if epoch, e := a.reserve(); e != nil || epoch[0] != 2 {
		t.Fatal(epoch, e)
	}
	close(release)
	token := <-result
	codec.makeAEAD = cursorAEAD
	raw, e := base64.RawURLEncoding.DecodeString(token)
	if e != nil || raw[1] != 1 {
		t.Fatal("delayed reservation lost epoch", e)
	}
	if pos, e := codec.decode(i, eventsCursor{"v", "p", "q"}, token, 1, 1); e != nil || pos != 1 {
		t.Fatal(pos, e)
	}
	before := a.used
	var wg sync.WaitGroup
	for j := 0; j < 120; j++ {
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
				c = adminCursor{"v", "actor", "command"}
			case 2:
				c = causesCursor{viewer: "v", project: "p", conversation: "q", authority: "a", client: "c", command: "cmd", scope: "enqueue", head: 1}
			}
			if _, e := codec.mint(i, c, 1, gen, 1); e != nil {
				t.Error(e)
			}
		}(j)
	}
	wg.Wait()
	if a.used != before+120 {
		t.Fatal("global count", a.used)
	}
	first, e := newCursorCodec([32]byte{})
	if e != nil {
		t.Fatal(e)
	}
	second, e := newCursorCodec([32]byte{})
	if e != nil || first.allocator.epoch == second.allocator.epoch {
		t.Fatal("fresh composition", e)
	}
}
