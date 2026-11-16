package sessionreplay

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

type headerLine struct {
	Format  string `json:"format"`
	Version int    `json:"version"`
	Cols    int    `json:"cols"`
	Rows    int    `json:"rows"`
}

type recordLine struct {
	T      *int64  `json:"t"`
	K      string  `json:"k"`
	D      *string `json:"d,omitempty"`
	Cols   *int    `json:"cols,omitempty"`
	Rows   *int    `json:"rows,omitempty"`
	Status *int    `json:"status,omitempty"`
}

// Encode returns the recording in the documented format. The same recording
// always gives the same bytes. It fails when the recording is not valid.
func Encode(r *Recording) ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	line := func(v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		out.Write(b)
		out.WriteByte('\n')
		return nil
	}
	if err := line(headerLine{Format, Version, r.Cols, r.Rows}); err != nil {
		return nil, err
	}
	for _, e := range r.Events {
		t := e.At.Milliseconds()
		rec := recordLine{T: &t, K: e.Kind.String()}
		switch e.Kind {
		case KindOutput:
			d := base64.StdEncoding.EncodeToString(e.Data)
			rec.D = &d
		case KindResize:
			c, rw := e.Cols, e.Rows
			rec.Cols, rec.Rows = &c, &rw
		case KindEnd:
			if e.StatusKnown {
				s := e.Status
				rec.Status = &s
			}
		}
		if err := line(rec); err != nil {
			return nil, err
		}
	}
	if out.Len() > MaxBytes {
		return nil, fmt.Errorf("recording larger than %d bytes", MaxBytes)
	}
	return out.Bytes(), nil
}

// Parse reads a recording in the documented format and validates it. Errors
// name the line.
func Parse(data []byte) (*Recording, error) {
	if len(data) > MaxBytes {
		return nil, fmt.Errorf("recording larger than %d bytes", MaxBytes)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		return nil, errors.New("recording does not end with a newline")
	}
	// Count before splitting, so that input made of newlines is refused
	// before it makes a slice of lines.
	if bytes.Count(data, []byte{'\n'}) > MaxEvents+1 {
		return nil, fmt.Errorf("more than %d records", MaxEvents)
	}
	lines := bytes.Split(data[:len(data)-1], []byte{'\n'})
	var h headerLine
	if err := strictDecode(lines[0], &h); err != nil {
		return nil, fmt.Errorf("line 1: %w", err)
	}
	if h.Format != Format {
		return nil, fmt.Errorf("line 1: format %q is not %q", h.Format, Format)
	}
	if h.Version != Version {
		return nil, fmt.Errorf("line 1: version %d is not %d", h.Version, Version)
	}
	rec := &Recording{Cols: h.Cols, Rows: h.Rows}
	for i, l := range lines[1:] {
		e, err := parseRecord(l)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", i+2, err)
		}
		rec.Events = append(rec.Events, e)
	}
	if err := rec.Validate(); err != nil {
		return nil, err
	}
	// encoding/json ignores the case of keys, keeps the last of a repeated
	// key and skips white space. Accept only the text Encode writes, so that
	// one recording has one text.
	canon, err := Encode(rec)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(canon, data) {
		return nil, fmt.Errorf("line %d: not in the documented form", firstDifference(canon, data))
	}
	return rec, nil
}

// firstDifference returns the number of the first line in which a and b
// differ, counting from 1.
func firstDifference(a, b []byte) int {
	al, bl := bytes.Split(a, []byte{'\n'}), bytes.Split(b, []byte{'\n'})
	for i := 0; i < len(al) && i < len(bl); i++ {
		if !bytes.Equal(al[i], bl[i]) {
			return i + 1
		}
	}
	return min(len(al), len(bl))
}

func strictDecode(line []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("data after the JSON object")
	}
	return nil
}

func parseRecord(line []byte) (Event, error) {
	var r recordLine
	if err := strictDecode(line, &r); err != nil {
		return Event{}, err
	}
	if r.T == nil {
		return Event{}, errors.New(`missing "t"`)
	}
	if *r.T < 0 || *r.T > MaxAt.Milliseconds() {
		return Event{}, fmt.Errorf(`"t" outside 0 to %d`, MaxAt.Milliseconds())
	}
	e := Event{At: time.Duration(*r.T) * time.Millisecond}
	switch r.K {
	case "out":
		if r.D == nil || r.Cols != nil || r.Rows != nil || r.Status != nil {
			return Event{}, errors.New(`an "out" record has "d" and nothing else`)
		}
		b, err := base64.StdEncoding.Strict().DecodeString(*r.D)
		if err != nil {
			return Event{}, fmt.Errorf(`"d": %w`, err)
		}
		e.Kind, e.Data = KindOutput, b
	case "resize":
		if r.Cols == nil || r.Rows == nil || r.D != nil || r.Status != nil {
			return Event{}, errors.New(`a "resize" record has "cols" and "rows" and nothing else`)
		}
		e.Kind, e.Cols, e.Rows = KindResize, *r.Cols, *r.Rows
	case "end":
		if r.D != nil || r.Cols != nil || r.Rows != nil {
			return Event{}, errors.New(`an "end" record has at most "status"`)
		}
		e.Kind = KindEnd
		if r.Status != nil {
			e.Status, e.StatusKnown = *r.Status, true
		}
	default:
		return Event{}, fmt.Errorf("unknown kind %q", r.K)
	}
	return e, nil
}
