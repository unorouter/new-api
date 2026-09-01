package notify

import (
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const maxTopicsPerConn = 100

type wsConn struct {
	// Server-assigned so a client cannot claim another connection's id and be
	// addressed in its place. Rooms use it as the guest identity.
	id           string
	send         chan []byte
	mu           sync.Mutex
	topics       map[string]struct{}
	wildcards    map[string]struct{}
	endpointHash string
	closed       bool
}

func (c *wsConn) setTopics(topics []string, add bool) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, t := range topics {
		target := c.topics
		if strings.Contains(t, "*") {
			target = c.wildcards
		}
		if !add {
			delete(target, t)
			continue
		}
		if len(c.topics)+len(c.wildcards) >= maxTopicsPerConn {
			break
		}
		target[t] = struct{}{}
	}
	out := make([]string, 0, len(c.topics)+len(c.wildcards))
	for t := range c.topics {
		out = append(out, t)
	}
	for t := range c.wildcards {
		out = append(out, t)
	}
	return out
}

func (c *wsConn) matches(topics []string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, t := range topics {
		if _, ok := c.topics[t]; ok {
			return true
		}
	}
	for w := range c.wildcards {
		for _, t := range topics {
			if TopicMatches(w, t) {
				return true
			}
		}
	}
	return false
}

// trySend enqueues a frame without ever writing to a closed channel.
// Returns false when the outbound buffer is full (slow consumer).
func (c *wsConn) trySend(payload []byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return true
	}
	select {
	case c.send <- payload:
		return true
	default:
		return false
	}
}

type hub struct {
	mu    sync.RWMutex
	conns map[*wsConn]struct{}
	byID  map[string]*wsConn
}

var globalHub = &hub{
	conns: make(map[*wsConn]struct{}),
	byID:  make(map[string]*wsConn),
}

func (h *hub) register(c *wsConn) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.conns) >= WSMaxConns() {
		return false
	}
	h.conns[c] = struct{}{}
	if c.id != "" {
		h.byID[c.id] = c
	}
	return true
}

func (h *hub) unregister(c *wsConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.conns[c]; ok {
		delete(h.conns, c)
		// Only drop the index entry when it still points at THIS connection, so
		// a late unregister cannot evict a reconnect that reused the id.
		if c.id != "" && h.byID[c.id] == c {
			delete(h.byID, c.id)
		}
		c.mu.Lock()
		if !c.closed {
			c.closed = true
			close(c.send)
		}
		c.mu.Unlock()
	}
}

// sendTo delivers to one connection by id. Connection ids are pod-local, so a
// miss is the normal case on the replicas that do not hold the target.
func (h *hub) sendTo(connID string, payload []byte) bool {
	h.mu.RLock()
	c := h.byID[connID]
	h.mu.RUnlock()
	if c == nil {
		return false
	}
	if !c.trySend(payload) {
		go h.unregister(c)
		return false
	}
	return true
}

func (h *hub) broadcast(payload []byte, topics []string) {
	h.mu.RLock()
	conns := make([]*wsConn, 0, len(h.conns))
	for c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.RUnlock()
	for _, c := range conns {
		if !c.matches(topics) {
			continue
		}
		if !c.trySend(payload) {
			// Slow consumer: drop the connection rather than block fanout.
			go h.unregister(c)
		}
	}
}

// StartHub subscribes this replica to the Redis event channel and fans
// events out to local WS connections. Safe to call on every replica.
func StartHub() {
	if !Enabled() {
		return
	}
	StartEventSubscriber(func(evt Event) {
		frame, err := common.Marshal(map[string]interface{}{"op": "event", "event": evt})
		if err != nil {
			return
		}
		globalHub.broadcast(frame, evt.Topics)
	})
	StartRoomSubscriber()
	// Periodically refresh presence for all connected devices so the push
	// sender keeps suppressing while a tab stays open.
	go func() {
		ticker := time.NewTicker(45 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			globalHub.mu.RLock()
			for c := range globalHub.conns {
				if c.endpointHash != "" {
					RefreshPresence(c.endpointHash)
				}
			}
			globalHub.mu.RUnlock()
		}
	}()
}
