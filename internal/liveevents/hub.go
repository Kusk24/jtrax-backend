// In-process subscriptions for live game and inbox events.
package liveevents

import (
	"sync"
	"time"
)

// heartbeat keeps idle streams alive. Proxies drop connections that say nothing
// for a while, and a silent game — two players thinking — is the normal case.
const Heartbeat = 25 * time.Second

// subBuffer is how far behind a watcher may fall before it is dropped. A player
// who backgrounds the tab stops reading; the game must not stall for the
// opponent because of it, so a full channel loses events rather than blocking.
const subBuffer = 8

type Hub struct {
	mu   sync.Mutex
	subs map[string]map[chan []byte]struct{} // room id -> subscribers
}

func NewHub() *Hub { return &Hub{subs: map[string]map[chan []byte]struct{}{}} }

func (h *Hub) Subscribe(roomID string) chan []byte {
	ch := make(chan []byte, subBuffer)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subs[roomID] == nil {
		h.subs[roomID] = map[chan []byte]struct{}{}
	}
	h.subs[roomID][ch] = struct{}{}
	return ch
}

func (h *Hub) Unsubscribe(roomID string, ch chan []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subs[roomID], ch)
	if len(h.subs[roomID]) == 0 {
		delete(h.subs, roomID)
	}
}

// Broadcast never blocks: a subscriber that cannot keep up is skipped, and will
// catch up from its next full read.
func (h *Hub) Broadcast(roomID string, payload []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[roomID] {
		select {
		case ch <- payload:
		default:
		}
	}
}
