package engine

import (
	"sort"
	"sync"
	"sync/atomic"
)

// bufferedLine is one inbound IRC line the app has not acked yet.
type bufferedLine struct {
	seq  uint64
	at   int64 // unix ms received
	line string
}

// lineCost is what a line charges against the byte budgets: its text plus a
// fixed overhead for the slice entry, so a flood of tiny lines is bounded too.
func lineCost(line string) int64 { return int64(len(line)) + 32 }

// lineBuffer is a session's backlog: every inbound line not yet acked, in seq
// order. Bounded in bytes, not lines — one busy channel and one quiet one
// differ by orders of magnitude, and what is being protected is the engine's
// memory. When full, the OLDEST lines go; newer lines describe the state the
// app is about to attach to.
//
// Deliberately in memory only: stugan's SQLite is the durable store, and this
// buffer exists to bridge a restart, not to be a second message log.
//
// Not safe for concurrent use; the owning session's mutex guards it. bytes is
// atomic so the global budget can pick the largest buffer without locking
// every session.
type lineBuffer struct {
	lines []bufferedLine
	bytes atomic.Int64
}

func (b *lineBuffer) push(l bufferedLine) {
	b.lines = append(b.lines, l)
	b.bytes.Add(lineCost(l.line))
}

// len is the number of buffered lines.
func (b *lineBuffer) len() int { return len(b.lines) }

// dropOldest removes the oldest line and returns it.
func (b *lineBuffer) dropOldest() (bufferedLine, bool) {
	if len(b.lines) == 0 {
		return bufferedLine{}, false
	}
	l := b.lines[0]
	b.lines[0] = bufferedLine{}
	b.lines = b.lines[1:]
	b.bytes.Add(-lineCost(l.line))
	b.compact()
	return l, true
}

// ackThrough drops every line with seq <= seq and returns the bytes freed.
func (b *lineBuffer) ackThrough(seq uint64) int64 {
	n := sort.Search(len(b.lines), func(i int) bool { return b.lines[i].seq > seq })
	var freed int64
	for i := range n {
		freed += lineCost(b.lines[i].line)
		b.lines[i] = bufferedLine{}
	}
	b.lines = b.lines[n:]
	b.bytes.Add(-freed)
	b.compact()
	return freed
}

// after returns up to limit lines with seq > seq, in order. The slice aliases
// the buffer: use it before releasing the owner's lock.
func (b *lineBuffer) after(seq uint64, limit int) []bufferedLine {
	i := sort.Search(len(b.lines), func(i int) bool { return b.lines[i].seq > seq })
	j := min(len(b.lines), i+limit)
	return b.lines[i:j]
}

// countAfter is how many buffered lines have seq > seq.
func (b *lineBuffer) countAfter(seq uint64) int {
	i := sort.Search(len(b.lines), func(i int) bool { return b.lines[i].seq > seq })
	return len(b.lines) - i
}

// clear empties the buffer and returns the bytes freed.
func (b *lineBuffer) clear() int64 {
	freed := b.bytes.Swap(0)
	b.lines = nil
	return freed
}

// compact re-slices into a fresh array once most of the backing array is
// dead space, so a long-lived session's buffer doesn't pin memory forever.
func (b *lineBuffer) compact() {
	if cap(b.lines) > 1024 && len(b.lines) < cap(b.lines)/4 {
		b.lines = append(make([]bufferedLine, 0, len(b.lines)*2), b.lines...)
	}
}

// budget bounds the bytes buffered across every session in the process, so a
// hundred connections mid-netsplit can't take the engine down by each staying
// under its own cap. Under global pressure it is the LARGEST backlog that
// sheds, not whichever connection happened to receive the next line.
type budget struct {
	max  int64
	used atomic.Int64

	mu       sync.Mutex
	sessions map[*session]struct{}
}

func newBudget(limit int64) *budget {
	return &budget{max: limit, sessions: map[*session]struct{}{}}
}

func (b *budget) register(s *session) {
	b.mu.Lock()
	b.sessions[s] = struct{}{}
	b.mu.Unlock()
}

func (b *budget) unregister(s *session) {
	b.mu.Lock()
	delete(b.sessions, s)
	b.mu.Unlock()
}

func (b *budget) charge(n int64) { b.used.Add(n) }
func (b *budget) refund(n int64) { b.used.Add(-n) }

// reclaim drops oldest lines from the largest buffers until the total fits.
// It must be called without any session lock held: it takes them itself.
func (b *budget) reclaim() {
	for b.used.Load() > b.max {
		b.mu.Lock()
		var largest *session
		var size int64
		for s := range b.sessions {
			if n := s.buf.bytes.Load(); n > size {
				largest, size = s, n
			}
		}
		b.mu.Unlock()
		if largest == nil || !largest.shedOldest() {
			return
		}
	}
}
