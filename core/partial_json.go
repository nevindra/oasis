package core

import (
	"bytes"
	"sync"
)

// PartialJSON takes an incomplete JSON byte slice and returns the most-complete
// valid JSON snapshot it can produce. It closes open strings, drops incomplete
// tail values, and terminates open objects/arrays.
//
// Returns (snapshot, true) when a valid snapshot can be produced.
// Returns (nil, false) when the input is empty or no valid snapshot exists.
//
// Why the pooled single-buffer design:
//
//	On the structured-streaming hot path (agent re-parses the entire
//	accumulated text on every EventTextDelta), this function used to allocate
//	a fresh bytes.Buffer per nested object/array/string node. For a 4KB JSON
//	body delivered as 800 deltas that's hundreds of thousands of small
//	allocations into gen-0. The current design writes every node directly
//	into one pooled output buffer and uses Truncate to roll back tentative
//	writes when a tail value turns out to be incomplete — one allocation per
//	call (the returned snapshot copy) instead of one per node.
func PartialJSON(data []byte) ([]byte, bool) {
	if len(data) == 0 {
		return nil, false
	}
	buf := partialJSONBufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer partialJSONBufPool.Put(buf)

	p := partialParser{data: data}
	if !p.parseValueInto(buf) {
		return nil, false
	}
	// Caller may retain the snapshot — copy out of the pooled buffer.
	out := make([]byte, buf.Len())
	copy(out, buf.Bytes())
	return out, true
}

var (
	partialJSONBufPool = sync.Pool{
		New: func() any { return &bytes.Buffer{} },
	}
	litTrue  = []byte("true")
	litFalse = []byte("false")
	litNull  = []byte("null")
)

type partialParser struct {
	data []byte
	pos  int
}

// parseValueInto writes a complete JSON value starting at the current position
// into out. Returns true on success, false when the value is genuinely
// incomplete (partial literal, partial number, or EOF before any token) — in
// which case out is unchanged and pos is rolled back.
//
// Object/array/string parsers always succeed by closing the truncated
// structure; only parseLiteralInto and parseNumberInto can refuse.
func (p *partialParser) parseValueInto(out *bytes.Buffer) bool {
	savepoint := out.Len()
	posSave := p.pos
	p.skipWhitespace()
	if p.pos >= len(p.data) {
		p.pos = posSave
		return false
	}
	ok := false
	switch p.data[p.pos] {
	case '{':
		ok = p.parseObjectInto(out)
	case '[':
		ok = p.parseArrayInto(out)
	case '"':
		ok = p.parseStringInto(out)
	case 't', 'f', 'n':
		ok = p.parseLiteralInto(out)
	default:
		if p.isNumberStart() {
			ok = p.parseNumberInto(out)
		}
	}
	if !ok {
		out.Truncate(savepoint)
		p.pos = posSave
		return false
	}
	return true
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

func (p *partialParser) parseObjectInto(out *bytes.Buffer) bool {
	p.pos++ // consume '{'
	out.WriteByte('{')
	first := true
	for {
		p.skipWhitespace()
		if p.pos >= len(p.data) {
			out.WriteByte('}')
			return true
		}
		if p.data[p.pos] == '}' {
			p.pos++
			out.WriteByte('}')
			return true
		}

		// Tentative pair: snapshot output and pos so we can drop the whole
		// key:value if any part of it turns out to be incomplete.
		pairOutSave := out.Len()
		pairPosSave := p.pos

		if !first {
			if p.data[p.pos] == ',' {
				p.pos++
				p.skipWhitespace()
				if p.pos >= len(p.data) {
					out.WriteByte('}')
					return true
				}
			}
		}
		if p.pos >= len(p.data) || p.data[p.pos] != '"' {
			out.Truncate(pairOutSave)
			p.pos = pairPosSave
			out.WriteByte('}')
			return true
		}
		if !first {
			out.WriteByte(',')
		}
		if !p.parseStringInto(out) {
			out.Truncate(pairOutSave)
			p.pos = pairPosSave
			out.WriteByte('}')
			return true
		}
		p.skipWhitespace()
		if p.pos >= len(p.data) || p.data[p.pos] != ':' {
			out.Truncate(pairOutSave)
			p.pos = pairPosSave
			out.WriteByte('}')
			return true
		}
		p.pos++ // consume ':'
		out.WriteByte(':')
		p.skipWhitespace()
		if p.pos >= len(p.data) {
			out.Truncate(pairOutSave)
			p.pos = pairPosSave
			out.WriteByte('}')
			return true
		}
		if !p.parseValueInto(out) {
			// Incomplete value — drop this whole key:value pair.
			out.Truncate(pairOutSave)
			p.pos = pairPosSave
			out.WriteByte('}')
			return true
		}
		first = false
	}
}

func (p *partialParser) parseArrayInto(out *bytes.Buffer) bool {
	p.pos++ // consume '['
	out.WriteByte('[')
	first := true
	for {
		p.skipWhitespace()
		if p.pos >= len(p.data) {
			out.WriteByte(']')
			return true
		}
		if p.data[p.pos] == ']' {
			p.pos++
			out.WriteByte(']')
			return true
		}

		elemOutSave := out.Len()
		elemPosSave := p.pos

		if !first {
			if p.data[p.pos] == ',' {
				p.pos++
				p.skipWhitespace()
				if p.pos >= len(p.data) {
					out.WriteByte(']')
					return true
				}
			}
			out.WriteByte(',')
		}
		if !p.parseValueInto(out) {
			// Incomplete element — drop it and close.
			out.Truncate(elemOutSave)
			p.pos = elemPosSave
			out.WriteByte(']')
			return true
		}
		first = false
	}
}

func (p *partialParser) parseStringInto(out *bytes.Buffer) bool {
	if p.pos >= len(p.data) || p.data[p.pos] != '"' {
		return false
	}
	p.pos++ // consume opening '"'
	out.WriteByte('"')
	for p.pos < len(p.data) {
		b := p.data[p.pos]
		if b == '\\' {
			if p.pos+1 >= len(p.data) {
				// Incomplete escape — close the string here.
				break
			}
			out.WriteByte(b)
			p.pos++
			out.WriteByte(p.data[p.pos])
			p.pos++
			continue
		}
		if b == '"' {
			out.WriteByte('"')
			p.pos++
			return true
		}
		out.WriteByte(b)
		p.pos++
	}
	// String was not closed — close it.
	out.WriteByte('"')
	return true
}

func (p *partialParser) parseLiteralInto(out *bytes.Buffer) bool {
	if bytes.HasPrefix(p.data[p.pos:], litTrue) {
		p.pos += len(litTrue)
		out.Write(litTrue)
		return true
	}
	if bytes.HasPrefix(p.data[p.pos:], litFalse) {
		p.pos += len(litFalse)
		out.Write(litFalse)
		return true
	}
	if bytes.HasPrefix(p.data[p.pos:], litNull) {
		p.pos += len(litNull)
		out.Write(litNull)
		return true
	}
	// Partial literal — not recoverable.
	return false
}

func (p *partialParser) parseNumberInto(out *bytes.Buffer) bool {
	start := p.pos
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
	if !isValidJSONNumber(raw) {
		// Incomplete number (e.g. "12.") — not recoverable.
		return false
	}
	out.Write(raw)
	return true
}

func isValidJSONNumber(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	// Simple state machine: [-][digits][.digits][e[+-]digits]
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
