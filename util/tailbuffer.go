package util

import "sync"

// TailBuffer is an io.Writer that keeps only the last N bytes written to it.
// It is safe for concurrent use.
type TailBuffer struct {
	mu    sync.Mutex
	data  []byte // fixed size: the limit
	pos   int    // where the next byte goes
	full  bool   // whether data has wrapped around at least once
	total int64  // total bytes ever written
}

// NewTailBuffer returns a TailBuffer that keeps the last limit bytes.
// A limit below 1 keeps nothing.
func NewTailBuffer(limit int) *TailBuffer {
	limit = max(limit, 0)
	return &TailBuffer{data: make([]byte, limit)}
}

func (b *TailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, size := len(p), len(b.data)
	b.total += int64(n)
	if size == 0 {
		return n, nil
	}
	if n >= size { // p alone fills the buffer: keep its tail
		copy(b.data, p[n-size:])
		b.pos, b.full = 0, true
		return n, nil
	}
	if b.pos+n >= size {
		b.full = true
	}
	c := copy(b.data[b.pos:], p) // up to the end of data
	copy(b.data, p[c:])          // the rest wraps to the start
	b.pos = (b.pos + n) % size
	return n, nil
}

// Bytes returns a copy of the retained bytes, oldest first.
func (b *TailBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.full {
		return append([]byte(nil), b.data[:b.pos]...)
	}
	out := make([]byte, 0, len(b.data))
	out = append(out, b.data[b.pos:]...)
	return append(out, b.data[:b.pos]...)
}

// String returns the retained bytes, oldest first.
func (b *TailBuffer) String() string {
	return string(b.Bytes())
}

// Total returns the number of bytes ever written, including dropped ones.
func (b *TailBuffer) Total() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.total
}
