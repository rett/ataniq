package seatpair

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

var t0 = time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)

// counting returns a reader of the bytes 0, 1, 2, ... so the code it makes is
// the first CodeLength characters of the alphabet.
func counting() *bytes.Reader {
	b := make([]byte, 256)
	for i := range b {
		b[i] = byte(i)
	}
	return bytes.NewReader(b)
}

const countingCode = "0123456789ABCDEFGHJK"

// freshCode makes a code at t0 from the system's random source.
func freshCode(t *testing.T) (string, Code) {
	t.Helper()
	code, c := NewCode(rand.Reader, t0)
	if code == "" {
		t.Fatal("NewCode gave no code")
	}
	return code, c
}

// wrongCode is a code of the right shape that differs from code.
func wrongCode(code string) string {
	if code[0] == '0' {
		return "1" + code[1:]
	}
	return "0" + code[1:]
}

func TestNewCodeShape(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		code, _ := freshCode(t)
		if len(code) != CodeLength || CodeLength != 20 {
			t.Fatalf("code %q has %d characters, want 20", code, len(code))
		}
		for _, r := range code {
			if !strings.ContainsRune(alphabet, r) {
				t.Fatalf("character %q of a code is not in the alphabet", r)
			}
			if strings.ContainsRune("ILOU", r) {
				t.Fatalf("character %q must not be in a code", r)
			}
		}
		if seen[code] {
			t.Fatalf("two codes alike: %q", code)
		}
		seen[code] = true
	}
}

func TestAlphabet(t *testing.T) {
	if len(alphabet) != 32 {
		t.Fatalf("alphabet has %d characters, want 32", len(alphabet))
	}
	seen := map[rune]bool{}
	for _, r := range alphabet {
		if seen[r] {
			t.Errorf("%q twice in the alphabet", r)
		}
		seen[r] = true
		if r != '0' && (r < '1' || r > '9') && (r < 'A' || r > 'Z') {
			t.Errorf("%q is not a digit or an upper-case letter", r)
		}
	}
	for _, r := range "ILOU" {
		if seen[r] {
			t.Errorf("%q must not be in the alphabet", r)
		}
	}
}

func TestNewCodeFromBytes(t *testing.T) {
	code, c := NewCode(counting(), t0)
	if code != countingCode {
		t.Fatalf("code = %q, want %q", code, countingCode)
	}
	if !c.Expires().Equal(t0.Add(10 * time.Minute)) {
		t.Errorf("expires %v, want %v", c.Expires(), t0.Add(10*time.Minute))
	}
	// Only the low five bits of a byte matter, so no character is likelier.
	high := bytes.Repeat([]byte{0xFF}, CodeLength)
	if code, _ := NewCode(bytes.NewReader(high), t0); code != strings.Repeat("Z", CodeLength) {
		t.Errorf("code of 0xFF bytes = %q", code)
	}
	mixed := make([]byte, CodeLength)
	for i := range mixed {
		mixed[i] = byte(i) | 0xE0
	}
	if code, _ := NewCode(bytes.NewReader(mixed), t0); code != countingCode {
		t.Errorf("code with high bits set = %q, want %q", code, countingCode)
	}
}

func TestNewCodeFailures(t *testing.T) {
	for name, r := range map[string]*bytes.Reader{
		"short": bytes.NewReader(make([]byte, CodeLength-1)),
		"empty": bytes.NewReader(nil),
	} {
		code, c := NewCode(r, t0)
		if code != "" || c != (Code{}) {
			t.Errorf("%s reader: got %q and %v", name, code, c)
		}
	}
	if code, c := NewCode(iotest.ErrReader(errors.New("no entropy")), t0); code != "" || c != (Code{}) {
		t.Errorf("failing reader: got %q and %v", code, c)
	}
	if code, c := NewCode(counting(), time.Time{}); code != "" || c != (Code{}) {
		t.Errorf("zero time: got %q and %v", code, c)
	}
}

func TestZeroCodeMatchesNothing(t *testing.T) {
	var c Code
	for _, p := range []string{"", countingCode, strings.Repeat("0", CodeLength)} {
		if err := c.Verify(p, t0); err != ErrWrongCode {
			t.Errorf("zero Code Verify(%q) = %v, want ErrWrongCode", p, err)
		}
	}
	if c.Tries() != 0 || c.Used() || c.Burnt() {
		t.Errorf("zero Code changed: %v", c)
	}
}

func TestVerifyRightCodeOnce(t *testing.T) {
	code, c := freshCode(t)
	if err := c.Verify(code, t0.Add(time.Minute)); err != nil {
		t.Fatalf("right code: %v", err)
	}
	if !c.Used() {
		t.Error("code not marked used")
	}
	if err := c.Verify(code, t0.Add(2*time.Minute)); err != ErrUsed {
		t.Errorf("second use = %v, want ErrUsed", err)
	}
	if err := c.Verify(wrongCode(code), t0.Add(2*time.Minute)); err != ErrUsed {
		t.Errorf("wrong code after use = %v, want ErrUsed", err)
	}
	if c.Tries() != 0 {
		t.Errorf("tries = %d after a used code was refused, want 0", c.Tries())
	}
}

func TestVerifyIsCaseInsensitive(t *testing.T) {
	_, c := NewCode(counting(), t0)
	if err := c.Verify(strings.ToLower(countingCode), t0); err != nil {
		t.Errorf("lower-case code: %v", err)
	}
}

func TestVerifyExpiryExactlyAtTenMinutes(t *testing.T) {
	for _, c := range []struct {
		name string
		at   time.Duration
		want error
	}{
		{"issue time", 0, nil},
		{"a nanosecond before", 10*time.Minute - time.Nanosecond, nil},
		{"exactly 10 minutes", 10 * time.Minute, ErrExpired},
		{"a nanosecond after", 10*time.Minute + time.Nanosecond, ErrExpired},
		{"an hour", time.Hour, ErrExpired},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, rec := freshCode(t)
			if err := rec.Verify(code, t0.Add(c.at)); err != c.want {
				t.Errorf("Verify at +%v = %v, want %v", c.at, err, c.want)
			}
		})
	}
}

func TestExpiredCodeCountsNoTry(t *testing.T) {
	code, c := freshCode(t)
	for i := 0; i < 10; i++ {
		if err := c.Verify(wrongCode(code), t0.Add(11*time.Minute)); err != ErrExpired {
			t.Fatalf("wrong code after expiry = %v, want ErrExpired", err)
		}
	}
	if c.Tries() != 0 || c.Burnt() {
		t.Errorf("tries = %d, burnt %v: an expired code counts none", c.Tries(), c.Burnt())
	}
}

func TestFiveWrongTriesBurnTheCode(t *testing.T) {
	code, c := freshCode(t)
	wrong := wrongCode(code)
	for i := 1; i <= MaxTries; i++ {
		if err := c.Verify(wrong, t0); err != ErrWrongCode {
			t.Fatalf("wrong try %d = %v, want ErrWrongCode", i, err)
		}
		if c.Tries() != i {
			t.Fatalf("tries = %d after wrong try %d", c.Tries(), i)
		}
	}
	if !c.Burnt() {
		t.Fatal("code not burnt after five wrong tries")
	}
	if err := c.Verify(wrong, t0); err != ErrBurnt {
		t.Errorf("sixth wrong try = %v, want ErrBurnt", err)
	}
	if c.Tries() != MaxTries {
		t.Errorf("tries = %d, a burnt code counts no more", c.Tries())
	}
}

func TestRightCodeAfterFiveWrongTriesIsBurnt(t *testing.T) {
	code, c := freshCode(t)
	for i := 0; i < MaxTries; i++ {
		_ = c.Verify(wrongCode(code), t0)
	}
	if err := c.Verify(code, t0); err != ErrBurnt {
		t.Errorf("right code after five wrong tries = %v, want ErrBurnt", err)
	}
	if c.Used() {
		t.Error("a burnt code was marked used")
	}
}

func TestRightCodeAfterFourWrongTriesWorks(t *testing.T) {
	code, c := freshCode(t)
	for i := 0; i < MaxTries-1; i++ {
		_ = c.Verify(wrongCode(code), t0)
	}
	if err := c.Verify(code, t0); err != nil {
		t.Errorf("right code on the fifth try = %v, want nil", err)
	}
}

func TestShapesOfWrongCode(t *testing.T) {
	code, _ := freshCode(t)
	for name, p := range map[string]string{
		"empty":        "",
		"prefix":       code[:CodeLength-1],
		"one extra":    code + "0",
		"doubled":      code + code,
		"huge":         strings.Repeat("A", 1<<16),
		"not alphabet": strings.Repeat("!", CodeLength),
		"spaces":       " " + code,
		"nul":          code[:CodeLength-1] + "\x00",
	} {
		_, c := NewCode(bytes.NewReader(bytes.Repeat([]byte{1}, CodeLength)), t0)
		if err := c.Verify(p, t0); err != ErrWrongCode {
			t.Errorf("%s: Verify = %v, want ErrWrongCode", name, err)
		}
		if c.Tries() != 1 {
			t.Errorf("%s: tries = %d, want 1", name, c.Tries())
		}
	}
}

// TestComparisonIsOfFixedLengthDigests holds the constant-time property that a
// test can hold: whatever the length of the text presented, the comparison is
// of two digests of one length, so there is no early return on a length
// difference.
func TestComparisonIsOfFixedLengthDigests(t *testing.T) {
	for _, n := range []int{0, 1, CodeLength - 1, CodeLength, CodeLength + 1, 1000} {
		d := digest(strings.Repeat("A", n))
		if len(d) != 32 {
			t.Errorf("digest of %d characters is %d bytes", n, len(d))
		}
	}
	if digest("abc") != digest("ABC") {
		t.Error("digest is not case-folded")
	}
}

func TestVerifyClockEdges(t *testing.T) {
	code, c := freshCode(t)
	if err := c.Verify(code, time.Time{}); err != ErrClock {
		t.Errorf("zero time = %v, want ErrClock", err)
	}
	if err := c.Verify(wrongCode(code), t0.Add(-time.Nanosecond)); err != ErrClock {
		t.Errorf("time before the issue = %v, want ErrClock", err)
	}
	if c.Tries() != 0 || c.Used() {
		t.Error("a clock refusal changed the code")
	}
	if err := c.Verify(code, t0); err != nil {
		t.Errorf("time of the issue = %v, want nil", err)
	}
}

func TestErrorsAreDistinctAndTyped(t *testing.T) {
	all := []Error{ErrWrongCode, ErrExpired, ErrUsed, ErrBurnt, ErrNoPairing,
		ErrPairingOpen, ErrPending, ErrClock, ErrRandom}
	seen := map[string]bool{}
	for _, e := range all {
		if seen[e.Error()] {
			t.Errorf("two errors with the text %q", e.Error())
		}
		seen[e.Error()] = true
		wrapped := fmt.Errorf("relay: %w", e)
		if !errors.Is(wrapped, e) {
			t.Errorf("errors.Is fails for %v", e)
		}
		var typed Error
		if !errors.As(wrapped, &typed) || typed != e {
			t.Errorf("errors.As fails for %v", e)
		}
	}
}

// TestNoSecretInTexts makes every refusal and every text of a code, and
// checks that none shows the code.
func TestNoSecretInTexts(t *testing.T) {
	code, c := NewCode(counting(), t0)
	texts := []string{}
	for _, e := range []Error{ErrWrongCode, ErrExpired, ErrUsed, ErrBurnt, ErrNoPairing,
		ErrPairingOpen, ErrPending, ErrClock, ErrRandom} {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
			texts = append(texts, fmt.Sprintf(verb, e), fmt.Sprintf(verb, fmt.Errorf("x: %w", e)))
		}
	}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		texts = append(texts, fmt.Sprintf(verb, c), fmt.Sprintf(verb, &c))
	}
	// Every refusal Verify can give, with the code in hand.
	_ = c.Verify(wrongCode(code), t0)
	for i := 0; i < MaxTries; i++ {
		texts = append(texts, fmt.Sprint(c.Verify(wrongCode(code), t0)))
	}
	_, e := NewCode(counting(), t0)
	texts = append(texts, fmt.Sprint(e.Verify(code, t0.Add(time.Hour))))
	_ = e.Verify(code, t0)
	for _, text := range texts {
		for _, secret := range []string{code, strings.ToLower(code), code[:8], code[8:]} {
			if strings.Contains(text, secret) {
				t.Errorf("text %q shows the code", text)
			}
		}
	}
}
