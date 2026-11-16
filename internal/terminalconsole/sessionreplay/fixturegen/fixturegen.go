// Package fixturegen builds the synthetic recordings the fixture console
// replays. It is deterministic: it reads no clock, no random source, no
// environment and no file, so the same source always gives the same bytes. The
// program in cmd/genfixtures writes them to testdata.
//
// Every piece of text in the recordings is a constant in this file. KnownText
// lists them, and a test fails on any other text. The text is made up and
// refers to no real project, person, host or path.
package fixturegen

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"ataniq/internal/terminalconsole/sessionreplay"
)

// File is one generated recording and the name it has in testdata.
type File struct {
	Name string
	Data []byte
}

// Terminal control strings the recordings use.
const (
	clear = "\x1b[2J\x1b[H"
	bel   = "\x07"
	st    = "\x1b\\"
)

// Text the recordings show on screen. The OSC bodies include the leading
// parameter, such as "0;" for a window title.
const (
	textZWJ        = "synthetic ZWJ test"
	textFamily     = "\U0001F468‍\U0001F469‍\U0001F467‍\U0001F466"
	textTechnician = "\U0001F469‍\U0001F4BB"
	textZWJLine    = "family " + textFamily + " and technologist " + textTechnician

	textTitle       = "title test"
	textTitleSet    = "title set"
	oscTitleTask    = "0;✳ synthetic task"
	oscTitleDone    = "2;✅ synthetic task done"
	oscTitleSplit   = "0;✳ split title"
	oscTitleBoth    = "2;✳ ✅ both marks"
	oscTitleCheck   = "0;✅ synthetic check"
	oscTitleStar    = "2;✳ synthetic star"
	textMode        = "mode test"
	textModeOn      = "modes enabled"
	textModeOff     = "modes restored"
	textFrame       = "frame test"
	textFrameOneA   = "frame one, line a"
	textFrameOneB   = "frame one, line b"
	textFrameTwoA   = "frame two, line a"
	textFrameTwoB   = "frame two, line b"
	textFrameThreeA = "frame three, line a"
	textFrameThreeB = "frame three, line b"
	textHeldA       = "held frame, line a"
	textHeldB       = "held frame, line b"
	textMultiOne    = "multi frame 1"
	textMultiTwo    = "multi frame 2"
	textMultiThree  = "multi frame 3"
	textMultiFour   = "multi frame 4"
	textMultiFive   = "multi frame 5"
	textQuery       = "query test"
	textQueryDone   = "answers are discarded"
	oscQueryFG      = "10;?"
	oscQueryBG      = "11;?"
	oscQueryCursor  = "12;?"
	textResize      = "resize test"
	textBefore      = "size before the resize"
	textAfter       = "size after the resize"
	textAfterTwo    = "size after the second resize"
	textColour      = "colour test"
	textRed         = "red"
	textGreen       = "green"
	textBoldUnder   = "bold underline"
	text256         = "256 colour"
	textTrue        = "true colour"
	textFinished    = "session finished"
	textFailure     = "failure test"
	textStepOne     = "step 1 ok"
	textStepTwo     = "step 2 failed"
	textStatus      = "exit status 3"
	textClipboard   = "clipboard test"
	textClipboardOK = "clipboard write is blocked"
	oscClipboard    = "52;c;c3ludGhldGljIGNsaXBib2FyZCB0ZXh0" // base64 of "synthetic clipboard text"
	textUnknown     = "unknown status test"
	textLostStatus  = "no exit status was kept"
)

var known = []string{
	textZWJ, textZWJLine,
	textTitle, textTitleSet, oscTitleTask, oscTitleDone, oscTitleSplit, oscTitleBoth,
	oscTitleCheck, oscTitleStar,
	textMode, textModeOn, textModeOff,
	textFrame, textFrameOneA, textFrameOneB, textFrameTwoA, textFrameTwoB,
	textFrameThreeA, textFrameThreeB, textHeldA, textHeldB,
	textMultiOne, textMultiTwo, textMultiThree, textMultiFour, textMultiFive,
	textQuery, textQueryDone, oscQueryFG, oscQueryBG, oscQueryCursor,
	textResize, textBefore, textAfter, textAfterTwo,
	textColour, textRed, textGreen, textBoldUnder, text256, textTrue, textFinished,
	textFailure, textStepOne, textStepTwo, textStatus,
	textClipboard, textClipboardOK, oscClipboard,
	textUnknown, textLostStatus,
}

// KnownText returns every text and OSC body the generator can put in a
// recording, sorted. Recordings contain no other visible text.
func KnownText() []string {
	k := append([]string(nil), known...)
	sort.Strings(k)
	return k
}

type script struct{ rec sessionreplay.Recording }

func newScript(cols, rows int) *script {
	return &script{rec: sessionreplay.Recording{Cols: cols, Rows: rows}}
}

func at(ms int) time.Duration { return time.Duration(ms) * time.Millisecond }

// out adds one chunk at ms.
func (s *script) out(ms int, data string) {
	s.rec.Events = append(s.rec.Events, sessionreplay.Event{At: at(ms), Kind: sessionreplay.KindOutput, Data: []byte(data)})
}

// chunks adds one chunk per piece, step ms apart from ms on, and returns the
// time after the last one.
func (s *script) chunks(ms, step int, pieces ...string) int {
	for _, p := range pieces {
		s.out(ms, p)
		ms += step
	}
	return ms
}

func (s *script) resize(ms, cols, rows int) {
	s.rec.Events = append(s.rec.Events, sessionreplay.Event{At: at(ms), Kind: sessionreplay.KindResize, Cols: cols, Rows: rows})
}

// end adds the end record with a known exit status.
func (s *script) end(ms, status int) *sessionreplay.Recording {
	s.rec.Events = append(s.rec.Events, sessionreplay.Event{At: at(ms), Kind: sessionreplay.KindEnd, Status: status, StatusKnown: true})
	return &s.rec
}

// endUnknown adds the end record of a session with no known exit status.
func (s *script) endUnknown(ms int) *sessionreplay.Recording {
	s.rec.Events = append(s.rec.Events, sessionreplay.Event{At: at(ms), Kind: sessionreplay.KindEnd})
	return &s.rec
}

// Files returns the generated recordings in name order.
func Files() ([]File, error) {
	zwj, err := zwjSplit()
	if err != nil {
		return nil, err
	}
	recs := map[string]*sessionreplay.Recording{
		"zwj-split.jsonl":    zwj,
		"title.jsonl":        title(),
		"modes.jsonl":        modes(),
		"sync-frames.jsonl":  syncFrames(),
		"queries.jsonl":      queries(),
		"clipboard.jsonl":    clipboard(),
		"resize.jsonl":       resize(),
		"exit-zero.jsonl":    exitZero(),
		"exit-nonzero.jsonl": exitNonzero(),
		"exit-unknown.jsonl": exitUnknown(),
	}
	names := make([]string, 0, len(recs))
	for n := range recs {
		names = append(names, n)
	}
	sort.Strings(names)
	files := make([]File, 0, len(names))
	for _, n := range names {
		data, err := sessionreplay.Encode(recs[n])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		files = append(files, File{Name: n, Data: data})
	}
	return files, nil
}

// zwjSplit shows emoji ZWJ sequences cut across chunks: after a ZWJ, before
// one, and inside the UTF-8 bytes of an emoji and of a ZWJ.
func zwjSplit() (*sessionreplay.Recording, error) {
	pieces := []string{
		"family ",
		"\U0001F468‍",
		"\U0001F469",
		"‍\U0001F467‍\xf0\x9f",
		"\x91\xa6",
		" and technologist \U0001F469\xe2",
		"\x80\x8d",
		"\U0001F4BB",
	}
	if strings.Join(pieces, "") != textZWJLine {
		return nil, fmt.Errorf("zwj-split.jsonl: the pieces do not make the line")
	}
	s := newScript(80, 24)
	s.out(0, clear+textZWJ+"\r\n")
	ms := s.chunks(40, 40, pieces...)
	s.out(ms, "\r\n")
	return s.end(ms+80, 0), nil
}

func title() *sessionreplay.Recording {
	s := newScript(80, 24)
	s.out(0, clear+textTitle+"\r\n")
	// Both terminators, then titles cut inside the text and inside the UTF-8
	// bytes of the marks.
	s.out(40, "\x1b]"+oscTitleTask+bel)
	s.out(80, "\x1b]"+oscTitleDone+st)
	s.out(120, "\x1b]0;✳ sp")
	s.out(160, "lit title"+bel)
	s.chunks(200, 40, "\x1b]2;\xe2\x9c", "\xb3 \xe2\x9c", "\x85 both marks\x1b", "\\")
	// The other mark with the other terminator: a check mark ended by BEL and
	// a star alone ended by ST.
	s.out(360, "\x1b]"+oscTitleCheck+bel)
	s.out(400, "\x1b]"+oscTitleStar+st)
	s.out(440, textTitleSet+"\r\n")
	return s.end(520, 0)
}

func modes() *sessionreplay.Recording {
	s := newScript(80, 24)
	s.out(0, clear+textMode+"\r\n")
	s.out(40, "\x1b[?1049h")
	s.out(80, "\x1b[?25l")
	s.out(120, "\x1b[?1000h\x1b[?1002h")
	// Mode sets cut inside the number, between ESC and the bracket, and
	// between two sets.
	s.chunks(160, 40, "\x1b[?10", "03h\x1b[?10", "06h", "\x1b", "[?2004h")
	s.out(360, "\x1b[?1004h\x1b[?25h")
	s.out(400, textModeOn+"\r\n")
	s.out(600, "\x1b[?1004l\x1b[?2004l")
	s.out(640, "\x1b[?1006l\x1b[?1003l\x1b[?1002l\x1b[?1000l")
	s.out(680, "\x1b[?1049l")
	s.out(720, textModeOff+"\r\n")
	return s.end(800, 0)
}

const (
	syncOn  = "\x1b[?2026h"
	syncOff = "\x1b[?2026l"
	home    = "\x1b[3;1H\x1b[2K"
)

func syncFrames() *sessionreplay.Recording {
	s := newScript(80, 24)
	s.out(0, clear+textFrame+"\r\n")
	// Frames that begin, end and begin again inside a chunk, with chunk
	// boundaries inside a frame and inside the mode sequence itself.
	s.chunks(40, 40,
		syncOn+home+textFrameOneA+"\r\n",
		textFrameOneB+syncOff,
		syncOn+home+textFrameTwoA+"\r\n"+textFrameTwoB+"\r\n"+syncOff+syncOn+home+textFrameThreeA[:len("frame three")],
		textFrameThreeA[len("frame three"):]+"\r\n"+textFrameThreeB+"\x1b[?20",
		"26l")
	// Chunks that hold several whole frames.
	s.out(400, syncOn+home+textMultiOne+"\r\n"+syncOff+
		syncOn+home+textMultiTwo+"\r\n"+syncOff+
		syncOn+home+textMultiThree+"\r\n"+syncOff)
	s.out(440, syncOn+home+textMultiFour+"\r\n"+syncOff+
		syncOn+home+textMultiFive+"\r\n"+syncOff)
	// A frame held open for longer than the console waits for its end.
	s.out(480, syncOn+home+textHeldA+"\r\n")
	s.out(720, textHeldB+"\r\n"+syncOff)
	return s.end(800, 0)
}

func queries() *sessionreplay.Recording {
	s := newScript(80, 24)
	s.out(0, clear+textQuery+"\r\n")
	// Colour queries with both terminators, device attribute queries, a run of
	// queries in one chunk, and queries cut across chunks.
	s.out(50, "\x1b]"+oscQueryFG+bel)
	s.out(100, "\x1b]"+oscQueryBG+st)
	s.out(150, "\x1b]"+oscQueryCursor+bel)
	s.out(200, "\x1b[c")
	s.out(250, "\x1b[0c")
	s.out(300, "\x1b[>c")
	s.out(350, "\x1b]"+oscQueryFG+bel+"\x1b]"+oscQueryBG+bel+"\x1b[c")
	s.chunks(400, 50, "\x1b]11;", "?"+bel, "\x1b[", ">c", "\x1b]10;?\x1b", "\\")
	s.out(700, textQueryDone+"\r\n")
	return s.end(800, 0)
}

func clipboard() *sessionreplay.Recording {
	s := newScript(80, 24)
	s.out(0, clear+textClipboard+"\r\n")
	// A clipboard write, which a console must not carry out.
	s.out(60, "\x1b]"+oscClipboard+bel)
	s.out(120, textClipboardOK+"\r\n")
	return s.end(300, 0)
}

func resize() *sessionreplay.Recording {
	s := newScript(100, 30)
	s.out(0, clear+textResize+"\r\n"+textBefore+"\r\n")
	s.resize(300, 80, 24)
	s.out(340, clear+textAfter+"\r\n")
	s.resize(700, 120, 40)
	s.out(740, textAfterTwo+"\r\n")
	return s.end(1000, 0)
}

func exitZero() *sessionreplay.Recording {
	s := newScript(80, 24)
	s.out(0, clear+textColour+"\r\n")
	s.out(40, "\x1b[31m"+textRed+"\x1b[0m \x1b[32m"+textGreen+"\x1b[0m \x1b[1;4m"+textBoldUnder+"\x1b[0m\r\n")
	s.out(80, "\x1b[38;5;208m"+text256+"\x1b[0m \x1b[38;2;10;120;200m\x1b[48;2;40;40;40m"+textTrue+"\x1b[0m\r\n")
	s.out(120, textFinished+"\r\n")
	return s.end(300, 0)
}

func exitNonzero() *sessionreplay.Recording {
	s := newScript(80, 24)
	s.out(0, clear+textFailure+"\r\n")
	s.out(60, textStepOne+"\r\n")
	s.out(120, "\x1b[31m"+textStepTwo+"\x1b[0m\r\n")
	s.out(180, textStatus+"\r\n")
	return s.end(400, 3)
}

func exitUnknown() *sessionreplay.Recording {
	s := newScript(80, 24)
	s.out(0, clear+textUnknown+"\r\n")
	s.out(60, textStepOne+"\r\n")
	s.out(120, textLostStatus+"\r\n")
	return s.endUnknown(300)
}
