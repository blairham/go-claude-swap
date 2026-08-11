package swapapi

import (
	"sync"

	"github.com/blairham/go-claude-swap/internal/autoswitch"
)

// Broadcast is an autoswitch.EventSink that tees every event to an
// underlying sink (the terminal/JSONL output) and fans it out to any number
// of stream subscribers. Slow subscribers drop events rather than block the
// engine.
type Broadcast struct {
	next autoswitch.EventSink

	mu     sync.Mutex
	subs   map[int]chan autoswitch.Event
	nextID int
}

// NewBroadcast wraps next (which may be nil) with subscriber fan-out.
func NewBroadcast(next autoswitch.EventSink) *Broadcast {
	return &Broadcast{next: next, subs: map[int]chan autoswitch.Event{}}
}

// Emit implements autoswitch.EventSink.
func (b *Broadcast) Emit(e autoswitch.Event) {
	if b.next != nil {
		b.next.Emit(e)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- e:
		default: // subscriber is behind; dropping beats stalling the engine
		}
	}
}

// Subscribe returns a channel of future events and a cancel function.
func (b *Broadcast) Subscribe() (<-chan autoswitch.Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.nextID
	b.nextID++
	ch := make(chan autoswitch.Event, 64)
	b.subs[id] = ch
	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if _, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(ch)
		}
	}
}
