package events

import "testing"

func drain(ch <-chan Message) []Message {
	var out []Message
	for {
		select {
		case m := <-ch:
			out = append(out, m)
		default:
			return out
		}
	}
}

func TestPublishAssignsIncreasingSequence(t *testing.T) {
	b := New()
	ch, cancel := b.Subscribe()
	defer cancel()

	for i := 0; i < 3; i++ {
		b.Publish([]byte("x"))
	}
	got := drain(ch)
	if len(got) != 3 {
		t.Fatalf("got %d messages, want 3", len(got))
	}
	for i, m := range got {
		if want := uint64(i + 1); m.Seq != want {
			t.Errorf("message %d has seq %d, want %d", i, m.Seq, want)
		}
	}
}

func TestSubscriberOnlyGetsLaterMessages(t *testing.T) {
	b := New()
	b.Publish([]byte("before"))
	ch, cancel := b.Subscribe()
	defer cancel()
	b.Publish([]byte("after"))

	got := drain(ch)
	if len(got) != 1 || string(got[0].Data) != "after" {
		t.Fatalf("got %v, want just the message published after subscribing", got)
	}
}

func TestReplayReturnsMessagesAfterSequence(t *testing.T) {
	b := New()
	for _, s := range []string{"a", "b", "c"} {
		b.Publish([]byte(s))
	}

	msgs, ok := b.Replay(1)
	if !ok {
		t.Fatal("Replay reported a gap for a sequence still in the buffer")
	}
	if len(msgs) != 2 || string(msgs[0].Data) != "b" || string(msgs[1].Data) != "c" {
		t.Fatalf("got %v, want the messages after seq 1", msgs)
	}
}

func TestReplayFromCurrentSequenceIsEmpty(t *testing.T) {
	b := New()
	b.Publish([]byte("a"))

	msgs, ok := b.Replay(1)
	if !ok || len(msgs) != 0 {
		t.Fatalf("got (%v, %v), want no messages and no gap", msgs, ok)
	}
	// A sequence from a previous manager process (whose numbering restarted) must not be
	// reported as a gap either — there is simply nothing buffered to hand back.
	if msgs, ok := b.Replay(9999); !ok || len(msgs) != 0 {
		t.Fatalf("got (%v, %v) for a future sequence, want no messages and no gap", msgs, ok)
	}
}

func TestReplayReportsGapOnceEvicted(t *testing.T) {
	b := New()
	for i := 0; i < ringSize+10; i++ {
		b.Publish([]byte("x"))
	}
	if _, ok := b.Replay(1); ok {
		t.Fatal("Replay accepted a sequence that has been evicted from the buffer")
	}
}

// A subscriber that falls behind must not block publishers, and the gap it caused has to be
// visible in the sequence numbers it does receive — that is what tells the dashboard to resync.
func TestSlowSubscriberDropsButLeavesADetectableGap(t *testing.T) {
	b := New()
	ch, cancel := b.Subscribe()
	defer cancel()

	// Overrun the 32-deep subscriber buffer, then catch up: the reader gets what fitted.
	for i := 0; i < 60; i++ {
		b.Publish([]byte("x"))
	}
	first := drain(ch)
	if len(first) == 0 || len(first) >= 60 {
		t.Fatalf("got %d messages, want some but not all of 60", len(first))
	}

	// Publishing again reveals the hole: the next message's sequence does not follow on from
	// the last one this subscriber received.
	b.Publish([]byte("y"))
	second := drain(ch)
	if len(second) != 1 {
		t.Fatalf("got %d messages, want 1", len(second))
	}
	if prev := first[len(first)-1].Seq; second[0].Seq == prev+1 {
		t.Fatal("sequence numbers are contiguous, so the drop would be undetectable")
	}
}

// Messages dropped at the tail leave no later message to expose them, so the SSE handler
// compares its own position against the broker's instead.
func TestSeqExposesDropsWithNothingPublishedAfter(t *testing.T) {
	b := New()
	ch, cancel := b.Subscribe()
	defer cancel()

	for i := 0; i < 60; i++ {
		b.Publish([]byte("x"))
	}
	got := drain(ch)

	last := got[len(got)-1].Seq
	if len(ch) != 0 {
		t.Fatal("channel should be drained")
	}
	if b.Seq() <= last {
		t.Fatalf("broker seq %d does not exceed the last delivered seq %d, so the drop is invisible", b.Seq(), last)
	}
}
