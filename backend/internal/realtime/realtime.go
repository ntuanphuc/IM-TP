package realtime

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"chattp/internal/auth"
	"chattp/internal/platform"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

type Hub struct {
	*auth.Service
	mu        sync.Mutex
	listeners map[chan struct{}]struct{}
}

func New(ctx context.Context, s *auth.Service) *Hub {
	h := &Hub{Service: s, listeners: make(map[chan struct{}]struct{})}
	go func() {
		sub := s.Redis.Subscribe(ctx, "chattp.events")
		defer sub.Close()
		messages := sub.Channel()
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-messages:
				if !ok {
					return
				}
				h.mu.Lock()
				for c := range h.listeners {
					select {
					case c <- struct{}{}:
					default:
					}
				}
				h.mu.Unlock()
			}
		}
	}()
	return h
}

type Frame struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw := auth.Bearer(r)
	origin := r.Header.Get("Origin")
	if (origin != "" && !h.OriginAllowed(r)) || (origin == "" && raw == "") {
		platform.Error(w, 403, "websocket_origin_denied")
		return
	}
	h.mu.Lock()
	if len(h.listeners) >= 5000 {
		h.mu.Unlock()
		platform.Error(w, 503, "socket_capacity")
		return
	}
	wake := make(chan struct{}, 1)
	h.listeners[wake] = struct{}{}
	h.mu.Unlock()
	defer func() { h.mu.Lock(); delete(h.listeners, wake); h.mu.Unlock() }()
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(8192)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	input := make(chan Frame)
	go func() {
		defer close(input)
		for {
			var f Frame
			if wsjson.Read(ctx, c, &f) != nil {
				return
			}
			select {
			case input <- f:
			case <-ctx.Done():
				return
			}
		}
	}()
	write := func(v any) error {
		deadline, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		return wsjson.Write(deadline, c, v)
	}
	if raw == "" {
		select {
		case f, ok := <-input:
			if !ok || f.Type != "auth" {
				return
			}
			var data struct {
				Token string `json:"token"`
			}
			if json.Unmarshal(f.Data, &data) != nil {
				return
			}
			raw = data.Token
		case <-time.After(5 * time.Second):
			return
		case <-ctx.Done():
			return
		}
	}
	user, err := h.Token(ctx, raw)
	if err != nil {
		_ = c.Close(websocket.StatusPolicyViolation, "unauthorized")
		return
	}
	var cursor int64
	if err = h.DB.QueryRow(ctx, "SELECT coalesce(max(id),0) FROM events").Scan(&cursor); err != nil {
		return
	}
	if write(map[string]any{"type": "auth.ok", "data": map[string]any{"cursor": cursor}}) != nil {
		return
	}
	drain := func() bool {
		check, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		if _, err := h.Token(check, raw); err != nil {
			_ = c.Close(websocket.StatusPolicyViolation, "unauthorized")
			return false
		}
		var high int64
		if err := h.DB.QueryRow(check, "SELECT coalesce(max(id),0) FROM events").Scan(&high); err != nil {
			return false
		}
		for cursor < high {
			rows, err := h.DB.Query(check, `SELECT e.id,e.channel_id,e.kind,e.payload FROM events e
    JOIN channel_members m ON m.channel_id=e.channel_id AND m.user_id=$1
    WHERE e.id>$2 AND e.id<=$3 ORDER BY e.id LIMIT 100`, user.ID, cursor, high)
			if err != nil {
				return false
			}
			var frames []map[string]any
			for rows.Next() {
				var id int64
				var channel, kind string
				var payload json.RawMessage
				if rows.Scan(&id, &channel, &kind, &payload) != nil {
					rows.Close()
					return false
				}
				cursor = id
				frames = append(frames, map[string]any{"type": kind, "id": id, "channel_id": channel, "data": payload})
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return false
			}
			for _, frame := range frames {
				if write(frame) != nil {
					return false
				}
			}
			if len(frames) < 100 {
				cursor = high
			}
		}
		return true
	}
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	lastSignal := time.Now()
	expiryNotified := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-wake:
			if !drain() {
				return
			}
		case <-ticker.C:
			if time.Since(lastSignal) >= 60*time.Second {
				return
			}
			if !drain() {
				return
			}
			if !expiryNotified && time.Until(user.ExpiresAt) < time.Minute {
				if write(map[string]string{"type": "auth.expiring"}) != nil {
					return
				}
				expiryNotified = true
			}
			if write(map[string]string{"type": "pong"}) != nil {
				return
			}
		case f, ok := <-input:
			if !ok {
				return
			}
			lastSignal = time.Now()
			switch f.Type {
			case "resume":
				var data struct {
					Cursor int64 `json:"cursor"`
				}
				if json.Unmarshal(f.Data, &data) != nil || data.Cursor < 0 || data.Cursor > cursor {
					return
				}
				cursor = data.Cursor
				if !drain() {
					return
				}
			case "auth":
				var data struct {
					Token string `json:"token"`
				}
				if json.Unmarshal(f.Data, &data) != nil {
					return
				}
				next, err := h.Token(ctx, data.Token)
				if err != nil || next.Session != user.Session {
					return
				}
				raw = data.Token
				user = next
				expiryNotified = false
			case "ping":
				if write(map[string]string{"type": "pong"}) != nil {
					return
				}
			}
		}
	}
}
