package server

import (
	"encoding/json"
	"sort"
	"sync"
	"time"
)

// maxItems caps the in-memory history. Files themselves stay on disk.
const maxItems = 1000

// Device is a browser (or future native client) connected to the hub.
type Device struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
	conns    int
}

// Item is one thing that was sent: a file or a piece of text.
type Item struct {
	ID       string    `json:"id"`
	Kind     string    `json:"kind"` // "file" | "text"
	From     string    `json:"from"`
	FromName string    `json:"fromName"`
	To       string    `json:"to,omitempty"` // empty means every device
	ToName   string    `json:"toName,omitempty"`
	Name     string    `json:"name,omitempty"`
	Size     int64     `json:"size,omitempty"`
	Mime     string    `json:"mime,omitempty"`
	Text     string    `json:"text,omitempty"`
	Time     time.Time `json:"time"`
	path     string
}

func (it *Item) visibleTo(device string) bool {
	return it.To == "" || it.To == device || it.From == device
}

type event struct {
	name string
	data []byte
}

type client struct {
	device string
	ch     chan event
}

type hello struct {
	Self    Device   `json:"self"`
	Devices []Device `json:"devices"`
	Items   []*Item  `json:"items"`
}

// Hub tracks online devices and the shared history, and fans events out to
// connected clients.
type Hub struct {
	mu      sync.Mutex
	devices map[string]*Device
	clients map[*client]struct{}
	items   []*Item
	byID    map[string]*Item
	closed  bool
}

func newHub() *Hub {
	return &Hub{
		devices: map[string]*Device{},
		clients: map[*client]struct{}{},
		byID:    map[string]*Item{},
	}
}

func (h *Hub) join(d Device) (*client, hello) {
	h.mu.Lock()
	defer h.mu.Unlock()

	c := &client{device: d.ID, ch: make(chan event, 64)}
	if h.closed {
		close(c.ch)
		return c, hello{}
	}
	if cur, ok := h.devices[d.ID]; ok {
		cur.Name, cur.Platform = d.Name, d.Platform
		cur.conns++
	} else {
		d.conns = 1
		h.devices[d.ID] = &d
	}
	h.clients[c] = struct{}{}

	visible := []*Item{}
	for _, it := range h.items {
		if it.visibleTo(d.ID) {
			visible = append(visible, it)
		}
	}
	h.broadcastDevicesLocked()
	return c, hello{Self: *h.devices[d.ID], Devices: h.deviceListLocked(), Items: visible}
}

func (h *Hub) leave(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[c]; !ok {
		return
	}
	h.dropLocked(c)
	h.broadcastDevicesLocked()
}

// dropLocked removes a client and closes its channel; the SSE handler then exits.
func (h *Hub) dropLocked(c *client) {
	delete(h.clients, c)
	close(c.ch)
	if d, ok := h.devices[c.device]; ok {
		d.conns--
		if d.conns <= 0 {
			delete(h.devices, c.device)
		}
	}
}

func (h *Hub) device(id string) (Device, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	d, ok := h.devices[id]
	if !ok {
		return Device{}, false
	}
	return *d, true
}

func (h *Hub) item(id string) *Item {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.byID[id]
}

func (h *Hub) addItem(it *Item) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.items = append(h.items, it)
	h.byID[it.ID] = it
	if len(h.items) > maxItems {
		old := h.items[0]
		h.items = h.items[1:]
		delete(h.byID, old.ID)
	}
	data, _ := json.Marshal(it)
	for c := range h.clients {
		if it.visibleTo(c.device) {
			h.sendLocked(c, event{"item", data})
		}
	}
}

func (h *Hub) deviceListLocked() []Device {
	list := make([]Device, 0, len(h.devices))
	for _, d := range h.devices {
		list = append(list, *d)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list
}

func (h *Hub) broadcastDevicesLocked() {
	data, _ := json.Marshal(h.deviceListLocked())
	for c := range h.clients {
		h.sendLocked(c, event{"devices", data})
	}
}

// sendLocked never blocks: a client that can't keep up is disconnected and
// will resync from the "hello" event when its EventSource reconnects.
func (h *Hub) sendLocked(c *client, ev event) {
	select {
	case c.ch <- ev:
	default:
		h.dropLocked(c)
	}
}

// close disconnects every client so long-lived SSE requests end on shutdown.
func (h *Hub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for c := range h.clients {
		h.dropLocked(c)
	}
}
