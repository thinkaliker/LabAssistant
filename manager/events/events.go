// Package events is a small fan-out broker: many subscribers receive every published
// message. The manager uses it for the dashboard's live feeds (SSE).
package events

import "sync"

// Broker fans published byte messages out to all current subscribers.
type Broker struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
}

// New creates an empty broker.
func New() *Broker {
	return &Broker{subs: map[chan []byte]struct{}{}}
}

// Subscribe returns a channel that receives every subsequent Publish, plus a cancel
// function the caller must invoke when done. Slow subscribers drop messages rather than
// block publishers.
func (b *Broker) Subscribe() (<-chan []byte, func()) {
	ch := make(chan []byte, 32)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	cancel := func() {
		b.mu.Lock()
		if _, ok := b.subs[ch]; ok {
			delete(b.subs, ch)
			close(ch)
		}
		b.mu.Unlock()
	}
	return ch, cancel
}

// Publish delivers data to all subscribers, skipping any whose buffer is full.
func (b *Broker) Publish(data []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- data:
		default:
		}
	}
}
