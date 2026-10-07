package plantools

import (
	"fmt"
	"sync"
)

type boundedLogBuffer struct {
	mu      sync.Mutex
	limit   int
	headCap int
	tailCap int
	total   int
	head    []byte
	tail    []byte
}

func newBoundedLogBuffer(limit int) *boundedLogBuffer {
	headCap := limit / 3
	return &boundedLogBuffer{
		limit:   limit,
		headCap: headCap,
		tailCap: limit - headCap,
		head:    make([]byte, 0, headCap),
		tail:    make([]byte, 0, limit-headCap),
	}
}

func (b *boundedLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	originalLen := len(p)
	b.total += originalLen
	if remaining := b.headCap - len(b.head); remaining > 0 {
		take := min(remaining, len(p))
		b.head = append(b.head, p[:take]...)
		p = p[take:]
	}
	if len(p) == 0 || b.tailCap == 0 {
		return originalLen, nil
	}
	if len(p) >= b.tailCap {
		b.tail = append(b.tail[:0], p[len(p)-b.tailCap:]...)
		return originalLen, nil
	}
	if overflow := len(b.tail) + len(p) - b.tailCap; overflow > 0 {
		copy(b.tail, b.tail[overflow:])
		b.tail = b.tail[:len(b.tail)-overflow]
	}
	b.tail = append(b.tail, p...)
	return originalLen, nil
}

func (b *boundedLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.total <= b.limit {
		return string(append(append(make([]byte, 0, b.total), b.head...), b.tail...))
	}
	notice := fmt.Sprintf("\n\n[build log truncated: %d bytes total]\n\n", b.total)
	result := make([]byte, 0, len(b.head)+len(notice)+len(b.tail))
	result = append(result, b.head...)
	result = append(result, notice...)
	result = append(result, b.tail...)
	return string(result)
}
