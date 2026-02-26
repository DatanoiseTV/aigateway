package handlers

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"ai-gateway/internal/services"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	requestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ai_gateway_requests_total",
			Help: "Total number of requests",
		},
		[]string{"client_id", "model", "status"},
	)

	requestsInProgress = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "ai_gateway_requests_in_progress",
		Help: "Number of requests currently being processed",
	})

	inputTokensTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ai_gateway_input_tokens_total",
			Help: "Total number of input tokens",
		},
		[]string{"client_id", "model"},
	)

	outputTokensTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ai_gateway_output_tokens_total",
			Help: "Total number of output tokens",
		},
		[]string{"client_id", "model"},
	)

	requestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "ai_gateway_request_duration_seconds",
			Help:    "Request duration in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"client_id", "model"},
	)

	activeClients = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "ai_gateway_active_clients",
		Help: "Number of active clients",
	})

	upstreamErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ai_gateway_upstream_errors_total",
			Help: "Total number of upstream errors",
		},
		[]string{"client_id", "model", "provider"},
	)
)

func init() {
	if err := prometheus.Register(requestsTotal); err != nil {
		log.Printf("[METRICS] Failed to register requestsTotal: %v", err)
	}
	if err := prometheus.Register(requestsInProgress); err != nil {
		log.Printf("[METRICS] Failed to register requestsInProgress: %v", err)
	}
	if err := prometheus.Register(inputTokensTotal); err != nil {
		log.Printf("[METRICS] Failed to register inputTokensTotal: %v", err)
	}
	if err := prometheus.Register(outputTokensTotal); err != nil {
		log.Printf("[METRICS] Failed to register outputTokensTotal: %v", err)
	}
	if err := prometheus.Register(requestDuration); err != nil {
		log.Printf("[METRICS] Failed to register requestDuration: %v", err)
	}
	if err := prometheus.Register(activeClients); err != nil {
		log.Printf("[METRICS] Failed to register activeClients: %v", err)
	}
	if err := prometheus.Register(upstreamErrors); err != nil {
		log.Printf("[METRICS] Failed to register upstreamErrors: %v", err)
	}
	log.Println("[METRICS] Registered custom Prometheus metrics")
}

type MetricsHandler struct {
	statsService *services.StatsService
	username     string
	password     string
	promHandler  http.Handler
}

func NewMetricsHandler(statsService *services.StatsService, username, password string) *MetricsHandler {
	return &MetricsHandler{
		statsService: statsService,
		username:     username,
		password:     password,
		promHandler:  promhttp.Handler(),
	}
}

func (h *MetricsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.authenticate(w, r) {
		return
	}

	h.promHandler.ServeHTTP(w, r)
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
