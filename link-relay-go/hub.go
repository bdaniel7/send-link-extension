package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Hub tracks live connections per device so a POST from one device can wake
// every other connected device instead of waiting for its next poll.
type Hub struct {
	mu      sync.Mutex
	clients map[string]map[*wsConn]bool
}

func NewHub() *Hub {
	return &Hub{clients: make(map[string]map[*wsConn]bool)}
}

func (h *Hub) add(deviceID string, c *wsConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.clients[deviceID] == nil {
		h.clients[deviceID] = make(map[*wsConn]bool)
	}
	h.clients[deviceID][c] = true
}

func (h *Hub) remove(deviceID string, c *wsConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if set, ok := h.clients[deviceID]; ok {
		delete(set, c)
		if len(set) == 0 {
			delete(h.clients, deviceID)
		}
	}
}

// OtherLabels returns the distinct, non-empty labels of currently connected
// devices other than excludeDeviceID, sorted. Extensions use it to name the
// browser(s) their links will go to.
func (h *Hub) OtherLabels(excludeDeviceID string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	seen := map[string]bool{}
	labels := []string{}
	for deviceID, set := range h.clients {
		if deviceID == excludeDeviceID {
			continue
		}
		for c := range set {
			if c.label != "" && !seen[c.label] {
				seen[c.label] = true
				labels = append(labels, c.label)
			}
		}
	}
	sort.Strings(labels)
	return labels
}

func (h *Hub) BroadcastExcept(excludeDeviceID string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for deviceID, set := range h.clients {
		if deviceID == excludeDeviceID {
			continue
		}
		for c := range set {
			_ = c.writeText(string(data))
		}
	}
}

// heartbeatLoop pings every connection on an interval and drops any that
// didn't answer the previous ping, and separately sends an app-level
// heartbeat message. A protocol pong never reaches the extension's JS —
// the browser answers it automatically at the network layer — so an actual
// message is what's needed to keep an MV3 service worker's activity timer
// from expiring.
func (h *Hub) heartbeatLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		h.mu.Lock()
		for deviceID, set := range h.clients {
			for c := range set {
				if !c.alive.Load() {
					delete(set, c)
					go c.close()
					continue
				}
				c.alive.Store(false)
				_ = c.writePing()
				_ = c.writeText(`{"type":"heartbeat"}`)
			}
			if len(set) == 0 {
				delete(h.clients, deviceID)
			}
		}
		h.mu.Unlock()
	}
}

// Handler wires storage, the connection hub, and the shared token together
// for the HTTP layer.
type Handler struct {
	store    *Store
	hub      *Hub
	token    string
	maxAgeMs int64
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func requireBearer(token string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" || got != token {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next(w, r)
	}
}

func (h *Handler) handleLinks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		h.handlePostLink(w, r)
	case http.MethodGet:
		h.handleGetLinks(w, r)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (h *Handler) handlePostLink(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL      string `json:"url"`
		Title    string `json:"title"`
		DeviceID string `json:"deviceId"`
		Label    string `json:"label"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	if body.URL == "" || body.DeviceID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url and deviceId are required"})
		return
	}

	label := cleanLabel(body.Label)
	if label != "" {
		log.Printf("received url:%s from host:%s label:%s", sanitizeLog(body.URL), remoteHost(r), label)
	} else {
		log.Printf("received url:%s from host:%s", sanitizeLog(body.URL), remoteHost(r))
	}

	entry := Link{
		ID:        newID(),
		URL:       body.URL,
		Title:     body.Title,
		From:      body.DeviceID,
		FromLabel: label,
		CreatedAt: time.Now().UnixMilli(),
		SeenBy:    []string{},
	}

	if _, err := h.store.Update(func(links []Link) []Link {
		return append(links, entry)
	}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "storage failure"})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "id": entry.ID})

	// Wake up every other connected client so it polls right away instead
	// of waiting for its next watchdog tick.
	h.hub.BroadcastExcept(body.DeviceID, map[string]string{"type": "new-link"})
}

func (h *Handler) handleGetLinks(w http.ResponseWriter, r *http.Request) {
	deviceID := r.URL.Query().Get("deviceId")
	if deviceID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "deviceId is required"})
		return
	}

	now := time.Now().UnixMilli()
	var unseen []Link

	if _, err := h.store.Update(func(links []Link) []Link {
		kept := links[:0]
		for _, l := range links {
			if now-l.CreatedAt >= h.maxAgeMs {
				continue // expired — drop it
			}
			if l.From != deviceID && !containsStr(l.SeenBy, deviceID) {
				l.SeenBy = append(l.SeenBy, deviceID)
				unseen = append(unseen, l)
			}
			kept = append(kept, l)
		}
		return kept
	}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "storage failure"})
		return
	}

	type outLink struct {
		ID        string `json:"id"`
		URL       string `json:"url"`
		Title     string `json:"title"`
		CreatedAt int64  `json:"createdAt"`
		FromLabel string `json:"fromLabel,omitempty"`
	}
	out := make([]outLink, 0, len(unseen))
	for _, l := range unseen {
		out = append(out, outLink{ID: l.ID, URL: l.URL, Title: l.Title, CreatedAt: l.CreatedAt, FromLabel: l.FromLabel})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) handleDevices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	deviceID := r.URL.Query().Get("deviceId")
	if deviceID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "deviceId is required"})
		return
	}
	writeJSON(w, http.StatusOK, map[string][]string{"labels": h.hub.OtherLabels(deviceID)})
}

// handleStream upgrades to a WebSocket. Browsers can't set custom headers
// on a WebSocket handshake, so the token travels as a query parameter here
// instead of the Authorization header the REST endpoints use.
func (h *Handler) handleStream(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	token := q.Get("token")
	deviceID := q.Get("deviceId")
	if h.token == "" || token != h.token || deviceID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	c, err := upgrade(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	c.alive.Store(true)
	c.label = cleanLabel(q.Get("label"))

	h.hub.add(deviceID, c)
	// Tell the other devices someone new is online so they can refresh the
	// name shown in their menus.
	h.hub.BroadcastExcept(deviceID, map[string]string{"type": "devices-changed"})
	go h.readLoop(deviceID, c)
}

func (h *Handler) readLoop(deviceID string, c *wsConn) {
	defer func() {
		h.hub.remove(deviceID, c)
		c.close()
	}()
	for {
		op, payload, err := c.readFrame()
		if err != nil {
			return
		}
		switch op {
		case opPong:
			c.alive.Store(true)
		case opPing:
			_ = c.writeFrame(opPong, payload)
		case opClose:
			return
		default:
			// This relay's clients don't send application messages; ignore.
		}
	}
}

// remoteHost returns the client's IP as seen by this server. Behind a
// reverse proxy this will be the proxy's address, not the real client's.
func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// sanitizeLog replaces control characters (newlines in particular) so a
// crafted URL can't forge extra log lines.
func sanitizeLog(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, s)
}

// cleanLabel trims and sanitizes the browser label sent by the extension
// and caps it at 40 characters.
func cleanLabel(s string) string {
	s = sanitizeLog(strings.TrimSpace(s))
	if r := []rune(s); len(r) > 40 {
		s = string(r[:40])
	}
	return s
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		log.Fatalf("failed to generate id: %v", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
