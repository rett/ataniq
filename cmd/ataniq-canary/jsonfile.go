//go:build linux && ataniq_canary

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// This file holds the two subcommands that read a JSON file for the attended
// run script, which has no JSON reader of its own: fields prints the values of
// one JSON object as lines, and scrub-screen replaces the screen text of a
// copy of the holder's final record by its hash. Neither reads the config.

// maxJSONFile is the largest file either subcommand reads. A final record
// holds one screen, which the holder bounds well below this.
const maxJSONFile = 8 << 20

// scrubbedPrefix starts the text that scrub-screen puts in place of a screen.
const scrubbedPrefix = "sha256:"

// runFile is "ataniq-canary fields|scrub-screen --file FILE".
func runFile(sub string, args []string, out, errOut io.Writer) int {
	fl := flag.NewFlagSet(sub, flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	file := fl.String("file", "", "the JSON file")
	if err := fl.Parse(args); err != nil || fl.NArg() != 0 || *file == "" {
		fmt.Fprintf(errOut, "ataniq-canary %s: --file FILE is required and nothing else is accepted\n%s\n", sub, usage)
		return exitUsage
	}
	var err error
	if sub == "fields" {
		err = fields(*file, out)
	} else {
		err = scrubScreen(*file, out)
	}
	if err != nil {
		fmt.Fprintf(errOut, "ataniq-canary %s: %v\n", sub, err)
		return exitFailed
	}
	return exitOK
}

// readObject reads the file, a regular file of at most maxJSONFile bytes that
// is not a symlink, as one JSON object with nothing after it. Numbers keep
// their text. "-" is standard input.
func readObject(file string) (map[string]any, error) {
	var r io.Reader = os.Stdin
	if file != "-" {
		f, err := os.OpenFile(file, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return nil, errors.New("the file cannot be opened, or is a symlink")
		}
		defer f.Close()
		if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
			return nil, errors.New("the file is not a regular file")
		}
		r = f
	}
	data, err := io.ReadAll(io.LimitReader(r, maxJSONFile+1))
	if err != nil {
		return nil, errors.New("the file cannot be read")
	}
	if len(data) > maxJSONFile {
		return nil, fmt.Errorf("the file is larger than %d bytes", maxJSONFile)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil || obj == nil {
		return nil, errors.New("the file is not one JSON object")
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("the file holds data after the JSON object")
	}
	return obj, nil
}

// fields prints every value of the object as "path=value", one per line,
// sorted by path. A path is the keys from the top joined by dots, with the
// index for an element of a list ("result.outcome", "argv.0"). A string that
// holds a control character, a quote at its start or a byte outside ASCII is
// printed as a quoted Go string, so a line is always one line and nothing
// reaches the terminal raw; null is printed as "null". A key that holds a dot,
// an equals sign or a control character is refused, so a path names one value.
func fields(file string, out io.Writer) error {
	obj, err := readObject(file)
	if err != nil {
		return err
	}
	var lines []string
	if err := flatten("", obj, &lines); err != nil {
		return err
	}
	sort.Strings(lines)
	for _, l := range lines {
		fmt.Fprintln(out, l)
	}
	return nil
}

func flatten(prefix string, v any, lines *[]string) error {
	switch t := v.(type) {
	case map[string]any:
		for k, e := range t {
			if k == "" || strings.ContainsFunc(k, func(r rune) bool { return r < 0x21 || r > 0x7e || r == '.' || r == '=' }) {
				return errors.New("a key of the object is not a plain name")
			}
			if err := flatten(join(prefix, k), e, lines); err != nil {
				return err
			}
		}
	case []any:
		for i, e := range t {
			if err := flatten(join(prefix, strconv.Itoa(i)), e, lines); err != nil {
				return err
			}
		}
	case string:
		*lines = append(*lines, prefix+"="+safeValue(t))
	case json.Number:
		*lines = append(*lines, prefix+"="+t.String())
	case bool:
		*lines = append(*lines, prefix+"="+strconv.FormatBool(t))
	case nil:
		*lines = append(*lines, prefix+"=null")
	}
	return nil
}

func join(prefix, k string) string {
	if prefix == "" {
		return k
	}
	return prefix + "." + k
}

// safeValue returns s as it is when it is printable ASCII that does not start
// with a quote, and as a quoted Go string in ASCII otherwise.
func safeValue(s string) string {
	plain := !strings.HasPrefix(s, `"`)
	for i := 0; plain && i < len(s); i++ {
		plain = s[i] >= 0x20 && s[i] <= 0x7e
	}
	if plain {
		return s
	}
	return strconv.QuoteToASCII(s)
}

// scrubScreen replaces the string "text" of the object "screen" in the file,
// a copy of the holder's final record, by "sha256:" and the SHA-256 of the
// text in hex, and writes the file again (a temporary file beside it, then a
// rename; mode 0600). It prints the hash and never the text. A file whose
// screen text is such a hash already, or that has no screen text, is left as
// it is, and the line says so.
func scrubScreen(file string, out io.Writer) error {
	if file == "-" {
		return errors.New("scrub-screen needs a file, not standard input")
	}
	obj, err := readObject(file)
	if err != nil {
		return err
	}
	screen, _ := obj["screen"].(map[string]any)
	text, ok := screen["text"].(string)
	if !ok {
		fmt.Fprintln(out, "kept: the record holds no screen text")
		return nil
	}
	if isScrubbed(text) {
		fmt.Fprintln(out, "kept: the screen text is a hash already")
		return nil
	}
	sum := sha256.Sum256([]byte(text))
	screen["text"] = scrubbedPrefix + hex.EncodeToString(sum[:])
	data, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return errors.New("the record cannot be encoded")
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), ".scrub-*")
	if err != nil {
		return errors.New("no temporary file can be made beside the record")
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(append(data, '\n')); err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), file)
	}
	if err != nil {
		return errors.New("the scrubbed record cannot be written")
	}
	fmt.Fprintf(out, "scrubbed: the screen text is replaced by %s\n", screen["text"])
	return nil
}

func isScrubbed(s string) bool {
	h, ok := strings.CutPrefix(s, scrubbedPrefix)
	if !ok || len(h) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(h)
	return err == nil && strings.ToLower(h) == h
}
