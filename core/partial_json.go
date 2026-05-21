package core

import "bytes"

// PartialJSON takes an incomplete JSON byte slice and returns the most-complete
// valid JSON snapshot it can produce. It closes open strings, drops incomplete
// tail values, and terminates open objects/arrays.
//
// Returns (snapshot, true) when a valid snapshot can be produced.
// Returns (nil, false) when the input is empty or no valid snapshot exists.
func PartialJSON(data []byte) ([]byte, bool) {
	if len(data) == 0 {
		return nil, false
	}
	p := &partialParser{data: data}
	result, ok := p.parse()
	if !ok {
		return nil, false
	}
	return result, true
}

type partialParser struct {
	data []byte
	pos  int
}

// parse parses a JSON value starting at the current position.
// Returns the serialized form and whether it succeeded.
func (p *partialParser) parse() ([]byte, bool) {
	p.skipWhitespace()
	if p.pos >= len(p.data) {
		return nil, false
	}
	switch p.data[p.pos] {
	case '{':
		return p.parseObject()
	case '[':
		return p.parseArray()
	case '"':
		return p.parseString()
	case 't', 'f', 'n':
		return p.parseLiteral()
	default:
		if p.isNumberStart() {
			return p.parseNumber()
		}
		return nil, false
	}
}

func (p *partialParser) skipWhitespace() {
	for p.pos < len(p.data) && isWS(p.data[p.pos]) {
		p.pos++
	}
}

func isWS(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

func (p *partialParser) isNumberStart() bool {
	b := p.data[p.pos]
	return b == '-' || (b >= '0' && b <= '9')
}

func (p *partialParser) parseObject() ([]byte, bool) {
	p.pos++ // consume '{'
	var buf bytes.Buffer
	buf.WriteByte('{')
	first := true
	for {
		p.skipWhitespace()
		if p.pos >= len(p.data) {
			// Truncated object: close it.
			buf.WriteByte('}')
			return buf.Bytes(), true
		}
		if p.data[p.pos] == '}' {
			p.pos++
			buf.WriteByte('}')
			return buf.Bytes(), true
		}
		if !first {
			if p.data[p.pos] == ',' {
				p.pos++ // consume ','
				p.skipWhitespace()
				if p.pos >= len(p.data) {
					// Trailing comma, no more keys.
					buf.WriteByte('}')
					return buf.Bytes(), true
				}
			}
		}
		if p.pos >= len(p.data) || p.data[p.pos] != '"' {
			// Expected a key but got something else or EOF; close object.
			buf.WriteByte('}')
			return buf.Bytes(), true
		}
		// Parse key.
		keyStart := p.pos
		keyBytes, ok := p.parseString()
		if !ok {
			// Incomplete key — close without this key.
			_ = keyStart
			buf.WriteByte('}')
			return buf.Bytes(), true
		}
		p.skipWhitespace()
		if p.pos >= len(p.data) || p.data[p.pos] != ':' {
			// No colon — drop this key.
			buf.WriteByte('}')
			return buf.Bytes(), true
		}
		p.pos++ // consume ':'
		p.skipWhitespace()
		if p.pos >= len(p.data) {
			// Key with no value — drop key.
			buf.WriteByte('}')
			return buf.Bytes(), true
		}
		// Parse value.
		valBytes, ok := p.parseValue()
		if !ok {
			// Incomplete value — drop this key-value pair.
			buf.WriteByte('}')
			return buf.Bytes(), true
		}
		if !first {
			buf.WriteByte(',')
		}
		buf.Write(keyBytes)
		buf.WriteByte(':')
		buf.Write(valBytes)
		first = false
	}
}

func (p *partialParser) parseArray() ([]byte, bool) {
	p.pos++ // consume '['
	var buf bytes.Buffer
	buf.WriteByte('[')
	first := true
	for {
		p.skipWhitespace()
		if p.pos >= len(p.data) {
			buf.WriteByte(']')
			return buf.Bytes(), true
		}
		if p.data[p.pos] == ']' {
			p.pos++
			buf.WriteByte(']')
			return buf.Bytes(), true
		}
		if !first {
			if p.data[p.pos] == ',' {
				p.pos++
				p.skipWhitespace()
				if p.pos >= len(p.data) {
					buf.WriteByte(']')
					return buf.Bytes(), true
				}
			}
		}
		valBytes, ok := p.parseValue()
		if !ok {
			// Incomplete element — drop it and close.
			buf.WriteByte(']')
			return buf.Bytes(), true
		}
		if !first {
			buf.WriteByte(',')
		}
		buf.Write(valBytes)
		first = false
	}
}

// parseValue parses a JSON value. Returns (nil, false) when the value is
// incomplete and cannot be snapshotted.
func (p *partialParser) parseValue() ([]byte, bool) {
	p.skipWhitespace()
	if p.pos >= len(p.data) {
		return nil, false
	}
	switch p.data[p.pos] {
	case '{':
		return p.parseObject()
	case '[':
		return p.parseArray()
	case '"':
		return p.parseString()
	case 't', 'f', 'n':
		return p.parseLiteral()
	default:
		if p.isNumberStart() {
			return p.parseNumber()
		}
		return nil, false
	}
}

func (p *partialParser) parseString() ([]byte, bool) {
	if p.pos >= len(p.data) || p.data[p.pos] != '"' {
		return nil, false
	}
	start := p.pos
	p.pos++ // consume opening '"'
	var buf bytes.Buffer
	buf.WriteByte('"')
	for p.pos < len(p.data) {
		b := p.data[p.pos]
		if b == '\\' {
			if p.pos+1 >= len(p.data) {
				// Incomplete escape — close the string here.
				break
			}
			buf.WriteByte(b)
			p.pos++
			buf.WriteByte(p.data[p.pos])
			p.pos++
			continue
		}
		if b == '"' {
			buf.WriteByte('"')
			p.pos++
			_ = start
			return buf.Bytes(), true
		}
		buf.WriteByte(b)
		p.pos++
	}
	// String was not closed — close it.
	buf.WriteByte('"')
	return buf.Bytes(), true
}

func (p *partialParser) parseLiteral() ([]byte, bool) {
	literals := []string{"true", "false", "null"}
	for _, lit := range literals {
		if bytes.HasPrefix(p.data[p.pos:], []byte(lit)) {
			p.pos += len(lit)
			return []byte(lit), true
		}
	}
	// Partial literal — not recoverable.
	return nil, false
}

func (p *partialParser) parseNumber() ([]byte, bool) {
	start := p.pos
	// Consume digits, sign, dot, e/E, +/-
	for p.pos < len(p.data) {
		b := p.data[p.pos]
		if b == '-' || b == '+' || b == '.' || b == 'e' || b == 'E' ||
			(b >= '0' && b <= '9') {
			p.pos++
		} else {
			break
		}
	}
	raw := p.data[start:p.pos]
	// Validate: must parse as a valid JSON number.
	// A complete number ends at a non-number byte.
	// If we consumed to EOF or hit a delimiter, check if raw is valid.
	if isValidJSONNumber(raw) {
		return raw, true
	}
	// Incomplete number (e.g. "12.") — not recoverable.
	return nil, false
}

func isValidJSONNumber(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	// Use a simple state machine: must be [-][digits][.digits][e[+-]digits]
	i := 0
	if b[i] == '-' {
		i++
	}
	if i >= len(b) {
		return false
	}
	if b[i] == '0' {
		i++
	} else if b[i] >= '1' && b[i] <= '9' {
		for i < len(b) && b[i] >= '0' && b[i] <= '9' {
			i++
		}
	} else {
		return false
	}
	if i < len(b) && b[i] == '.' {
		i++
		if i >= len(b) || b[i] < '0' || b[i] > '9' {
			return false
		}
		for i < len(b) && b[i] >= '0' && b[i] <= '9' {
			i++
		}
	}
	if i < len(b) && (b[i] == 'e' || b[i] == 'E') {
		i++
		if i < len(b) && (b[i] == '+' || b[i] == '-') {
			i++
		}
		if i >= len(b) || b[i] < '0' || b[i] > '9' {
			return false
		}
		for i < len(b) && b[i] >= '0' && b[i] <= '9' {
			i++
		}
	}
	return i == len(b)
}
