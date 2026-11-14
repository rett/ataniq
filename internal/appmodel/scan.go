package appmodel

import (
	"bytes"
	"encoding/json"
	"unicode/utf8"
)

// scanJSON bounds work before typed decoding. It retains only decoded keys in
// currently open objects, not a generic tree. encoding/json alone repairs bad
// UTF-8/surrogates and accepts duplicate keys, so cannot perform this check.
func scanJSON(wire []byte) error {
	if len(wire) > MaxWireBytes || !utf8.Valid(wire) {
		return invalid()
	}
	s := scanner{wire: wire}
	s.space()
	if s.pos == len(wire) || wire[s.pos] != '{' || !s.value(0) {
		return invalid()
	}
	s.space()
	if s.pos != len(wire) {
		return invalid()
	}
	return nil
}

type scanner struct {
	wire        []byte
	pos, tokens int
}

func (s *scanner) space() {
	for s.pos < len(s.wire) {
		switch s.wire[s.pos] {
		case ' ', '\t', '\r', '\n':
			s.pos++
		default:
			return
		}
	}
}
func (s *scanner) take(c byte) bool {
	if s.pos < len(s.wire) && s.wire[s.pos] == c {
		s.pos++
		return true
	}
	return false
}
func (s *scanner) token() bool { s.tokens++; return s.tokens <= MaxTokens }

func (s *scanner) value(depth int) bool {
	s.space()
	if s.pos == len(s.wire) || !s.token() {
		return false
	}
	switch s.wire[s.pos] {
	case '{', '[':
		if depth >= MaxDepth {
			return false
		}
		object := s.wire[s.pos] == '{'
		end := byte(']')
		if object {
			end = '}'
		}
		s.pos++
		s.space()
		if s.take(end) {
			return s.token()
		}
		var keys map[string]struct{}
		if object {
			keys = make(map[string]struct{})
		}
		for {
			if object {
				if !s.token() {
					return false
				}
				raw, ok := s.string()
				if !ok {
					return false
				}
				var key string
				if json.Unmarshal(raw, &key) != nil {
					return false
				}
				if _, exists := keys[key]; exists {
					return false
				}
				keys[key] = struct{}{}
				s.space()
				if !s.take(':') {
					return false
				}
			}
			if !s.value(depth + 1) {
				return false
			}
			s.space()
			if s.take(end) {
				return s.token()
			}
			if !s.take(',') {
				return false
			}
			s.space()
		}
	case '"':
		_, ok := s.string()
		return ok
	case 't':
		return s.literal("true")
	case 'f':
		return s.literal("false")
	case 'n':
		return s.literal("null")
	default:
		return s.number()
	}
}

func (s *scanner) literal(v string) bool {
	if !bytes.HasPrefix(s.wire[s.pos:], []byte(v)) {
		return false
	}
	s.pos += len(v)
	return true
}
func (s *scanner) digit() bool {
	return s.pos < len(s.wire) && s.wire[s.pos] >= '0' && s.wire[s.pos] <= '9'
}
func (s *scanner) digits() bool {
	start := s.pos
	for s.digit() {
		s.pos++
	}
	return s.pos > start
}
func (s *scanner) number() bool {
	s.take('-')
	if !s.take('0') && !s.digits() {
		return false
	}
	if s.take('.') && !s.digits() {
		return false
	}
	if s.take('e') || s.take('E') {
		if !s.take('+') {
			s.take('-')
		}
		if !s.digits() {
			return false
		}
	}
	return true
}

// string checks escapes before handing a bounded valid string to json.Unmarshal.
func (s *scanner) string() ([]byte, bool) {
	start := s.pos
	if !s.take('"') {
		return nil, false
	}
	for s.pos < len(s.wire) {
		c := s.wire[s.pos]
		s.pos++
		switch {
		case c == '"':
			return s.wire[start:s.pos], true
		case c < 0x20:
			return nil, false
		case c == '\\':
			if s.pos == len(s.wire) {
				return nil, false
			}
			escape := s.wire[s.pos]
			s.pos++
			switch escape {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
			case 'u':
				u, ok := s.hex4()
				if !ok {
					return nil, false
				}
				if u >= 0xdc00 && u <= 0xdfff {
					return nil, false
				}
				if u >= 0xd800 && u <= 0xdbff {
					if !s.take('\\') || !s.take('u') {
						return nil, false
					}
					lo, ok := s.hex4()
					if !ok || lo < 0xdc00 || lo > 0xdfff {
						return nil, false
					}
				}
			default:
				return nil, false
			}
		}
	}
	return nil, false
}
func (s *scanner) hex4() (uint16, bool) {
	if len(s.wire)-s.pos < 4 {
		return 0, false
	}
	var n uint16
	for range 4 {
		c := s.wire[s.pos]
		s.pos++
		var v byte
		switch {
		case c >= '0' && c <= '9':
			v = c - '0'
		case c >= 'a' && c <= 'f':
			v = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			v = c - 'A' + 10
		default:
			return 0, false
		}
		n = n*16 + uint16(v)
	}
	return n, true
}
