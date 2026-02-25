package handlers

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"ai-gateway/internal/middleware"
	"ai-gateway/internal/models"
	"ai-gateway/internal/providers"
	"ai-gateway/internal/services"

	"github.com/go-chi/chi/v5"
)

type OpenAIHandler struct {
	geminiService *services.GeminiService
	registry      *providers.Registry
}

func NewOpenAIHandler(geminiService *services.GeminiService, registry *providers.Registry) *OpenAIHandler {
	return &OpenAIHandler{geminiService: geminiService, registry: registry}
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

// writeOpenAIError sends an OpenAI-compatible error response with the appropriate HTTP status code.
func writeOpenAIError(w http.ResponseWriter, statusCode int, errMsg, errType string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("x-request-id", "req-"+randomID(12))
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{
			"message": errMsg,
			"type":    errType,
			"code":    nil,
		},
	})
}

// mapUpstreamStatusToHTTP converts upstream API status codes to appropriate HTTP status codes
// for the OpenAI-compatible response.
func mapUpstreamStatusToHTTP(geminiStatus int) int {
	switch {
	case geminiStatus == 429:
		return http.StatusTooManyRequests
	case geminiStatus == 403:
		return http.StatusForbidden
	case geminiStatus == 401:
		return http.StatusUnauthorized
	case geminiStatus >= 500:
		return http.StatusBadGateway
	default:
		return http.StatusInternalServerError
	}
}

// resolveProvider returns the appropriate provider for the given client.
func (h *OpenAIHandler) resolveProvider(client *models.Client) (providers.Provider, error) {
	backend := client.Backend
	if backend == "" {
		backend = "gemini"
	}
	return h.registry.GetWithOverride(backend, client.BackendBaseURL)
}

func (h *OpenAIHandler) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	client := middleware.GetClientFromContext(r.Context())
	if client == nil {
		writeOpenAIError(w, http.StatusUnauthorized, "Unauthorized", "authentication_error")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "Failed to read request body", "invalid_request_error")
		return
	}

	var req OpenAIChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "Invalid JSON in request body", "invalid_request_error")
		return
	}

	provider, err := h.resolveProvider(client)
	if err != nil {
		log.Printf("[CHAT] Provider error for client %s: %v", client.Name, err)
		writeOpenAIError(w, http.StatusBadRequest, "Backend not configured: "+err.Error(), "invalid_request_error")
		return
	}

	log.Printf("[CHAT] Client: %s, Backend: %s, Model: %s, Messages: %d, Stream: %v", client.Name, provider.Name(), req.Model, len(req.Messages), req.Stream)

	// Build internal chat request from the OpenAI format, injecting the client's system prompt if set
	chatReq := h.buildChatRequest(req, provider, client)

	if len(chatReq.Messages) == 0 {
		writeOpenAIError(w, http.StatusBadRequest, "No content in messages", "invalid_request_error")
		return
	}

	log.Printf("[CHAT] Resolved model: %s, messages: %d", chatReq.Model, len(chatReq.Messages))

	if req.Stream {
		h.handleStreamingRequest(w, r, client, req, provider, chatReq)
		return
	}

	h.handleNonStreamingRequest(w, client, req, provider, chatReq)
}

// buildChatRequest converts the OpenAI-format request into our internal ChatRequest,
// resolving the model name through the provider. If the client has a SystemPrompt
// configured, it is prepended as a system message to every request.
func (h *OpenAIHandler) buildChatRequest(req OpenAIChatRequest, provider providers.Provider, client *models.Client) *providers.ChatRequest {
	model := req.Model
	if model == "" {
		model = provider.DefaultModel()
	}

	messages := make([]providers.ChatMessage, 0, len(req.Messages)+1)

	// Inject per-client system prompt if configured.
	// It goes first so the client's own system messages (if any) can extend or override it.
	if client.SystemPrompt != "" {
		messages = append(messages, providers.ChatMessage{Role: "system", Content: client.SystemPrompt})
	}

	for _, msg := range req.Messages {
		role, _ := msg["role"].(string)
		content, _ := msg["content"].(string)
		if content == "" {
			continue
		}
		messages = append(messages, providers.ChatMessage{Role: role, Content: content})
	}

	return &providers.ChatRequest{
		Model:       model,
		Messages:    messages,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		Stream:      req.Stream,
	}
}

// handleNonStreamingRequest sends the request through the provider and returns
// the full response as an OpenAI-compatible JSON response.
func (h *OpenAIHandler) handleNonStreamingRequest(w http.ResponseWriter, client *models.Client, req OpenAIChatRequest, provider providers.Provider, chatReq *providers.ChatRequest) {
	start := time.Now()
	respBody, statusCode, err := provider.ChatCompletion(chatReq)
	latencyMs := int(time.Since(start).Milliseconds())

	if err != nil {
		log.Printf("[CHAT] %s request error: %v", provider.Name(), err)
		writeOpenAIError(w, http.StatusBadGateway, "Upstream request failed: "+err.Error(), "api_error")
		return
	}

	log.Printf("[CHAT] %s response status: %d, latency: %dms", provider.Name(), statusCode, latencyMs)

	if statusCode >= 400 {
		errMsg := extractErrorMessage(respBody)
		log.Printf("[CHAT] %s error: %s", provider.Name(), errMsg)
		httpStatus := mapUpstreamStatusToHTTP(statusCode)
		writeOpenAIError(w, httpStatus, errMsg, "api_error")
		return
	}

	responseText, inputTokens, outputTokens, _ := provider.ParseResponse(respBody)
	h.geminiService.LogRequest(client.ID, chatReq.Model, statusCode, inputTokens, outputTokens, latencyMs, "")

	responseID := "chatcmpl-" + randomID(12)
	log.Printf("[CHAT] Sending response: text length=%d", len(responseText))

	response := OpenAIChatResponse{
		ID:      responseID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []map[string]interface{}{
			{
				"index":         0,
				"message":       map[string]interface{}{"role": "assistant", "content": responseText},
				"finish_reason": "stop",
			},
		},
		Usage: map[string]interface{}{
			"prompt_tokens":     inputTokens,
			"completion_tokens": outputTokens,
			"total_tokens":      inputTokens + outputTokens,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("x-request-id", responseID)
	w.Header().Set("openai-processing-ms", fmt.Sprintf("%d", latencyMs))
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}

// handleStreamingRequest sends a streaming request through the provider,
// reads SSE chunks, and translates them to OpenAI-format SSE in real time.
func (h *OpenAIHandler) handleStreamingRequest(w http.ResponseWriter, r *http.Request, client *models.Client, req OpenAIChatRequest, provider providers.Provider, chatReq *providers.ChatRequest) {
	start := time.Now()

	resp, err := provider.ChatCompletionStream(chatReq)
	if err != nil {
		log.Printf("[CHAT] %s stream error: %v", provider.Name(), err)
		writeOpenAIError(w, http.StatusBadGateway, "Upstream request failed: "+err.Error(), "api_error")
		return
	}
	defer resp.Body.Close()

	// If provider returned an error status, read the body and return error
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		latencyMs := int(time.Since(start).Milliseconds())
		log.Printf("[CHAT] %s stream error status: %d, latency: %dms", provider.Name(), resp.StatusCode, latencyMs)

		errMsg := extractErrorMessage(body)
		log.Printf("[CHAT] %s error: %s", provider.Name(), errMsg)
		httpStatus := mapUpstreamStatusToHTTP(resp.StatusCode)
		writeOpenAIError(w, httpStatus, errMsg, "api_error")
		return
	}

	// Set up SSE headers for the client
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	responseID := "chatcmpl-" + randomID(12)
	created := time.Now().Unix()
	w.Header().Set("x-request-id", responseID)
	w.WriteHeader(http.StatusOK)

	flusher, ok := w.(http.Flusher)
	if !ok {
		log.Printf("[CHAT] ResponseWriter does not implement http.Flusher")
		return
	}

	// Send the initial role chunk
	sendSSEChunk(w, flusher, responseID, req.Model, created, map[string]interface{}{"role": "assistant", "content": ""}, nil)

	// Read upstream SSE stream and forward chunks
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 256*1024), 256*1024)

	prefix := provider.StreamDataPrefix()
	var inputTokens, outputTokens int
	chunkCount := 0

	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, prefix) {
			continue
		}

		jsonData := strings.TrimPrefix(line, prefix)
		if jsonData == "" || jsonData == "[DONE]" {
			continue
		}

		text, it, ot := provider.ParseStreamChunk([]byte(jsonData))
		if it > 0 {
			inputTokens = it
		}
		if ot > 0 {
			outputTokens = ot
		}

		if text != "" {
			chunkCount++
			sendSSEChunk(w, flusher, responseID, req.Model, created, map[string]interface{}{"content": text}, nil)
		}
	}

	latencyMs := int(time.Since(start).Milliseconds())
	log.Printf("[CHAT] Stream completed: %d chunks, %d input tokens, %d output tokens, latency: %dms", chunkCount, inputTokens, outputTokens, latencyMs)

	// Send the final stop chunk with usage info
	finalChunk := map[string]interface{}{
		"id":      responseID,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   req.Model,
		"choices": []map[string]interface{}{
			{
				"index":         0,
				"delta":         map[string]interface{}{},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]interface{}{
			"prompt_tokens":     inputTokens,
			"completion_tokens": outputTokens,
			"total_tokens":      inputTokens + outputTokens,
		},
	}
	data, _ := json.Marshal(finalChunk)
	fmt.Fprintf(w, "data: %s\n\n", data)
	fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()

	// Log the request after streaming completes
	h.geminiService.LogRequest(client.ID, chatReq.Model, resp.StatusCode, inputTokens, outputTokens, latencyMs, "")
}

// sendSSEChunk writes a single OpenAI-format SSE chunk to the client.
func sendSSEChunk(w http.ResponseWriter, flusher http.Flusher, id, model string, created int64, delta map[string]interface{}, finishReason interface{}) {
	chunk := map[string]interface{}{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []map[string]interface{}{
			{
				"index":         0,
				"delta":         delta,
				"finish_reason": finishReason,
			},
		},
	}
	data, _ := json.Marshal(chunk)
	fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()
}

// extractErrorMessage extracts an error message from an upstream error response body.
// Tries OpenAI format, Gemini format, and Anthropic format.
func extractErrorMessage(body []byte) string {
	var geminiErr map[string]interface{}
	if err := json.Unmarshal(body, &geminiErr); err != nil {
		return "Upstream API error"
	}
	// OpenAI/Gemini format: {"error": {"message": "..."}}
	if errObj, ok := geminiErr["error"].(map[string]interface{}); ok {
		if msg, ok := errObj["message"].(string); ok {
			return msg
		}
		// Sometimes error is a string directly
		if msg, ok := geminiErr["error"].(string); ok {
			return msg
		}
	}
	// Anthropic format: {"type": "error", "error": {"message": "..."}}
	if t, ok := geminiErr["type"].(string); ok && t == "error" {
		if errObj, ok := geminiErr["error"].(map[string]interface{}); ok {
			if msg, ok := errObj["message"].(string); ok {
				return msg
			}
		}
	}
	// Fallback: look for "message" at top level
	if msg, ok := geminiErr["message"].(string); ok {
		return msg
	}
	return "Upstream API error"
}

func (h *OpenAIHandler) ListModels(w http.ResponseWriter, r *http.Request) {
	var allModels []OpenAIModel

	for _, name := range h.registry.Names() {
		provider, err := h.registry.Get(name)
		if err != nil {
			continue
		}
		for _, m := range provider.Models() {
			allModels = append(allModels, OpenAIModel{
				ID:      m,
				Object:  "model",
				Created: time.Now().Unix(),
				OwnedBy: name,
			})
		}
	}

	result := OpenAIModelsResponse{
		Object: "list",
		Data:   allModels,
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
		OwnedBy: "ai-gateway",
	})
}

func randomID(length int) string {
	b := make([]byte, (length+1)/2)
	rand.Read(b)
	return hex.EncodeToString(b)[:length]
}
