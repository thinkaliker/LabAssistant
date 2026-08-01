// Package events is a small fan-out broker: many subscribers receive every published
// message. The manager uses it for the dashboard's live feeds (SSE).
//
// Every message carries a broker-assigned sequence number, and the broker keeps a bounded
// replay buffer of recent messages. Together they let an SSE handler (a) resume a reconnecting
// client from its Last-Event-ID instead of silently skipping whatever it missed, and (b) notice
// when a slow subscriber's buffer overflowed, because the sequence numbers it receives jump.
// Dropping is still preferred to blocking a publisher — but a drop is now detectable rather
// than invisible, so the dashboard can resync instead of running on a partial view.
package events

import "sync"

// ringSize bounds the replay buffer. It only has to cover a browser's EventSource reconnect
// (a few seconds), not an arbitrary outage: a client that falls further behind is told to
// resync from the REST endpoints instead.
const ringSize = 512

// Message is one published payload plus its sequence number. Sequence numbers start at 1 and
// increase by one per publish, so a gap means messages were dropped for that subscriber.
type Message struct {
	Seq  uint64
	Data []byte
}

// Broker fans published byte messages out to all current subscribers.
type Broker struct {
	mu   sync.Mutex
	seq  uint64
	subs map[chan Message]struct{}
	ring []Message
}

// New creates an empty broker.
func New() *Broker {
	return &Broker{subs: map[chan Message]struct{}{}}
}

// Subscribe returns a channel that receives every subsequent Publish, plus a cancel
// function the caller must invoke when done. Slow subscribers drop messages rather than
// block publishers; the sequence numbers let the reader detect that it happened.
func (b *Broker) Subscribe() (<-chan Message, func()) {
	ch := make(chan Message, 32)
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

// Seq returns the sequence number of the most recent publish. A reader whose own last-seen
// sequence trails this while its channel sits empty has been dropped from — it will never
// receive those messages, and nothing later is queued to reveal the gap.
func (b *Broker) Seq() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.seq
}

// Replay returns the buffered messages newer than after, for a client resuming from a
// Last-Event-ID. ok is false when the buffer no longer covers that point, meaning the caller
// has an unrecoverable gap and must resync from scratch.
func (b *Broker) Replay(after uint64) (msgs []Message, ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if after >= b.seq {
		// Already current — or a sequence from a previous manager process, whose numbering
		// restarted at 1. Either way there is nothing buffered to hand back.
		return nil, true
	}
	if len(b.ring) == 0 || b.ring[0].Seq > after+1 {
		return nil, false
	}
	for _, m := range b.ring {
		if m.Seq > after {
			msgs = append(msgs, m)
		}
	}
	return msgs, true
}

// Publish delivers data to all subscribers, skipping any whose buffer is full. It returns the
// sequence number assigned to the message, so a caller that also keeps its own replayable log
// (see jobs.Registry) can record where each entry sits in the stream.
func (b *Broker) Publish(data []byte) uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	m := Message{Seq: b.seq, Data: data}
	b.ring = append(b.ring, m)
	if len(b.ring) > ringSize {
		b.ring = b.ring[len(b.ring)-ringSize:]
	}
	for ch := range b.subs {
		select {
		case ch <- m:
		default:
		}
	}
	return m.Seq
}
