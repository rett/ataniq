package tests

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"ataniq/internal/approval/seatpair"
)

// The seat app's pairing script computes the six words of a new passkey itself
// and compares them with the daemon's, so its word list and its formula must
// be those of the seatpair package. The seatapp package's import guard keeps
// it from importing the repository, so the check is here, where both may be
// imported.

const seatappWordsJS = "internal/seatapp/assets/words.js"

// the vector of seatpair's own test (fingerprint_test.go): made there with a
// second implementation, not with the package.
const (
	vectorSPKI  = "3059301306072a8648ce3d020106082a8648ce3d030107034200046ff03b949241ce1dadd43519e6960e0a85b41a69a05c328103aa2bce1594ca163c4f753a55bf01dc53f6c0b0c7eee78b40c6ff7d25a96e2282b989cef71c144a"
	vectorWords = "poppy ribbon brick goblet nickel ruby"
)

func readWordsJS(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root(t), seatappWordsJS))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// jsWordList reads the 256 words of the script's list: the quoted strings of
// the array that follows "var LIST = [".
func jsWordList(t *testing.T, js string) []string {
	t.Helper()
	_, after, ok := strings.Cut(js, "var LIST = [")
	if !ok {
		t.Fatal("the script has no `var LIST = [`")
	}
	body, _, ok := strings.Cut(after, "];")
	if !ok {
		t.Fatal("the list does not end with `];`")
	}
	// Nothing in the array but quoted words, commas and white space.
	rest := regexp.MustCompile(`"[a-z]+"`).ReplaceAllString(body, "")
	if strings.Trim(rest, ", \t\r\n") != "" {
		t.Fatalf("the list holds more than words: %q", rest)
	}
	var words []string
	for _, m := range regexp.MustCompile(`"([a-z]+)"`).FindAllStringSubmatch(body, -1) {
		words = append(words, m[1])
	}
	return words
}

func TestSeatappWordsListEqualsSeatpair(t *testing.T) {
	got := jsWordList(t, readWordsJS(t))
	want := seatpair.WordList()
	if len(got) != 256 {
		t.Fatalf("the script's list has %d words, want 256", len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("word %d: script %q, seatpair %q", i, got[i], want[i])
		}
	}
}

// TestSeatappWordsFormulaInTheSource checks the formula without running the
// script: SHA-256, the 4-byte big-endian length prefixes and the first six
// bytes, and the vector's expected words through seatpair.
func TestSeatappWordsFormulaInTheSource(t *testing.T) {
	js := readWordsJS(t)
	for _, want := range []string{`"SHA-256"`, "crypto.subtle.digest", "setUint32(", "false", "slice(0, 6)"} {
		if !strings.Contains(js, want) {
			t.Errorf("the script lacks %q", want)
		}
	}
	for _, bad := range []string{"Math.random", "innerHTML", "eval(", "http://", "https://", "localStorage"} {
		if strings.Contains(js, bad) {
			t.Errorf("the script holds %q", bad)
		}
	}
	spki, _ := hex.DecodeString(vectorSPKI)
	id := make([]byte, 32)
	for i := range id {
		id[i] = byte(i)
	}
	if got := seatpair.FingerprintOf(id, spki).String(); got != vectorWords {
		t.Errorf("seatpair's vector: %q, want %q", got, vectorWords)
	}
}

// nodeHarness loads the script in a bare context with the web crypto of node
// and prints the words of each (credential id, SPKI) pair read from stdin, one
// line of hex pairs per input, as the script computes them.
const nodeHarness = `
const fs = require("fs"), vm = require("vm");
const box = { crypto: require("crypto").webcrypto };
box.window = box;
vm.createContext(box);
vm.runInContext(fs.readFileSync(process.argv[2], "utf8"), box);
(async () => {
  const lines = fs.readFileSync(0, "utf8").split("\n").filter(Boolean);
  for (const line of lines) {
    const [id, spki] = line.split(" ").map((h) => new Uint8Array(Buffer.from(h, "hex")));
    console.log((await box.SeatWords.fingerprint(id.buffer, spki)).join(" "));
  }
})();
`

// TestSeatappWordsScriptRuns runs the script's fingerprint function under node
// (when node is installed) on the vector and on random inputs, and compares
// with seatpair.FingerprintOf. It needs no browser, only the web crypto API.
func TestSeatappWordsScriptRuns(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; the formula is checked in the source only")
	}
	dir := t.TempDir()
	harness := filepath.Join(dir, "harness.js")
	if err := os.WriteFile(harness, []byte(nodeHarness), 0o600); err != nil {
		t.Fatal(err)
	}
	var in bytes.Buffer
	var want []string
	add := func(id, spki []byte) {
		in.WriteString(hex.EncodeToString(id) + " " + hex.EncodeToString(spki) + "\n")
		want = append(want, seatpair.FingerprintOf(id, spki).String())
	}
	vspki, _ := hex.DecodeString(vectorSPKI)
	vid := make([]byte, 32)
	for i := range vid {
		vid[i] = byte(i)
	}
	add(vid, vspki)
	if want[0] != vectorWords {
		t.Fatalf("vector %q", want[0])
	}
	for i := 0; i < 40; i++ {
		id := make([]byte, 16+i*20)
		rand.Read(id)
		spki := make([]byte, 91)
		rand.Read(spki)
		add(id, spki)
	}
	cmd := exec.Command(node, harness, filepath.Join(root(t), seatappWordsJS))
	cmd.Stdin = &in
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("node: %v: %s", err, errb.String())
	}
	got := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(got) != len(want) {
		t.Fatalf("%d lines, want %d: %s", len(got), len(want), out.String())
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("input %d: script %q, seatpair %q", i, got[i], want[i])
		}
	}
}
