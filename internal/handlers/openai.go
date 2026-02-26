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

	"ai-gateway/internal/config"
	"ai-gateway/internal/middleware"
	"ai-gateway/internal/models"
	"ai-gateway/internal/providers"
	"ai-gateway/internal/services"

	"github.com/go-chi/chi/v5"
)

type OpenAIHandler struct {
	geminiService *services.GeminiService
	clientService *services.ClientService
	statsService  *services.StatsService
	registry      *providers.Registry
	toolService   *services.ToolService
}

func NewOpenAIHandler(geminiService *services.GeminiService, clientService *services.ClientService, statsService *services.StatsService, registry *providers.Registry, toolService *services.ToolService) *OpenAIHandler {
	return &OpenAIHandler{geminiService: geminiService, clientService: clientService, statsService: statsService, registry: registry, toolService: toolService}
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
	Model          string                   `json:"model"`
	Messages       []map[string]interface{} `json:"messages"`
	MaxTokens      int                      `json:"max_tokens,omitempty"`
	Temperature    float64                  `json:"temperature,omitempty"`
	Stream         bool                     `json:"stream,omitempty"`
	Tools          []map[string]interface{} `json:"tools,omitempty"`
	ResponseFormat any                      `json:"response_format,omitempty"`
	StreamOptions  *StreamOptions           `json:"stream_options,omitempty"`
}

type StreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
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
// If the client has its own API key configured, a provider is built from
// the client's settings. Otherwise we fall back to the global registry.
func (h *OpenAIHandler) resolveProvider(client *models.Client) (providers.Provider, error) {
	backend := client.Backend
	if backend == "" {
		backend = "gemini"
	}

	// If the client has a per-client API key or base URL, build a dedicated provider
	if client.BackendAPIKey != "" || client.BackendBaseURL != "" {
		cfg := config.ProviderConfig{
			Type:           backend,
			APIKey:         client.BackendAPIKey,
			BaseURL:        client.BackendBaseURL,
			DefaultModel:   client.BackendDefaultModel,
			TimeoutSeconds: 120,
		}
		// If client has no API key but does have a base URL override,
		// inherit the API key from the global provider if one exists
		if cfg.APIKey == "" {
			if globalP := h.geminiService.GetConfig().GetProvider(backend); globalP != nil {
				cfg.APIKey = globalP.APIKey
			}
		}
		if cfg.DefaultModel == "" {
			if globalP := h.geminiService.GetConfig().GetProvider(backend); globalP != nil {
				cfg.DefaultModel = globalP.DefaultModel
			}
		}
		return providers.BuildSingleProvider(backend, cfg)
	}

	// Fall back to the global registry
	return h.registry.Get(backend)
}

// updateClientModels fetches available models from the provider and stores them in the client record.
func (h *OpenAIHandler) updateClientModels(client *models.Client, provider providers.Provider) {
	models, err := provider.FetchModels()
	if err != nil {
		log.Printf("[%s] Failed to fetch models for client %s: %v", provider.Name(), client.Name, err)
		return
	}

	if len(models) == 0 {
		return
	}

	// Store models as JSON
	modelsJSON, err := json.Marshal(models)
	if err != nil {
		log.Printf("[%s] Failed to marshal models for client %s: %v", provider.Name(), client.Name, err)
		return
	}

	client.BackendModels = string(modelsJSON)
	if err := h.clientService.UpdateClient(client); err != nil {
		log.Printf("[%s] Failed to update models for client %s: %v", provider.Name(), client.Name, err)
	}
	log.Printf("[%s] Updated models for client %s: %v", provider.Name(), client.Name, models)
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

	if h.statsService != nil {
		h.statsService.IncrementRequestsInProgress()
	}

	var req OpenAIChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "Invalid JSON in request body", "invalid_request_error")
		if h.statsService != nil {
			h.statsService.DecrementRequestsInProgress()
		}
		return
	}

	provider, err := h.resolveProvider(client)
	if err != nil {
		log.Printf("[CHAT] Provider error for client %s: %v", client.Name, err)
		writeOpenAIError(w, http.StatusBadRequest, "Backend not configured: "+err.Error(), "invalid_request_error")
		if h.statsService != nil {
			h.statsService.DecrementRequestsInProgress()
		}
		return
	}

	log.Printf("[CHAT] Client: %s, Backend: %s, Model: %s, Messages: %d, Stream: %v", client.Name, provider.Name(), req.Model, len(req.Messages), req.Stream)

	// Build internal chat request from the OpenAI format, injecting the client's system prompt if set
	chatReq := h.buildChatRequest(req, provider, client)

	if len(chatReq.Messages) == 0 {
		writeOpenAIError(w, http.StatusBadRequest, "No content in messages", "invalid_request_error")
		if h.statsService != nil {
			h.statsService.DecrementRequestsInProgress()
		}
		return
	}

	log.Printf("[CHAT] Resolved model: %s, messages: %d, tools: %d", chatReq.Model, len(chatReq.Messages), len(chatReq.Tools))
	if len(chatReq.Tools) > 0 {
		for _, t := range chatReq.Tools {
			if t.Function != nil {
				log.Printf("[CHAT] Tool available: %s - %s", t.Function.Name, t.Function.Description)
			}
		}
	}

	// Handle fallback models
	fallbackModels := parseFallbackModels(client.FallbackModels)
	if len(fallbackModels) > 0 {
		log.Printf("[CHAT] Fallback models configured: %v", fallbackModels)
	}

	if req.Stream {
		h.handleStreamingRequestWithFallback(w, r, client, req, provider, chatReq, string(body), fallbackModels)
		return
	}

	h.handleNonStreamingRequestWithFallback(w, client, req, provider, chatReq, string(body), fallbackModels)
}

// parseFallbackModels parses comma-separated fallback model names
func parseFallbackModels(fallbackStr string) []string {
	if fallbackStr == "" {
		return nil
	}
	var models []string
	for _, m := range strings.Split(fallbackStr, ",") {
		m = strings.TrimSpace(m)
		if m != "" {
			models = append(models, m)
		}
	}
	return models
}

// isRetryableError checks if an error warrants trying a fallback model
func isRetryableError(statusCode int, errMsg string) bool {
	// Retry on rate limit errors
	if statusCode == 429 {
		return true
	}
	// Retry on upstream server errors
	if statusCode >= 500 && statusCode <= 599 {
		return true
	}
	// Retry on specific error messages
	lowerErr := strings.ToLower(errMsg)
	if strings.Contains(lowerErr, "rate limit") ||
		strings.Contains(lowerErr, "quota") ||
		strings.Contains(lowerErr, "too many requests") ||
		strings.Contains(lowerErr, "insufficient_quota") ||
		strings.Contains(lowerErr, "billing") {
		return true
	}
	return false
}

// buildChatRequest converts the OpenAI-format request into our internal ChatRequest,
// resolving the model name through the provider. If the client has a SystemPrompt
// configured, it is prepended as a system message to every request.
func (h *OpenAIHandler) buildChatRequest(req OpenAIChatRequest, provider providers.Provider, client *models.Client) *providers.ChatRequest {
	model := req.Model
	// Priority: request model -> client default model -> provider default model
	if model == "" {
		if client.BackendDefaultModel != "" {
			model = client.BackendDefaultModel
		} else {
			model = provider.DefaultModel()
		}
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
		toolCallID, _ := msg["tool_call_id"].(string)

		// Handle tool result messages
		if role == "tool" {
			messages = append(messages, providers.ChatMessage{
				Role:       role,
				Content:    content,
				ToolCallID: toolCallID,
			})
			continue
		}

		// Handle assistant messages with tool_calls (from previous tool call)
		if toolCallsRaw, ok := msg["tool_calls"].([]interface{}); ok && len(toolCallsRaw) > 0 {
			toolCalls := make([]providers.ToolCall, len(toolCallsRaw))
			for i, tcRaw := range toolCallsRaw {
				if tcMap, ok := tcRaw.(map[string]interface{}); ok {
					if fn, ok := tcMap["function"].(map[string]interface{}); ok {
						toolCalls[i] = providers.ToolCall{
							ID:        getString(tcMap, "id"),
							Name:      getString(fn, "name"),
							Arguments: getString(fn, "arguments"),
						}
					}
				}
			}
			messages = append(messages, providers.ChatMessage{
				Role:      role,
				Content:   content,
				ToolCalls: toolCalls,
			})
			continue
		}

		if content == "" {
			continue
		}
		messages = append(messages, providers.ChatMessage{Role: role, Content: content})
	}

	return &providers.ChatRequest{
		Model:          model,
		Messages:       messages,
		MaxTokens:      req.MaxTokens,
		Temperature:    req.Temperature,
		Stream:         req.Stream,
		Tools:          convertTools(req.Tools),
		ResponseFormat: req.ResponseFormat,
		StreamOptions: func() *providers.StreamOptions {
			if req.StreamOptions != nil {
				return &providers.StreamOptions{IncludeUsage: req.StreamOptions.IncludeUsage}
			}
			return nil
		}(),
	}
}

func convertTools(tools []map[string]interface{}) []providers.Tool {
	if tools == nil {
		return nil
	}
	result := make([]providers.Tool, len(tools))
	for i, t := range tools {
		result[i] = providers.Tool{Type: "function"}
		if fn, ok := t["function"].(map[string]interface{}); ok {
			result[i].Function = &providers.ToolFunction{
				Name:        getString(fn, "name"),
				Description: getString(fn, "description"),
				Parameters:  fn["parameters"],
			}
		}
	}
	return result
}

func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// handleNonStreamingRequest sends the request through the provider and returns
// the full response as an OpenAI-compatible JSON response.
func (h *OpenAIHandler) handleNonStreamingRequest(w http.ResponseWriter, client *models.Client, req OpenAIChatRequest, provider providers.Provider, chatReq *providers.ChatRequest, requestBody string) {
	start := time.Now()
	maxToolIterations := 5
	var toolNames []string

	respBody, statusCode, err := provider.ChatCompletion(chatReq)
	latencyMs := int(time.Since(start).Milliseconds())

	if err != nil {
		log.Printf("[CHAT] %s request error: %v", provider.Name(), err)
		writeOpenAIError(w, http.StatusBadGateway, "Upstream request failed: "+err.Error(), "api_error")
		if h.statsService != nil {
			h.statsService.DecrementRequestsInProgress()
		}
		return
	}

	log.Printf("[CHAT] %s response status: %d, latency: %dms", provider.Name(), statusCode, latencyMs)

	if statusCode >= 400 {
		errMsg := extractErrorMessage(respBody)
		log.Printf("[CHAT] %s error: %s", provider.Name(), errMsg)
		httpStatus := mapUpstreamStatusToHTTP(statusCode)
		writeOpenAIError(w, httpStatus, errMsg, "api_error")
		if h.statsService != nil {
			h.statsService.DecrementRequestsInProgress()
		}
		return
	}

	// Tool execution loop
	for iteration := 0; iteration < maxToolIterations; iteration++ {
		toolCalls, err := provider.ParseToolCalls(respBody)
		if err != nil {
			log.Printf("[CHAT] %s parse tool calls error: %v", provider.Name(), err)
			break
		}

		if len(toolCalls) == 0 {
			// Debug: log what the response looks like
			preview := string(respBody)
			if len(preview) > 500 {
				preview = preview[:500] + "..."
			}
			log.Printf("[CHAT] %s no tool calls detected in iteration %d, response preview: %s", provider.Name(), iteration, preview)
			break // No more tool calls, exit loop
		}

		log.Printf("[CHAT] %s tool calls detected: %d", provider.Name(), len(toolCalls))

		// Check if tool mode is pass-through (forward tool_calls to client)
		if client.ToolMode == "pass-through" {
			log.Printf("[CHAT] %s passing tool_calls to client (ToolMode=pass-through)", provider.Name())
			// Build response with tool_calls for the client to handle
			toolCallsResp := make([]map[string]interface{}, len(toolCalls))
			for i, tc := range toolCalls {
				toolCallsResp[i] = map[string]interface{}{
					"id":   tc.ID,
					"type": "function",
					"function": map[string]interface{}{
						"name":      tc.Name,
						"arguments": tc.Arguments,
					},
				}
			}
			response := OpenAIChatResponse{
				ID:      "chatcmpl-" + randomID(12),
				Object:  "chat.completion",
				Created: time.Now().Unix(),
				Model:   req.Model,
				Choices: []map[string]interface{}{
					{
						"index": 0,
						"message": map[string]interface{}{
							"role":       "assistant",
							"content":    nil,
							"tool_calls": toolCallsResp,
						},
						"finish_reason": "tool_calls",
					},
				},
				Usage: map[string]interface{}{
					"prompt_tokens":     0,
					"completion_tokens": 0,
					"total_tokens":      0,
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(response)
			return
		}

		// Execute each tool and add results to messages
		for _, tc := range toolCalls {
			log.Printf("[CHAT] Executing tool: %s with args: %s", tc.Name, tc.Arguments)
			toolNames = append(toolNames, tc.Name)

			// Add assistant's tool call message first
			chatReq.Messages = append(chatReq.Messages, providers.ChatMessage{
				Role:      "assistant",
				ToolCalls: []providers.ToolCall{tc},
			})

			var args map[string]interface{}
			if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil {
				log.Printf("[CHAT] Failed to parse tool args: %v", err)
				args = map[string]interface{}{"raw": tc.Arguments}
			}

			result, err := h.toolService.Execute(tc.Name, args)
			if err != nil {
				log.Printf("[CHAT] Tool execution error: %v", err)
				result = `{"error": "tool execution failed"}`
			}

			// Add tool result as a message
			chatReq.Messages = append(chatReq.Messages, providers.ChatMessage{
				Role:       "tool",
				ToolCallID: tc.ID,
				Content:    result,
			})
		}

		// Remove tools from request for final response (let model provide answer)
		chatReq.Tools = nil

		// Re-query with tool results
		start = time.Now()
		respBody, statusCode, err = provider.ChatCompletion(chatReq)
		latencyMs = int(time.Since(start).Milliseconds())

		if err != nil {
			log.Printf("[CHAT] %s tool loop request error: %v", provider.Name(), err)
			writeOpenAIError(w, http.StatusBadGateway, "Upstream request failed: "+err.Error(), "api_error")
			return
		}

		if statusCode >= 400 {
			errMsg := extractErrorMessage(respBody)
			log.Printf("[CHAT] %s tool loop error: %s", provider.Name(), errMsg)
			writeOpenAIError(w, mapUpstreamStatusToHTTP(statusCode), errMsg, "api_error")
			return
		}

		log.Printf("[CHAT] %s tool loop response status: %d, latency: %dms", provider.Name(), statusCode, latencyMs)
	}

	responseText, inputTokens, outputTokens, _ := provider.ParseResponse(respBody)
	h.geminiService.LogRequest(client.ID, chatReq.Model, statusCode, inputTokens, outputTokens, latencyMs, "", requestBody, chatReq.Stream, len(toolNames) > 0, strings.Join(toolNames, ","))
	RecordRequest(client.ID, chatReq.Model, fmt.Sprintf("%d", statusCode), inputTokens, outputTokens, latencyMs)

	if h.statsService != nil {
		h.statsService.DecrementRequestsInProgress()
	}

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

	// Update client models if not already cached
	if client.BackendModels == "" {
		h.updateClientModels(client, provider)
	}
}

// handleNonStreamingRequestWithFallback handles requests with automatic fallback to backup models
func (h *OpenAIHandler) handleNonStreamingRequestWithFallback(w http.ResponseWriter, client *models.Client, req OpenAIChatRequest, provider providers.Provider, chatReq *providers.ChatRequest, requestBody string, fallbackModels []string) {
	// Try primary model first
	err := h.tryNonStreamingRequest(w, client, req, provider, chatReq, requestBody)

	// If successful or no fallback configured, we're done
	if err == nil || len(fallbackModels) == 0 {
		return
	}

	// Try fallback models
	originalModel := chatReq.Model
	for i, fallbackModel := range fallbackModels {
		log.Printf("[CHAT] Trying fallback model %d: %s (error: %v)", i+1, fallbackModel, err)
		chatReq.Model = fallbackModel

		err = h.tryNonStreamingRequest(w, client, req, provider, chatReq, requestBody)
		if err == nil {
			log.Printf("[CHAT] Fallback model %s succeeded", fallbackModel)
			return
		}
	}

	// All models failed, return the last error
	chatReq.Model = originalModel
}

// tryNonStreamingRequest attempts a single non-streaming request
func (h *OpenAIHandler) tryNonStreamingRequest(w http.ResponseWriter, client *models.Client, req OpenAIChatRequest, provider providers.Provider, chatReq *providers.ChatRequest, requestBody string) error {
	start := time.Now()

	respBody, statusCode, err := provider.ChatCompletion(chatReq)
	latencyMs := int(time.Since(start).Milliseconds())

	if err != nil {
		log.Printf("[CHAT] %s request error: %v", provider.Name(), err)
		writeOpenAIError(w, http.StatusBadGateway, "Upstream request failed: "+err.Error(), "api_error")
		if h.statsService != nil {
			h.statsService.DecrementRequestsInProgress()
		}
		return err
	}

	log.Printf("[CHAT] %s response status: %d, latency: %dms", provider.Name(), statusCode, latencyMs)

	if statusCode >= 400 {
		errMsg := extractErrorMessage(respBody)
		log.Printf("[CHAT] %s error: %s", provider.Name(), errMsg)
		httpStatus := mapUpstreamStatusToHTTP(statusCode)
		writeOpenAIError(w, httpStatus, errMsg, "api_error")
		if h.statsService != nil {
			h.statsService.DecrementRequestsInProgress()
		}
		return fmt.Errorf("status %d: %s", statusCode, errMsg)
	}

	return nil
}

// handleStreamingRequest sends a streaming request through the provider,
// reads SSE chunks, and translates them to OpenAI-format SSE in real time.
func (h *OpenAIHandler) handleStreamingRequest(w http.ResponseWriter, r *http.Request, client *models.Client, req OpenAIChatRequest, provider providers.Provider, chatReq *providers.ChatRequest, requestBody string) {
	start := time.Now()
	var toolNames []string

	log.Printf("[CHAT] %s calling ChatCompletionStream with model: %s, ToolMode: %q", provider.Name(), chatReq.Model, client.ToolMode)

	resp, err := provider.ChatCompletionStream(chatReq)
	if err != nil {
		log.Printf("[CHAT] %s stream error: %v", provider.Name(), err)
		writeOpenAIError(w, http.StatusBadGateway, "Upstream request failed: "+err.Error(), "api_error")
		if h.statsService != nil {
			h.statsService.DecrementRequestsInProgress()
		}
		return
	}
	defer resp.Body.Close()

	log.Printf("[CHAT] %s stream response status: %d", provider.Name(), resp.StatusCode)

	// If provider returned an error status, read the body and return error
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		latencyMs := int(time.Since(start).Milliseconds())
		log.Printf("[CHAT] %s stream error status: %d, latency: %dms", provider.Name(), resp.StatusCode, latencyMs)

		errMsg := extractErrorMessage(body)
		log.Printf("[CHAT] %s error: %s", provider.Name(), errMsg)
		httpStatus := mapUpstreamStatusToHTTP(resp.StatusCode)
		writeOpenAIError(w, httpStatus, errMsg, "api_error")
		if h.statsService != nil {
			h.statsService.DecrementRequestsInProgress()
		}
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
		if h.statsService != nil {
			h.statsService.DecrementRequestsInProgress()
		}
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
	var totalText strings.Builder

	maxToolIterations := 5
toolLoop:
	for iteration := 0; iteration < maxToolIterations; iteration++ {
		var accumulatedText strings.Builder
		var toolCallID, toolCallName, toolCallArgs string
		var hasToolCall bool
		var inToolCall bool

		for scanner.Scan() {
			line := scanner.Text()

			if !strings.HasPrefix(line, prefix) {
				continue
			}

			jsonData := strings.TrimPrefix(line, prefix)
			if jsonData == "" || jsonData == "[DONE]" {
				continue
			}

			// Parse the chunk
			var chunk map[string]interface{}
			if err := json.Unmarshal([]byte(jsonData), &chunk); err != nil {
				continue
			}

			// Get finish_reason
			var finishReason string
			if choices, ok := chunk["choices"].([]interface{}); ok && len(choices) > 0 {
				if choice, ok := choices[0].(map[string]interface{}); ok {
					finishReason, _ = choice["finish_reason"].(string)
				}
			}

			// Check for tool call in delta
			if choices, ok := chunk["choices"].([]interface{}); ok && len(choices) > 0 {
				if choice, ok := choices[0].(map[string]interface{}); ok {
					if delta, ok := choice["delta"].(map[string]interface{}); ok {
						// Check for tool_calls in delta
						if toolCalls, ok := delta["tool_calls"].([]interface{}); ok && len(toolCalls) > 0 {
							if tc, ok := toolCalls[0].(map[string]interface{}); ok {
								hasToolCall = true
								inToolCall = true

								// Extract ID (only in first chunk)
								if id, ok := tc["id"].(string); ok && id != "" {
									toolCallID = id
								}

								// Extract function name (may be partial)
								if fn, ok := tc["function"].(map[string]interface{}); ok {
									if name, ok := fn["name"].(string); ok && name != "" {
										toolCallName = name
									}
									if args, ok := fn["arguments"].(string); ok && args != "" {
										toolCallArgs += args
									}
								}
							}
						}
					}
				}
			}

			// Log finish reason for debugging
			if finishReason != "" && finishReason != "null" && finishReason != "stop" {
				log.Printf("[CHAT] %s chunk finish_reason: %s, toolCallName: %s, toolCallArgs: %s", provider.Name(), finishReason, toolCallName, toolCallArgs)
			}

			// If finish_reason is tool_calls, execute the tool
			if finishReason == "tool_calls" && hasToolCall {
				log.Printf("[CHAT] %s streaming tool call detected: %s with args: %s", provider.Name(), toolCallName, toolCallArgs)
				toolNames = append(toolNames, toolCallName)
				break
			}

			// Regular text content
			text, it, ot := provider.ParseStreamChunk([]byte(jsonData))
			if it > 0 {
				inputTokens = it
			}
			if ot > 0 {
				outputTokens = ot
			}

			if text != "" && !inToolCall {
				chunkCount++
				accumulatedText.WriteString(text)
				totalText.WriteString(text)
				sendSSEChunk(w, flusher, responseID, req.Model, created, map[string]interface{}{"content": text}, nil)
			}
		}

		// If no tool call detected in this iteration, we're done
		if !hasToolCall {
			break toolLoop
		}

		// Check if tool mode is pass-through (forward tool_calls to client)
		if client.ToolMode == "pass-through" {
			log.Printf("[CHAT] %s tool call detected, passing through to client (ToolMode=pass-through)", provider.Name())
			// Send tool_calls in a chunk and end stream - client will handle execution
			toolCallsChunk := map[string]interface{}{
				"tool_calls": []map[string]interface{}{
					{
						"id":    toolCallID,
						"index": 0,
						"type":  "function",
						"function": map[string]interface{}{
							"name":      toolCallName,
							"arguments": toolCallArgs,
						},
					},
				},
			}
			sendSSEChunk(w, flusher, responseID, req.Model, created, toolCallsChunk, nil)
			// End the stream with finish_reason: tool_calls
			finishChunk := map[string]interface{}{
				"choices": []map[string]interface{}{
					{
						"index":         0,
						"delta":         map[string]interface{}{},
						"finish_reason": "tool_calls",
					},
				},
			}
			sendSSEChunk(w, flusher, responseID, req.Model, created, finishChunk, nil)
			fmt.Fprintf(w, "data: [DONE]\n\n")
			flusher.Flush()
			return
		}

		// Execute the tool (gateway mode)
		log.Printf("[CHAT] %s executing tool: %s with args: %s", provider.Name(), toolCallName, toolCallArgs)

		// Add assistant's tool call message first
		chatReq.Messages = append(chatReq.Messages, providers.ChatMessage{
			Role: "assistant",
			ToolCalls: []providers.ToolCall{{
				ID:        toolCallID,
				Name:      toolCallName,
				Arguments: toolCallArgs,
			}},
		})

		var args map[string]interface{}
		if err := json.Unmarshal([]byte(toolCallArgs), &args); err != nil {
			log.Printf("[CHAT] Failed to parse tool args: %v", err)
			args = map[string]interface{}{"raw": toolCallArgs}
		}

		result, err := h.toolService.Execute(toolCallName, args)
		if err != nil {
			log.Printf("[CHAT] Tool execution error: %v", err)
			result = `{"error": "tool execution failed"}`
		}

		// Add tool result as message
		chatReq.Messages = append(chatReq.Messages, providers.ChatMessage{
			Role:       "tool",
			ToolCallID: toolCallID,
			Content:    result,
		})

		log.Printf("[CHAT] %s tool result: %s", provider.Name(), result)

		// Remove tools from request for final response
		chatReq.Tools = nil

		// Re-query with tool results - start new streaming request
		log.Printf("[CHAT] %s re-querying with tool results (iteration %d)", provider.Name(), iteration+1)

		resp.Body.Close()
		resp, err = provider.ChatCompletionStream(chatReq)
		if err != nil {
			log.Printf("[CHAT] %s tool loop stream error: %v", provider.Name(), err)
			break
		}

		scanner = bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 256*1024), 256*1024)

		// Update response ID for new stream
		responseID = "chatcmpl-" + randomID(12)
		created = time.Now().Unix()
		w.Header().Set("x-request-id", responseID)
	}

	latencyMs := int(time.Since(start).Milliseconds())

	// Estimate tokens if not provided by provider (e.g., LM Studio doesn't send usage in stream)
	if outputTokens == 0 && totalText.Len() > 0 {
		outputTokens = totalText.Len() / 4
		log.Printf("[CHAT] Estimated output tokens: %d (from %d chars)", outputTokens, totalText.Len())
	}

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
	h.geminiService.LogRequest(client.ID, chatReq.Model, resp.StatusCode, inputTokens, outputTokens, latencyMs, "", requestBody, chatReq.Stream, len(toolNames) > 0, strings.Join(toolNames, ","))
	RecordRequest(client.ID, chatReq.Model, fmt.Sprintf("%d", resp.StatusCode), inputTokens, outputTokens, latencyMs)

	if h.statsService != nil {
		h.statsService.DecrementRequestsInProgress()
	}

	// Update client models if not already cached
	if client.BackendModels == "" {
		h.updateClientModels(client, provider)
	}
}

// handleStreamingRequestWithFallback handles streaming requests with automatic fallback to backup models
func (h *OpenAIHandler) handleStreamingRequestWithFallback(w http.ResponseWriter, r *http.Request, client *models.Client, req OpenAIChatRequest, provider providers.Provider, chatReq *providers.ChatRequest, requestBody string, fallbackModels []string) {
	// Try primary model first
	err := h.tryStreamingRequest(w, r, client, req, provider, chatReq, requestBody)

	// If successful or no fallback configured, we're done
	if err == nil || len(fallbackModels) == 0 {
		return
	}

	// Check if error is retryable (before streaming started)
	if !isRetryableError(0, err.Error()) {
		return
	}

	// Try fallback models
	originalModel := chatReq.Model
	for i, fallbackModel := range fallbackModels {
		log.Printf("[CHAT] Streaming fallback: trying model %d: %s (error: %v)", i+1, fallbackModel, err)
		chatReq.Model = fallbackModel

		err = h.tryStreamingRequest(w, r, client, req, provider, chatReq, requestBody)
		if err == nil {
			log.Printf("[CHAT] Streaming fallback model %s succeeded", fallbackModel)
			return
		}

		// Check if retryable
		if !isRetryableError(0, err.Error()) {
			return
		}
	}

	// All models failed, restore original model
	chatReq.Model = originalModel
}

// tryStreamingRequest attempts a single streaming request, returns error if streaming failed to start
func (h *OpenAIHandler) tryStreamingRequest(w http.ResponseWriter, r *http.Request, client *models.Client, req OpenAIChatRequest, provider providers.Provider, chatReq *providers.ChatRequest, requestBody string) error {
	start := time.Now()
	var toolNames []string

	log.Printf("[CHAT] %s calling ChatCompletionStream with model: %s, ToolMode: %q", provider.Name(), chatReq.Model, client.ToolMode)

	resp, err := provider.ChatCompletionStream(chatReq)
	if err != nil {
		log.Printf("[CHAT] %s stream error: %v", provider.Name(), err)
		writeOpenAIError(w, http.StatusBadGateway, "Upstream request failed: "+err.Error(), "api_error")
		if h.statsService != nil {
			h.statsService.DecrementRequestsInProgress()
		}
		return err
	}
	defer resp.Body.Close()

	log.Printf("[CHAT] %s stream response status: %d", provider.Name(), resp.StatusCode)

	// If provider returned an error status, read the body and return error
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		latencyMs := int(time.Since(start).Milliseconds())
		log.Printf("[CHAT] %s stream error status: %d, latency: %dms", provider.Name(), resp.StatusCode, latencyMs)

		errMsg := extractErrorMessage(body)
		log.Printf("[CHAT] %s error: %s", provider.Name(), errMsg)
		httpStatus := mapUpstreamStatusToHTTP(resp.StatusCode)
		writeOpenAIError(w, httpStatus, errMsg, "api_error")
		if h.statsService != nil {
			h.statsService.DecrementRequestsInProgress()
		}
		return fmt.Errorf("status %d: %s", resp.StatusCode, errMsg)
	}

	// Set up SSE headers for the client - at this point we can't fallback anymore
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
		if h.statsService != nil {
			h.statsService.DecrementRequestsInProgress()
		}
		return nil
	}

	// Send the initial role chunk
	sendSSEChunk(w, flusher, responseID, req.Model, created, map[string]interface{}{"role": "assistant", "content": ""}, nil)

	// Read upstream SSE stream and forward chunks
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 256*1024), 256*1024)

	prefix := provider.StreamDataPrefix()
	var inputTokens, outputTokens int
	chunkCount := 0

	var totalText, accumulatedText strings.Builder
	toolCallID := ""
	var toolCallName, toolCallArgs string
	inToolCall := false
	hasToolCall := false

toolLoop:
	for iteration := 0; iteration < 10; iteration++ {
		for scanner.Scan() {
			line := scanner.Text()

			// Skip empty lines
			if strings.TrimSpace(line) == "" {
				continue
			}

			// Remove data: prefix if present
			if strings.HasPrefix(line, prefix) {
				line = strings.TrimPrefix(line, prefix)
				line = strings.TrimSpace(line)
			} else {
				continue
			}

			// Skip [DONE] marker
			if line == "[DONE]" {
				continue
			}

			// Parse the JSON data
			var data map[string]interface{}
			if err := json.Unmarshal([]byte(line), &data); err != nil {
				log.Printf("[CHAT] Failed to parse stream chunk: %v", err)
				continue
			}

			// Extract finish_reason
			finishReason := ""
			if choices, ok := data["choices"].([]interface{}); ok && len(choices) > 0 {
				if choice, ok := choices[0].(map[string]interface{}); ok {
					if fr, ok := choice["finish_reason"].(string); ok {
						finishReason = fr
					}
					// Handle delta with content
					if delta, ok := choice["delta"].(map[string]interface{}); ok {
						// Check for content
						if content, ok := delta["content"].(string); ok && content != "" {
							chunkCount++
							accumulatedText.WriteString(content)
							totalText.WriteString(content)
							sendSSEChunk(w, flusher, responseID, req.Model, created, map[string]interface{}{"content": content}, nil)
						}

						// Check for tool_calls
						if tc, ok := delta["tool_calls"].([]interface{}); ok && len(tc) > 0 {
							hasToolCall = true
							inToolCall = true

							for _, tcItem := range tc {
								if tcMap, ok := tcItem.(map[string]interface{}); ok {
									// Extract tool call ID
									if id, ok := tcMap["id"].(string); ok && id != "" {
										toolCallID = id
									}

									// Extract function name (may be partial)
									if fn, ok := tcMap["function"].(map[string]interface{}); ok {
										if name, ok := fn["name"].(string); ok && name != "" {
											toolCallName = name
										}
										if args, ok := fn["arguments"].(string); ok && args != "" {
											toolCallArgs += args
										}
									}
								}
							}
						}
					}
				}
			}

			// Log finish reason for debugging
			if finishReason != "" && finishReason != "null" && finishReason != "stop" {
				log.Printf("[CHAT] %s chunk finish_reason: %s, toolCallName: %s, toolCallArgs: %s", provider.Name(), finishReason, toolCallName, toolCallArgs)
			}

			// If finish_reason is tool_calls, execute the tool
			if finishReason == "tool_calls" && hasToolCall {
				log.Printf("[CHAT] %s streaming tool call detected: %s with args: %s", provider.Name(), toolCallName, toolCallArgs)
				toolNames = append(toolNames, toolCallName)
				break
			}

			// Regular text content
			text, it, ot := provider.ParseStreamChunk([]byte(line))
			if it > 0 {
				inputTokens = it
			}
			if ot > 0 {
				outputTokens = ot
			}

			if text != "" && !inToolCall {
				chunkCount++
				accumulatedText.WriteString(text)
				totalText.WriteString(text)
				sendSSEChunk(w, flusher, responseID, req.Model, created, map[string]interface{}{"content": text}, nil)
			}
		}

		// If no tool call detected in this iteration, we're done
		if !hasToolCall {
			break toolLoop
		}

		// Check if tool mode is pass-through (forward tool_calls to client)
		if client.ToolMode == "pass-through" {
			log.Printf("[CHAT] %s tool call detected, passing through to client (ToolMode=pass-through)", provider.Name())
			// Send tool_calls in a chunk and end stream - client will handle execution
			toolCallsChunk := map[string]interface{}{
				"tool_calls": []map[string]interface{}{
					{
						"id":    toolCallID,
						"index": 0,
						"type":  "function",
						"function": map[string]interface{}{
							"name":      toolCallName,
							"arguments": toolCallArgs,
						},
					},
				},
			}
			sendSSEChunk(w, flusher, responseID, req.Model, created, toolCallsChunk, nil)
			// End the stream with finish_reason: tool_calls
			finishChunk := map[string]interface{}{
				"choices": []map[string]interface{}{
					{
						"index":         0,
						"delta":         map[string]interface{}{},
						"finish_reason": "tool_calls",
					},
				},
			}
			sendSSEChunk(w, flusher, responseID, req.Model, created, finishChunk, nil)
			fmt.Fprintf(w, "data: [DONE]\n\n")
			flusher.Flush()
			return nil
		}

		// Execute the tool (gateway mode)
		log.Printf("[CHAT] %s executing tool: %s with args: %s", provider.Name(), toolCallName, toolCallArgs)

		// Add assistant's tool call message first
		chatReq.Messages = append(chatReq.Messages, providers.ChatMessage{
			Role: "assistant",
			ToolCalls: []providers.ToolCall{{
				ID:        toolCallID,
				Name:      toolCallName,
				Arguments: toolCallArgs,
			}},
		})

		var args map[string]interface{}
		if err := json.Unmarshal([]byte(toolCallArgs), &args); err != nil {
			log.Printf("[CHAT] Failed to parse tool args: %v", err)
			args = map[string]interface{}{"raw": toolCallArgs}
		}

		result, err := h.toolService.Execute(toolCallName, args)
		if err != nil {
			log.Printf("[CHAT] Tool execution error: %v", err)
			result = `{"error": "tool execution failed"}`
		}

		// Add tool result as message
		chatReq.Messages = append(chatReq.Messages, providers.ChatMessage{
			Role:       "tool",
			ToolCallID: toolCallID,
			Content:    result,
		})

		log.Printf("[CHAT] %s tool result: %s", provider.Name(), result)

		// Remove tools from request for final response
		chatReq.Tools = nil

		// Re-query with tool results - start new streaming request
		log.Printf("[CHAT] %s re-querying with tool results (iteration %d)", provider.Name(), iteration+1)

		resp.Body.Close()
		resp, err = provider.ChatCompletionStream(chatReq)
		if err != nil {
			log.Printf("[CHAT] %s tool loop stream error: %v", provider.Name(), err)
			break
		}

		scanner = bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 256*1024), 256*1024)

		// Update response ID for new stream
		responseID = "chatcmpl-" + randomID(12)
		created = time.Now().Unix()
		w.Header().Set("x-request-id", responseID)
	}

	latencyMs := int(time.Since(start).Milliseconds())

	// Estimate tokens if not provided by provider (e.g., LM Studio doesn't send usage in stream)
	if outputTokens == 0 && totalText.Len() > 0 {
		outputTokens = totalText.Len() / 4
		log.Printf("[CHAT] Estimated output tokens: %d (from %d chars)", outputTokens, totalText.Len())
	}

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
	h.geminiService.LogRequest(client.ID, chatReq.Model, resp.StatusCode, inputTokens, outputTokens, latencyMs, "", requestBody, chatReq.Stream, len(toolNames) > 0, strings.Join(toolNames, ","))
	RecordRequest(client.ID, chatReq.Model, fmt.Sprintf("%d", resp.StatusCode), inputTokens, outputTokens, latencyMs)

	if h.statsService != nil {
		h.statsService.DecrementRequestsInProgress()
	}

	// Update client models if not already cached
	if client.BackendModels == "" {
		h.updateClientModels(client, provider)
	}

	return nil
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
	client := middleware.GetClientFromContext(r.Context())

	log.Printf("[ListModels] Request from client: %s, path: %s", r.RemoteAddr, r.URL.Path)
	log.Printf("[ListModels] Authenticated client: %v", client != nil)
	if client != nil {
		log.Printf("[ListModels] Client ID: %s, Backend: %s, BackendModels: %q", client.ID, client.Backend, client.BackendModels)
	}

	// If authenticated client, return their models
	if client != nil && client.BackendModels != "" && client.BackendModels != "[]" {
		var models []string
		if err := json.Unmarshal([]byte(client.BackendModels), &models); err == nil && len(models) > 0 {
			log.Printf("[ListModels] Returning %d models from client BackendModels", len(models))
			var allModels []OpenAIModel
			for _, m := range models {
				allModels = append(allModels, OpenAIModel{
					ID:      m,
					Object:  "model",
					Created: time.Now().Unix(),
					OwnedBy: client.Backend,
				})
			}
			result := OpenAIModelsResponse{
				Object: "list",
				Data:   allModels,
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(result)
			return
		} else {
			log.Printf("[ListModels] Failed to parse BackendModels: %v", err)
		}
	}

	// Try to fetch models from client's provider if not available
	if client != nil && (client.BackendModels == "" || client.BackendModels == "[]") {
		log.Printf("[ListModels] No BackendModels (%q), attempting to fetch from client provider: %s", client.BackendModels, client.Backend)

		pcfg := config.ProviderConfig{
			Type:           client.Backend,
			APIKey:         client.BackendAPIKey,
			BaseURL:        client.BackendBaseURL,
			DefaultModel:   client.BackendDefaultModel,
			TimeoutSeconds: 30,
		}

		log.Printf("[ListModels] Building provider: backend=%s, baseURL=%s, apiKey=%s", client.Backend, pcfg.BaseURL, pcfg.APIKey)

		provider, err := providers.BuildSingleProvider(client.Backend, pcfg)
		if err != nil {
			log.Printf("[ListModels] Failed to build provider: %v", err)
		} else {
			log.Printf("[ListModels] Provider built successfully, fetching models...")
			models, fetchErr := provider.FetchModels()
			if fetchErr != nil {
				log.Printf("[ListModels] Failed to fetch models from provider: %v", fetchErr)
			} else if len(models) == 0 {
				log.Printf("[ListModels] Fetched 0 models from provider")
			} else {
				log.Printf("[ListModels] Fetched %d models from provider: %v", len(models), models)
				// Save to client
				modelsJSON, _ := json.Marshal(models)
				client.BackendModels = string(modelsJSON)
				h.clientService.UpdateClient(client)

				var allModels []OpenAIModel
				for _, m := range models {
					allModels = append(allModels, OpenAIModel{
						ID:      m,
						Object:  "model",
						Created: time.Now().Unix(),
						OwnedBy: client.Backend,
					})
				}
				result := OpenAIModelsResponse{
					Object: "list",
					Data:   allModels,
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(result)
				return
			}
		}
	}

	// Otherwise return models from global registry
	log.Printf("[ListModels] No client or no BackendModels, falling back to global registry")
	var allModels []OpenAIModel

	// Get models from global registry if configured
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

	log.Printf("[ListModels] Returning %d models from global registry", len(allModels))

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
