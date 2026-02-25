package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"gemini-proxy/internal/middleware"
	"gemini-proxy/internal/services"

	"github.com/go-chi/chi/v5"
)

type OpenAIHandler struct {
	geminiService *services.GeminiService
}

func NewOpenAIHandler(geminiService *services.GeminiService) *OpenAIHandler {
	return &OpenAIHandler{geminiService: geminiService}
}

func (h *OpenAIHandler) RegisterRoutes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(middleware.Recovery)

		r.Post("/v1/chat/completions", h.ChatCompletions)
		r.Post("/v1/messages", h.ChatCompletions)
		r.Post("/v1/messages/count_tokens", h.CountTokens)
		r.Post("/chat/completions", h.ChatCompletions)
		r.Get("/v1/models", h.ListModels)
		r.Get("/v1/models/{model}", h.GetModel)
	})
}

type OpenAIChatRequest struct {
	Model       string                   `json:"model"`
	Messages    []map[string]interface{} `json:"messages"`
	MaxTokens   int                      `json:"max_tokens,omitempty"`
	Temperature float64                  `json:"temperature,omitempty"`
	Stream      bool                     `json:"stream,omitempty"`
}

type OpenAIChatResponse struct {
	ID      string                   `json:"id"`
	Object  string                   `json:"object"`
	Created int64                    `json:"created"`
	Model   string                   `json:"model"`
	Choices []map[string]interface{} `json:"choices"`
	Usage   map[string]interface{}   `json:"usage"`
}

type OpenAIModelsResponse struct {
	Object string        `json:"object"`
	Data   []OpenAIModel `json:"data"`
}

type OpenAIModel struct {
	ID         string        `json:"id"`
	Object     string        `json:"object"`
	Created    int64         `json:"created"`
	OwnedBy    string        `json:"owned_by"`
	Permission []interface{} `json:"permission,omitempty"`
}

func (h *OpenAIHandler) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	client := middleware.GetClientFromContext(r.Context())
	if client == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "Unauthorized"})
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to read request body"})
		return
	}

	var req OpenAIChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid JSON"})
		return
	}

	model := h.mapModel(req.Model)
	if model == "" {
		model = h.geminiService.GetDefaultModel()
	}

	var prompt string
	for _, msg := range req.Messages {
		role, _ := msg["role"].(string)
		content, _ := msg["content"].(string)
		prompt += role + ": " + content + "\n"
	}

	geminiReq := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"parts": []map[string]interface{}{
					{"text": prompt},
				},
			},
		},
	}

	if req.MaxTokens > 0 || req.Temperature > 0 {
		genConfig := map[string]interface{}{}
		if req.MaxTokens > 0 {
			genConfig["maxOutputTokens"] = req.MaxTokens
		}
		if req.Temperature > 0 {
			genConfig["temperature"] = req.Temperature
		}
		geminiReq["generationConfig"] = genConfig
	}

	geminiBody, _ := json.Marshal(geminiReq)

	start := time.Now()
	respBody, statusCode, err := h.geminiService.ForwardRequest(model, geminiBody)
	latencyMs := int(time.Since(start).Milliseconds())

	inputTokens, outputTokens, _ := services.ParseGeminiResponse(respBody)

	h.geminiService.LogRequest(client.ID, model, statusCode, inputTokens, outputTokens, latencyMs, "")

	var geminiResp map[string]interface{}
	json.Unmarshal(respBody, &geminiResp)

	choices := make([]map[string]interface{}, 0)
	if candidates, ok := geminiResp["candidates"].([]interface{}); ok && len(candidates) > 0 {
		if candidate, ok := candidates[0].(map[string]interface{}); ok {
			if content, ok := candidate["content"].(map[string]interface{}); ok {
				if parts, ok := content["parts"].([]interface{}); ok && len(parts) > 0 {
					if part, ok := parts[0].(map[string]interface{}); ok {
						if text, ok := part["text"].(string); ok {
							choices = append(choices, map[string]interface{}{
								"index":         0,
								"message":       map[string]string{"role": "assistant", "content": text},
								"finish_reason": "stop",
							})
						}
					}
				}
			}
		}
	}

	if len(choices) == 0 {
		choices = append(choices, map[string]interface{}{
			"index":         0,
			"message":       map[string]string{"role": "assistant", "content": ""},
			"finish_reason": "stop",
		})
	}

	usage := map[string]interface{}{
		"prompt_tokens":     inputTokens,
		"completion_tokens": outputTokens,
		"total_tokens":      inputTokens + outputTokens,
	}

	response := OpenAIChatResponse{
		ID:      "chatcmpl-" + randomID(8),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: choices,
		Usage:   usage,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}

func (h *OpenAIHandler) ListModels(w http.ResponseWriter, r *http.Request) {
	models := h.geminiService.GetAllowedModels()

	result := OpenAIModelsResponse{
		Object: "list",
		Data:   make([]OpenAIModel, len(models)),
	}

	for i, m := range models {
		result.Data[i] = OpenAIModel{
			ID:      m,
			Object:  "model",
			Created: time.Now().Unix(),
			OwnedBy: "google",
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(result)
}

func (h *OpenAIHandler) CountTokens(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to read request body"})
		return
	}

	var req struct {
		Model    string                   `json:"model"`
		Messages []map[string]interface{} `json:"messages"`
		Prompt   string                   `json:"prompt,omitempty"`
	}

	if err := json.Unmarshal(body, &req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid JSON"})
		return
	}

	text := req.Prompt
	if text == "" {
		for _, msg := range req.Messages {
			if content, ok := msg["content"].(string); ok {
				text += content + "\n"
			}
		}
	}

	estimatedTokens := len(text) / 4

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"tokens": estimatedTokens,
	})
}

func (h *OpenAIHandler) GetModel(w http.ResponseWriter, r *http.Request) {
	model := chi.URLParam(r, "model")

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(OpenAIModel{
		ID:      model,
		Object:  "model",
		Created: time.Now().Unix(),
		OwnedBy: "google",
	})
}

func (h *OpenAIHandler) mapModel(model string) string {
	mappings := map[string]string{
		"gpt-4":         "gemini-2.0-pro",
		"gpt-4-turbo":   "gemini-2.0-flash",
		"gpt-3.5-turbo": "gemini-2.0-flash-lite",
		"gpt-4o":        "gemini-2.0-pro",
		"gpt-4o-mini":   "gemini-2.0-flash-lite",
		"o1":            "gemini-2.0-pro",
		"o1-mini":       "gemini-2.0-flash",
	}

	if m, ok := mappings[model]; ok {
		return m
	}

	for _, allowed := range h.geminiService.GetAllowedModels() {
		if strings.Contains(allowed, model) || strings.Contains(model, allowed) {
			return allowed
		}
	}

	return ""
}

func randomID(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[i%len(charset)]
	}
	return string(b)
}
