// Package events fans live updates (node metrics, task state changes) out
// to SSE subscribers. The broker never blocks a publisher: slow subscribers
// lose their oldest buffered event instead of stalling the system.
package events

import "sync"

// Event is one named SSE event; Data is marshaled to JSON as the payload.
type Event struct {
	Name string
	Data any
}

const subscriberBuffer = 16

// Broker is a minimal pub/sub hub for SSE.
type Broker struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

// NewBroker returns an empty broker.
func NewBroker() *Broker {
	return &Broker{subs: make(map[chan Event]struct{})}
}

// Subscribe registers a subscriber. The returned cancel must be called to
// release the subscription; the channel is closed by cancel.
func (b *Broker) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, subscriberBuffer)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, ch)
			b.mu.Unlock()
			close(ch)
		})
	}
	return ch, cancel
}

// Publish delivers e to every subscriber, dropping the oldest buffered
// event of any subscriber whose buffer is full.
func (b *Broker) Publish(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- e:
		default:
			select {
			case <-ch: // drop oldest
			default:
			}
			select {
			case ch <- e:
			default:
			}
		}
	}
}

// SubscriberCount reports how many subscribers are registered.
func (b *Broker) SubscriberCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}
