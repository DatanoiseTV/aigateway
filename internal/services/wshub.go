package services

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const wsDebug = false

// DashboardHub manages WebSocket connections from admin dashboard clients
// and broadcasts real-time stats updates whenever a new request is logged.
type DashboardHub struct {
	mu           sync.RWMutex
	clients      map[*websocket.Conn]bool
	statsService *StatsService
	// debounce broadcasts to avoid hammering the DB on burst traffic
	pending   bool
	debounceT *time.Timer
}

func NewDashboardHub(statsService *StatsService) *DashboardHub {
	return &DashboardHub{
		clients:      make(map[*websocket.Conn]bool),
		statsService: statsService,
	}
}

// Register adds a new WebSocket connection and sends initial state.
func (h *DashboardHub) Register(conn *websocket.Conn) {
	h.mu.Lock()
	h.clients[conn] = true
	h.mu.Unlock()

	if wsDebug {
		log.Printf("[WS] Dashboard client connected (%d total)", h.clientCount())
	}

	// Send initial snapshot immediately
	h.sendStatsTo(conn)

	// Keep the connection alive by reading (and discarding) client messages.
	// When the client disconnects, this will return an error and we unregister.
	go func() {
		defer h.unregister(conn)
		conn.SetReadLimit(512)
		conn.SetReadDeadline(time.Now().Add(120 * time.Second))
		conn.SetPongHandler(func(string) error {
			conn.SetReadDeadline(time.Now().Add(120 * time.Second))
			return nil
		})
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				break
			}
		}
	}()

	// Start pinging to keep connection alive
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			h.mu.RLock()
			_, exists := h.clients[conn]
			h.mu.RUnlock()
			if !exists {
				return
			}
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
				return
			}
		}
	}()
}

func (h *DashboardHub) unregister(conn *websocket.Conn) {
	h.mu.Lock()
	delete(h.clients, conn)
	h.mu.Unlock()
	conn.Close()
	if wsDebug {
		log.Printf("[WS] Dashboard client disconnected (%d remaining)", h.clientCount())
	}
}

func (h *DashboardHub) clientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// NotifyUpdate is called whenever a new request is logged.
// It debounces broadcasts to max once per 500ms to avoid overloading
// on burst traffic.
func (h *DashboardHub) NotifyUpdate() {
	if h.clientCount() == 0 {
		return
	}
	if wsDebug {
		log.Printf("[WS] NotifyUpdate called, clients: %d", h.clientCount())
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if h.debounceT != nil {
		// Already have a pending broadcast, skip
		return
	}

	h.debounceT = time.AfterFunc(500*time.Millisecond, func() {
		h.mu.Lock()
		h.debounceT = nil
		h.mu.Unlock()
		h.broadcast()
	})
}

// broadcast fetches fresh stats and sends them to all connected clients.
func (h *DashboardHub) broadcast() {
	payload := h.buildPayload()
	if payload == nil {
		return
	}

	if wsDebug {
		log.Printf("[WS] Broadcasting to %d clients", h.clientCount())
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	for conn := range h.clients {
		err := conn.WriteMessage(websocket.TextMessage, payload)
		if err != nil {
			log.Printf("[WS] Write error, removing client: %v", err)
			go h.unregister(conn)
		}
	}
}

// sendStatsTo sends the current stats snapshot to a single connection.
func (h *DashboardHub) sendStatsTo(conn *websocket.Conn) {
	payload := h.buildPayload()
	if payload == nil {
		return
	}
	conn.WriteMessage(websocket.TextMessage, payload)
}

// DashboardPayload is the JSON structure sent over WebSocket to the dashboard.
type DashboardPayload struct {
	Type        string                   `json:"type"`
	Stats       map[string]interface{}   `json:"stats"`
	RecentLogs  []map[string]interface{} `json:"recent_logs"`
	ModelUsage  map[string]int           `json:"model_usage"`
	ClientStats map[string]interface{}   `json:"client_stats"`
}

func (h *DashboardHub) buildPayload() []byte {
	stats, err := h.statsService.GetGlobalStats()
	if err != nil {
		log.Printf("[WS] Failed to get stats: %v", err)
		return nil
	}

	recentLogs, err := h.statsService.GetRecentRequests("", 20)
	if err != nil {
		log.Printf("[WS] Failed to get recent requests: %v", err)
	}

	modelUsage, err := h.statsService.GetModelUsage()
	if err != nil {
		log.Printf("[WS] Failed to get model usage: %v", err)
		modelUsage = make(map[string]int)
	}
	if wsDebug {
		log.Printf("[WS] Model usage: %v", modelUsage)
	}

	clientStats, _ := h.statsService.GetAllClientStats()
	clientStatsMap := make(map[string]interface{})
	for _, cs := range clientStats {
		clientStatsMap[cs.ClientID] = map[string]interface{}{
			"requests_today": cs.RequestsToday,
			"input_tokens":   cs.InputTokensToday,
			"output_tokens":  cs.OutputTokensToday,
		}
	}

	// Convert request logs to serializable maps
	logMaps := make([]map[string]interface{}, len(recentLogs))
	for i, l := range recentLogs {
		logMaps[i] = map[string]interface{}{
			"client_id":     l.ClientID,
			"model":         l.Model,
			"status_code":   l.StatusCode,
			"input_tokens":  l.InputTokens,
			"output_tokens": l.OutputTokens,
			"latency_ms":    l.LatencyMs,
			"created_at":    l.CreatedAt.Format("Jan 02, 2006 15:04"),
			"is_streaming":  l.IsStreaming,
			"has_tools":     l.HasTools,
			"tool_names":    l.ToolNames,
			"request_body":  l.RequestBody != "",
		}
	}

	payload := DashboardPayload{
		Type: "stats_update",
		Stats: map[string]interface{}{
			"total_requests_today":      stats.TotalRequestsToday,
			"total_input_tokens_today":  stats.TotalInputTokensToday,
			"total_output_tokens_today": stats.TotalOutputTokensToday,
			"active_clients":            stats.ActiveClients,
			"total_clients":             stats.TotalClients,
			"error_rate":                stats.ErrorRate,
			"requests_in_progress":      h.statsService.GetRequestsInProgress(),
		},
		RecentLogs:  logMaps,
		ModelUsage:  modelUsage,
		ClientStats: clientStatsMap,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[WS] Failed to marshal payload: %v", err)
		return nil
	}

	return data
}
