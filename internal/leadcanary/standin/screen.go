//go:build linux && ataniq_canary

package standin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"
)

// The caps of a screen, the same as the injection library's own.
const (
	maxCols = 500
	maxRows = 200
	minCols = 20
	minRows = 5
	// maxFile bounds every file read from the replay folder.
	maxFile = 1 << 20
	// maxFaint bounds the faint spans of a screen.
	maxFaint = 4096
)

// span is a run of faint characters of one row: the zero-based row of the
// text, and the zero-based character (rune) offsets From, included, and To,
// excluded. They are offsets in the row's text, not cell columns.
type span struct {
	Row  int `json:"row"`
	From int `json:"from"`
	To   int `json:"to"`
}

// snapshot is one screen, in the JSON form of the injection library's
// fixtures. That form keeps the screen's text in a file beside the JSON, with
// the same name and the extension ".txt"; a "text" field in the JSON is
// taken in its place. Every other field (source, description, want) is
// ignored. CursorX is a cell column and CursorY a row, both zero-based. A
// span's offsets count characters of its row, and may run past the end of a
// row whose trailing blanks were cut.
type snapshot struct {
	Text         *string `json:"text"`
	Cols         int     `json:"cols"`
	Rows         int     `json:"rows"`
	CursorX      int     `json:"cursor_x"`
	CursorY      int     `json:"cursor_y"`
	CursorHidden bool    `json:"cursor_hidden"`
	AltScreen    bool    `json:"alt_screen"`
	Title        string  `json:"title"`
	Faint        []span  `json:"faint"`

	lines [][]rune
}

// readLimited reads a regular file of at most maxFile bytes, without
// following a link.
func readLimited(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", filepath.Base(path))
	}
	b, err := io.ReadAll(io.LimitReader(f, maxFile+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxFile {
		return nil, fmt.Errorf("%s is larger than %d bytes", filepath.Base(path), maxFile)
	}
	return b, nil
}

// loadSnapshot reads the screen of the JSON file at path: the file, and its
// text from the file's "text" field or from the ".txt" file beside it.
func loadSnapshot(path string) (*snapshot, error) {
	raw, err := readLimited(path)
	if err != nil {
		return nil, err
	}
	var s snapshot
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	var text string
	if s.Text != nil {
		text = *s.Text
	} else {
		b, err := readLimited(strings.TrimSuffix(path, ".json") + ".txt")
		if err != nil {
			return nil, err
		}
		text = string(b)
	}
	if err := s.check(text); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return &s, nil
}

// check validates the snapshot against its text and splits the text into
// rows. One final line break of the text is dropped.
func (s *snapshot) check(text string) error {
	if s.Cols < minCols || s.Cols > maxCols || s.Rows < minRows || s.Rows > maxRows {
		return errors.New("screen size outside 20x5 to 500x200")
	}
	if !utf8.ValidString(text) {
		return errors.New("text is not valid UTF-8")
	}
	text = strings.TrimSuffix(text, "\n")
	for _, row := range strings.Split(text, "\n") {
		if strings.IndexFunc(row, unicode.IsControl) >= 0 {
			return errors.New("text holds a control character")
		}
		runes := []rune(row)
		if len(runes) > s.Cols {
			return fmt.Errorf("a row of %d characters is wider than the %d columns", len(runes), s.Cols)
		}
		s.lines = append(s.lines, runes)
	}
	if len(s.lines) > s.Rows {
		return fmt.Errorf("%d rows of text on a screen of %d rows", len(s.lines), s.Rows)
	}
	if s.CursorX < 0 || s.CursorX >= s.Cols || s.CursorY < 0 || s.CursorY >= s.Rows {
		return errors.New("the cursor is outside the screen")
	}
	if len(s.Faint) > maxFaint {
		return errors.New("too many faint spans")
	}
	// A recorded row may have its trailing blanks cut, while a faint span
	// still runs over them (a faint blank is a faint cell). The row is padded
	// with blanks to the end of its spans, and a span may name a row that
	// follows the last row of text.
	for _, sp := range s.Faint {
		if sp.Row < 0 || sp.Row >= s.Rows || sp.From < 0 || sp.From > sp.To || sp.To > s.Cols {
			return fmt.Errorf("faint span %d,%d,%d is outside the screen", sp.Row, sp.From, sp.To)
		}
		for len(s.lines) <= sp.Row {
			s.lines = append(s.lines, nil)
		}
		for len(s.lines[sp.Row]) < sp.To {
			s.lines[sp.Row] = append(s.lines[sp.Row], ' ')
		}
	}
	return nil
}

// cleanTitle drops the control characters of a title, so that it cannot end
// its own OSC string.
func cleanTitle(t string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return -1
		}
		return r
	}, t)
}

// Control sequences the stand-in writes.
const (
	seqBracketedPasteOn = "\x1b[?2004h"
	seqAltScreenOn      = "\x1b[?1049h"
	seqAltScreenOff     = "\x1b[?1049l"
	seqCursorHide       = "\x1b[?25l"
	seqCursorShow       = "\x1b[?25h"
	seqAutowrapOff      = "\x1b[?7l"
	seqAutowrapOn       = "\x1b[?7h"
	seqClear            = "\x1b[H\x1b[2J"
	seqReset            = "\x1b[0m"
	seqFaint            = "\x1b[2m"
	seqDA1Query         = "\x1b[c"
)

// draw returns the bytes that draw the screen on a terminal that shows
// altNow (true when the alternate screen is on). The screen is drawn at
// absolute rows from a cleared screen: each row of text at its row, with the
// faint spans in SGR 2, then the title, then the cursor at its cell and
// visible or hidden as the snapshot says. Autowrap is off while the rows are
// drawn and on again after them: a row is checked in characters, not cells, so
// a row of wide characters can be wider than the screen, and it is clipped at
// the edge instead of wrapping into the next row.
func (s *snapshot) draw(altNow bool) []byte {
	var b bytes.Buffer
	b.WriteString(seqCursorHide)
	b.WriteString(seqAutowrapOff)
	switch {
	case s.AltScreen && !altNow:
		b.WriteString(seqAltScreenOn)
	case !s.AltScreen && altNow:
		b.WriteString(seqAltScreenOff)
	}
	b.WriteString(seqBracketedPasteOn)
	b.WriteString(seqClear)
	b.WriteString("\x1b]2;" + cleanTitle(s.Title) + "\x1b\\")
	for y, line := range s.lines {
		if len(line) == 0 {
			continue
		}
		b.WriteString("\x1b[" + strconv.Itoa(y+1) + ";1H")
		faint := make([]bool, len(line))
		for _, sp := range s.Faint {
			if sp.Row == y {
				for i := sp.From; i < sp.To; i++ {
					faint[i] = true
				}
			}
		}
		on := false
		for i, r := range line {
			if faint[i] != on {
				if faint[i] {
					b.WriteString(seqFaint)
				} else {
					b.WriteString(seqReset)
				}
				on = faint[i]
			}
			b.WriteRune(r)
		}
		if on {
			b.WriteString(seqReset)
		}
	}
	b.WriteString(seqAutowrapOn)
	b.WriteString("\x1b[" + strconv.Itoa(s.CursorY+1) + ";" + strconv.Itoa(s.CursorX+1) + "H")
	if !s.CursorHidden {
		b.WriteString(seqCursorShow)
	}
	return b.Bytes()
}
