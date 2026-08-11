package events

import "testing"

func TestBrokerPublishSubscribe(t *testing.T) {
	b := NewBroker()
	ch, cancel := b.Subscribe()
	defer cancel()

	b.Publish(Event{Name: "metrics", Data: 1})
	if e := <-ch; e.Name != "metrics" {
		t.Fatalf("got %q", e.Name)
	}
	if n := b.SubscriberCount(); n != 1 {
		t.Fatalf("subscribers = %d, want 1", n)
	}
}

func TestBrokerDropOldestOnSlowSubscriber(t *testing.T) {
	b := NewBroker()
	ch, cancel := b.Subscribe()
	defer cancel()

	// Overfill the buffer without reading.
	for i := 0; i < subscriberBuffer+5; i++ {
		b.Publish(Event{Name: "e", Data: i})
	}

	// The oldest events must have been dropped: first readable is > 0,
	// and the newest published value is still present.
	first := (<-ch).Data.(int)
	if first == 0 {
		t.Fatalf("oldest event was not dropped, got first=%d", first)
	}
	last := first
	for {
		select {
		case e := <-ch:
			last = e.Data.(int)
		default:
			if last != subscriberBuffer+4 {
				t.Fatalf("newest event lost: last=%d, want %d", last, subscriberBuffer+4)
			}
			return
		}
	}
}

func TestBrokerUnsubscribeCleanup(t *testing.T) {
	b := NewBroker()
	_, cancel1 := b.Subscribe()
	ch2, cancel2 := b.Subscribe()
	if n := b.SubscriberCount(); n != 2 {
		t.Fatalf("subscribers = %d, want 2", n)
	}

	cancel1()
	cancel1() // idempotent
	if n := b.SubscriberCount(); n != 1 {
		t.Fatalf("after cancel subscribers = %d, want 1", n)
	}

	// Publishing must not panic on the closed/removed channel and must
	// still reach the live subscriber.
	b.Publish(Event{Name: "x"})
	if e := <-ch2; e.Name != "x" {
		t.Fatalf("live subscriber missed event")
	}
	cancel2()
	if n := b.SubscriberCount(); n != 0 {
		t.Fatalf("subscribers = %d, want 0", n)
	}
}
