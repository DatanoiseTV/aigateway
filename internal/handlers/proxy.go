package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"gemini-proxy/internal/middleware"
	"gemini-proxy/internal/models"
	"gemini-proxy/internal/services"

	"github.com/go-chi/chi/v5"
)

type ProxyHandler struct {
	geminiService *services.GeminiService
}

func NewProxyHandler(geminiService *services.GeminiService) *ProxyHandler {
	return &ProxyHandler{geminiService: geminiService}
}

func (h *ProxyHandler) RegisterRoutes(r chi.Router) {
	r.Route("/v1beta", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(middleware.Recovery)

			r.Post("/models/{model}:generateContent", h.GenerateContent)
			r.Post("/models/{model}:streamGenerateContent", h.StreamGenerateContent)
			r.Get("/models", h.ListModels)
			r.Get("/models/{model}", h.GetModel)
		})
	})
}

func (h *ProxyHandler) GenerateContent(w http.ResponseWriter, r *http.Request) {
	client := middleware.GetClientFromContext(r.Context())
	if client == nil {
		http.Error(w, `{"error": "Unauthorized"}`, http.StatusUnauthorized)
		return
	}

	model := chi.URLParam(r, "model")
	if model == "" {
		model = "gemini-flash-lite-latest"
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, `{"error": "Failed to read request body"}`, http.StatusBadRequest)
		return
	}

	if err := h.enforceRequestLimits(client, body); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Error-Code", "REQUEST_LIMIT_EXCEEDED")
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	body = h.capOutputTokens(client, body)

	start := time.Now()
	respBody, statusCode, err := h.geminiService.ForwardRequest(model, body)
	latencyMs := int(time.Since(start).Milliseconds())

	inputTokens, outputTokens, _ := services.ParseGeminiResponse(respBody)

	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}

	h.geminiService.LogRequest(client.ID, model, statusCode, inputTokens, outputTokens, latencyMs, errMsg)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-RateLimit-Limit-Minute", string(rune(client.RateLimitMinute)))
	w.Header().Set("X-RateLimit-Limit-Hour", string(rune(client.RateLimitHour)))
	w.Header().Set("X-RateLimit-Limit-Day", string(rune(client.RateLimitDay)))
	w.Header().Set("X-TokenLimit-Input", string(rune(client.MaxInputTokens)))
	w.Header().Set("X-TokenLimit-Output", string(rune(client.MaxOutputTokens)))

	w.WriteHeader(statusCode)
	if respBody != nil {
		w.Write(respBody)
	}
}

func (h *ProxyHandler) enforceRequestLimits(client *models.Client, body []byte) error {
	inputTokens := estimateInputTokens(string(body))

	if client.MaxInputTokens > 0 && inputTokens > client.MaxInputTokens {
		return &APIError{
			Err: APIErrorBody{
				Message: "Input token count exceeds limit",
				Code:    "MAX_INPUT_TOKENS_EXCEEDED",
				Status:  "INVALID_ARGUMENT",
				Details: []map[string]interface{}{
					{"limit": client.MaxInputTokens, "received": inputTokens},
				},
			},
		}
	}

	return nil
}

func (h *ProxyHandler) capOutputTokens(client *models.Client, body []byte) []byte {
	if client.MaxOutputTokens == 0 {
		return body
	}

	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		return body
	}

	generationConfig, ok := req["generationConfig"].(map[string]interface{})
	if !ok {
		req["generationConfig"] = map[string]interface{}{
			"maxOutputTokens": client.MaxOutputTokens,
		}
	} else {
		if current, ok := generationConfig["maxOutputTokens"].(float64); !ok || int(current) > client.MaxOutputTokens {
			generationConfig["maxOutputTokens"] = client.MaxOutputTokens
		}
	}

	newBody, _ := json.Marshal(req)
	return newBody
}

func estimateInputTokens(text string) int {
	return len(text) / 4
}

func (h *ProxyHandler) StreamGenerateContent(w http.ResponseWriter, r *http.Request) {
	client := middleware.GetClientFromContext(r.Context())
	if client == nil {
		http.Error(w, `{"error": "Unauthorized"}`, http.StatusUnauthorized)
		return
	}

	model := chi.URLParam(r, "model")
	if model == "" {
		model = "gemini-flash-lite-latest"
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, `{"error": "Failed to read request body"}`, http.StatusBadRequest)
		return
	}

	if err := h.enforceRequestLimits(client, body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	body = h.capOutputTokens(client, body)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Transfer-Encoding", "chunked")

	baseURL := h.geminiService.GetBaseURL()
	url := baseURL + "/models/" + model + ":streamGenerateContent?key="

	req, err := http.NewRequest("POST", url, strings.NewReader(string(body)))
	if err != nil {
		http.Error(w, `{"error": "Failed to create request"}`, http.StatusInternalServerError)
		return
	}

	req.Header.Set("Content-Type", "application/json")

	clientHTTP := &http.Client{
		Timeout: 120 * time.Second,
	}

	resp, err := clientHTTP.Do(req)
	if err != nil {
		http.Error(w, `{"error": "Failed to forward request"}`, http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, `{"error": "Streaming not supported"}`, http.StatusInternalServerError)
		return
	}

	buffer := make([]byte, 1024)
	for {
		n, err := resp.Body.Read(buffer)
		if n > 0 {
			w.Write(buffer[:n])
			flusher.Flush()
		}
		if err != nil {
			break
		}
	}
}

func (h *ProxyHandler) ListModels(w http.ResponseWriter, r *http.Request) {
	models := h.geminiService.GetAllowedModels()

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"models":[`))
	for i, m := range models {
		if i > 0 {
			w.Write([]byte(","))
		}
		w.Write([]byte(`{"name":"` + m + `","version":"v1","displayName":"` + m + `"}`))
	}
	w.Write([]byte(`]}`))
}

func (h *ProxyHandler) GetModel(w http.ResponseWriter, r *http.Request) {
	model := chi.URLParam(r, "model")

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"name":"` + model + `","version":"v1","displayName":"` + model + `"}`))
}

type APIError struct {
	Err APIErrorBody `json:"error"`
}

type APIErrorBody struct {
	Message string                   `json:"message"`
	Code    string                   `json:"code"`
	Status  string                   `json:"status"`
	Details []map[string]interface{} `json:"details,omitempty"`
}

func (e *APIError) Error() string {
	b, _ := json.Marshal(e.Err)
	return string(b)
}
