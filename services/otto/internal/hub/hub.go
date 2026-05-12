// Package hub runs the in-process WebSocket fanout.
//
// Each connected client subscribes to one or more rooms. Rooms are keyed
// strings; the service uses two room families:
//
//   - conv:<conversation_id>  — everyone in a specific thread (the
//     customer who owns it + any staff who have opened it).
//   - inbox:<tenant>:<store>  — the staff inbox, used so staff get a
//     live toast when a new pending conversation appears.
//
// The hub itself doesn't know anything about tenants or isolation — it just
// fans messages out to whoever is subscribed. Isolation is enforced at the
// HTTP layer (you can only subscribe to a conversation room after passing
// the customer-session or staff-auth check). This keeps the hub small and
// testable.
package hub

import (
	"log/slog"
	"sync"

	"github.com/gorilla/websocket"
)

// Envelope is the outbound frame every client receives. Keep it stable —
// both the React widget and the admin inbox parse it.
type Envelope struct {
	Type    string         `json:"type"`
	Room    string         `json:"room,omitempty"`
	Payload map[string]any `json:"payload"`
}

// Client is one connected socket.
type Client struct {
	conn   *websocket.Conn
	send   chan []byte
	rooms  map[string]struct{}
	meta   map[string]string // free-form labels for audit/debug (e.g. user_id, role)
	closed bool
	mu     sync.Mutex
}

// Hub is the in-process pub/sub.
type Hub struct {
	mu      sync.RWMutex
	rooms   map[string]map[*Client]struct{}
	clients map[*Client]struct{}
	log     *slog.Logger
}

// New constructs a Hub.
func New(log *slog.Logger) *Hub {
	return &Hub{
		rooms:   make(map[string]map[*Client]struct{}),
		clients: make(map[*Client]struct{}),
		log:     log,
	}
}

// NewClient attaches a *websocket.Conn to the hub and returns a Client
// handle. Call client.Subscribe for each room, then client.Run to block
// until the peer disconnects.
func (h *Hub) NewClient(conn *websocket.Conn, meta map[string]string) *Client {
	c := &Client{
		conn:  conn,
		send:  make(chan []byte, 64),
		rooms: make(map[string]struct{}),
		meta:  meta,
	}
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	return c
}

// Subscribe adds a client to a room.
func (h *Hub) Subscribe(c *Client, room string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.rooms[room]; !ok {
		h.rooms[room] = make(map[*Client]struct{})
	}
	h.rooms[room][c] = struct{}{}
	c.mu.Lock()
	c.rooms[room] = struct{}{}
	c.mu.Unlock()
}

// Broadcast sends a JSON envelope to every client subscribed to the room.
// Callers who aren't subscribed never see it.
func (h *Hub) Broadcast(room string, env Envelope) {
	if env.Room == "" {
		env.Room = room
	}
	buf, err := jsonMarshal(env)
	if err != nil {
		if h.log != nil {
			h.log.Error("hub: marshal envelope", "err", err)
		}
		return
	}

	h.mu.RLock()
	subs := h.rooms[room]
	recipients := make([]*Client, 0, len(subs))
	for c := range subs {
		recipients = append(recipients, c)
	}
	h.mu.RUnlock()

	for _, c := range recipients {
		select {
		case c.send <- buf:
		default:
			// Slow consumer — drop them. Client.Run will clean up on the
			// next read failure.
			go h.Disconnect(c)
		}
	}
}

// Disconnect removes a client from all rooms and closes the socket.
func (h *Hub) Disconnect(c *Client) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	rooms := make([]string, 0, len(c.rooms))
	for r := range c.rooms {
		rooms = append(rooms, r)
	}
	c.mu.Unlock()

	h.mu.Lock()
	for _, r := range rooms {
		if set, ok := h.rooms[r]; ok {
			delete(set, c)
			if len(set) == 0 {
				delete(h.rooms, r)
			}
		}
	}
	delete(h.clients, c)
	h.mu.Unlock()

	_ = c.conn.Close()
	close(c.send)
}

// Run drives the client's read and write pumps until the peer disconnects.
// It ignores all inbound frames — the wire protocol is server-push only in
// v1; customers and staff send messages over REST.
func (c *Client) Run(h *Hub) {
	done := make(chan struct{})
	go c.writePump(done)
	c.readPump(h)
	<-done
}

func (c *Client) readPump(h *Hub) {
	defer h.Disconnect(c)
	c.conn.SetReadLimit(4 * 1024)
	for {
		if _, _, err := c.conn.NextReader(); err != nil {
			return
		}
	}
}

func (c *Client) writePump(done chan<- struct{}) {
	defer close(done)
	for buf := range c.send {
		if err := c.conn.WriteMessage(websocket.TextMessage, buf); err != nil {
			return
		}
	}
}
