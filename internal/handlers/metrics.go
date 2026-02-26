package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"ai-gateway/internal/services"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	requestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ai_gateway_requests_total",
			Help: "Total number of requests",
		},
		[]string{"client_id", "model", "status"},
	)

	requestsInProgress = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "ai_gateway_requests_in_progress",
		Help: "Number of requests currently being processed",
	})

	inputTokensTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ai_gateway_input_tokens_total",
			Help: "Total number of input tokens",
		},
		[]string{"client_id", "model"},
	)

	outputTokensTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ai_gateway_output_tokens_total",
			Help: "Total number of output tokens",
		},
		[]string{"client_id", "model"},
	)

	requestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "ai_gateway_request_duration_seconds",
			Help:    "Request duration in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"client_id", "model"},
	)

	activeClients = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "ai_gateway_active_clients",
		Help: "Number of active clients",
	})

	upstreamErrors = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ai_gateway_upstream_errors_total",
			Help: "Total number of upstream errors",
		},
		[]string{"client_id", "model", "provider"},
	)
)

type MetricsHandler struct {
	statsService *services.StatsService
	username     string
	password     string
}

func NewMetricsHandler(statsService *services.StatsService, username, password string) *MetricsHandler {
	return &MetricsHandler{
		statsService: statsService,
		username:     username,
		password:     password,
	}
}

func (h *MetricsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.authenticate(w, r) {
		return
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`# HELP ai_gateway_requests_total Total number of requests
# TYPE ai_gateway_requests_total counter
`))

	stats, err := h.statsService.GetGlobalStats()
	if err == nil && stats != nil {
		clientStats, _ := h.statsService.GetAllClientStats()
		for _, cs := range clientStats {
			model := "unknown"
			if cs.RequestsToday > 0 {
				model = "all"
			}
			fmt.Fprintf(w, "ai_gateway_requests_total{client_id=\"%s\",model=\"%s\",status=\"200\"} %d\n", cs.ClientID, model, cs.RequestsToday)
		}
		fmt.Fprintf(w, "ai_gateway_active_clients %d\n", stats.ActiveClients)
	}
}

func (h *MetricsHandler) authenticate(w http.ResponseWriter, r *http.Request) bool {
	username, password, ok := r.BasicAuth()
	if !ok || username != h.username || password != h.password {
		w.Header().Set("WWW-Authenticate", `Basic realm="Prometheus Metrics"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

type PrometheusConfig struct {
	Enabled  bool   `yaml:"enabled" json:"enabled"`
	Username string `yaml:"username" json:"username"`
	Password string `yaml:"password" json:"password"`
}

func (h *MetricsHandler) RegisterRoutes(r chi.Router) {
	r.Get("/metrics", h.ServeHTTP)
}

func RecordRequest(clientID, model, status string, inputTokens, outputTokens int, latencyMs int) {
	requestsTotal.WithLabelValues(clientID, model, status).Inc()
	inputTokensTotal.WithLabelValues(clientID, model).Add(float64(inputTokens))
	outputTokensTotal.WithLabelValues(clientID, model).Add(float64(outputTokens))
	requestDuration.WithLabelValues(clientID, model).Observe(float64(latencyMs) / 1000)
}

func RecordUpstreamError(clientID, model, provider string) {
	upstreamErrors.WithLabelValues(clientID, model, provider).Inc()
}

func SetRequestsInProgress(n int64) {
	requestsInProgress.Set(float64(n))
}

var clientModelsCache struct {
	sync.RWMutex
	models map[string][]string
}

func GetCachedClientModels() map[string][]string {
	clientModelsCache.RLock()
	defer clientModelsCache.RUnlock()
	return clientModelsCache.models
}

func UpdateClientModelsCache(clientModels map[string][]string) {
	clientModelsCache.Lock()
	defer clientModelsCache.Unlock()
	clientModelsCache.models = clientModels
}

type ClientModelsResponse struct {
	Models map[string][]string `json:"models"`
}

func (h *MetricsHandler) ServeClientModels(w http.ResponseWriter, r *http.Request) {
	if !h.authenticate(w, r) {
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ClientModelsResponse{
		Models: GetCachedClientModels(),
	})
}

func (h *MetricsHandler) RegisterClientModelsRoutes(r chi.Router) {
	r.Get("/client-models", h.ServeClientModels)
}
