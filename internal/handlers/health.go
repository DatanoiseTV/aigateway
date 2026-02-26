package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

type HealthHandler struct {
	db *gorm.DB
}

func NewHealthHandler(db *gorm.DB) *HealthHandler {
	return &HealthHandler{db: db}
}

func (h *HealthHandler) RegisterRoutes(r chi.Router) {
	r.Get("/health", h.Health)
	r.Get("/health/ready", h.Ready)
	r.Get("/health/live", h.Live)
}

func (h *HealthHandler) Health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(HealthResponse{
		Status:    "ok",
		Timestamp: time.Now().Unix(),
	})
}

func (h *HealthHandler) Ready(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	checks := make(map[string]CheckResult)
	allHealthy := true

	// Check database
	dbCheck := h.checkDatabase()
	checks["database"] = dbCheck
	if !dbCheck.Healthy {
		allHealthy = false
	}

	response := HealthDetailResponse{
		Status:    "ready",
		Timestamp: time.Now().Unix(),
		Checks:    checks,
	}

	if !allHealthy {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	json.NewEncoder(w).Encode(response)
}

func (h *HealthHandler) Live(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

func (h *HealthHandler) checkDatabase() CheckResult {
	sqlDB, err := h.db.DB()
	if err != nil {
		return CheckResult{
			Healthy: false,
			Message: "Failed to get database connection: " + err.Error(),
		}
	}

	if err := sqlDB.Ping(); err != nil {
		return CheckResult{
			Healthy: false,
			Message: "Database ping failed: " + err.Error(),
		}
	}

	return CheckResult{
		Healthy: true,
		Message: "Database connected",
	}
}

type HealthResponse struct {
	Status    string `json:"status"`
	Timestamp int64  `json:"timestamp"`
}

type HealthDetailResponse struct {
	Status    string                 `json:"status"`
	Timestamp int64                  `json:"timestamp"`
	Checks    map[string]CheckResult `json:"checks"`
}

type CheckResult struct {
	Healthy bool   `json:"healthy"`
	Message string `json:"message"`
}
