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

func (h *OpenAIHandler) resolveProvider(client *models.Client) (providers.Provider, error) {
	backend := client.Backend
	if backend == "" {
		backend = "gemini"
	}

	if client.BackendAPIKey != "" || client.BackendBaseURL != "" {
		cfg := config.ProviderConfig{
			Type:           backend,
			APIKey:         client.BackendAPIKey,
			BaseURL:        client.BackendBaseURL,
			DefaultModel:   client.BackendDefaultModel,
			TimeoutSeconds: 120,
		}
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

	return h.registry.Get(backend)
}

func (h *OpenAIHandler) updateClientModels(client *models.Client, provider providers.Provider) {
	models, err := provider.FetchModels()
	if err != nil {
		log.Printf("[%s] Failed to fetch models for client %s: %v", provider.Name(), client.Name, err)
		return
	}

	if len(models) == 0 {
		return
	}

	modelsJSON, err := json.Marshal(models)
	if err != nil {
		return
	}

	client.BackendModels = string(modelsJSON)
	h.clientService.UpdateClient(client)
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
		writeOpenAIError(w, http.StatusBadRequest, "Backend not configured: "+err.Error(), "invalid_request_error")
		if h.statsService != nil {
			h.statsService.DecrementRequestsInProgress()
		}
		return
	}

	chatReq := h.buildChatRequest(req, provider, client)
	if len(chatReq.Messages) == 0 {
		writeOpenAIError(w, http.StatusBadRequest, "No content in messages", "invalid_request_error")
		if h.statsService != nil {
			h.statsService.DecrementRequestsInProgress()
		}
		return
	}

	fallbackModels := parseFallbackModels(client.FallbackModels)

	if req.Stream {
		h.handleStreamingRequestWithFallback(w, r, client, req, provider, chatReq, string(body), fallbackModels)
		return
	}

	h.handleNonStreamingRequestWithFallback(w, client, req, provider, chatReq, string(body), fallbackModels)
}

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

func isRetryableError(statusCode int, errMsg string) bool {
	if statusCode == 429 || (statusCode >= 500 && statusCode <= 599) {
		return true
	}
	lowerErr := strings.ToLower(errMsg)
	return strings.Contains(lowerErr, "rate limit") || strings.Contains(lowerErr, "quota") || strings.Contains(lowerErr, "too many requests")
}

func (h *OpenAIHandler) buildChatRequest(req OpenAIChatRequest, provider providers.Provider, client *models.Client) *providers.ChatRequest {
	model := req.Model
	if model == "" {
		if client.BackendDefaultModel != "" {
			model = client.BackendDefaultModel
		} else {
			model = provider.DefaultModel()
		}
	}

	messages := make([]providers.ChatMessage, 0, len(req.Messages)+1)
	if client.SystemPrompt != "" {
		messages = append(messages, providers.ChatMessage{Role: "system", Content: client.SystemPrompt})
	}

	for _, msg := range req.Messages {
		role, _ := msg["role"].(string)
		content, _ := msg["content"].(string)
		toolCallID, _ := msg["tool_call_id"].(string)

		if role == "tool" {
			messages = append(messages, providers.ChatMessage{Role: role, Content: content, ToolCallID: toolCallID})
			continue
		}

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
			messages = append(messages, providers.ChatMessage{Role: role, Content: content, ToolCalls: toolCalls})
			continue
		}

		if content != "" || role == "assistant" {
			messages = append(messages, providers.ChatMessage{Role: role, Content: content})
		}
	}

	return &providers.ChatRequest{
		Model:          model,
		Messages:       messages,
		MaxTokens:      req.MaxTokens,
		Temperature:    req.Temperature,
		Stream:         req.Stream,
		Tools:          h.mergeTools(req.Tools, client.ServerTools),
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

func (h *OpenAIHandler) mergeTools(clientTools []map[string]interface{}, serverToolsEnabled bool) []providers.Tool {
	var tools []providers.Tool
	if len(clientTools) > 0 {
		tools = convertTools(clientTools)
	}
	if serverToolsEnabled && h.toolService != nil {
		serverToolDefs := h.toolService.GetOpenAITools()
		for _, st := range serverToolDefs {
			tools = append(tools, providers.Tool{
				Type: "function",
				Function: &providers.ToolFunction{
					Name:        getString(st["function"].(map[string]interface{}), "name"),
					Description: getString(st["function"].(map[string]interface{}), "description"),
					Parameters:  st["function"].(map[string]interface{})["parameters"],
				},
			})
		}
	}
	return tools
}

func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func (h *OpenAIHandler) handleNonStreamingRequestWithFallback(w http.ResponseWriter, client *models.Client, req OpenAIChatRequest, provider providers.Provider, chatReq *providers.ChatRequest, requestBody string, fallbackModels []string) {
	err := h.tryNonStreamingRequest(w, client, req, provider, chatReq, requestBody)
	if err == nil || len(fallbackModels) == 0 {
		return
	}

	for _, fallbackModel := range fallbackModels {
		log.Printf("[CHAT] Trying fallback: %s (error: %v)", fallbackModel, err)
		chatReq.Model = fallbackModel
		err = h.tryNonStreamingRequest(w, client, req, provider, chatReq, requestBody)
		if err == nil {
			return
		}
	}
}

func (h *OpenAIHandler) tryNonStreamingRequest(w http.ResponseWriter, client *models.Client, req OpenAIChatRequest, provider providers.Provider, chatReq *providers.ChatRequest, requestBody string) error {
	start := time.Now()
	maxToolIterations := 5
	var toolNames []string

	respBody, statusCode, err := provider.ChatCompletion(chatReq)
	latencyMs := int(time.Since(start).Milliseconds())

	if err != nil {
		if isRetryableError(502, err.Error()) {
			return err
		}
		writeOpenAIError(w, http.StatusBadGateway, "Upstream request failed: "+err.Error(), "api_error")
		if h.statsService != nil {
			h.statsService.DecrementRequestsInProgress()
		}
		return nil
	}

	if statusCode >= 400 {
		errMsg := extractErrorMessage(respBody)
		if isRetryableError(statusCode, errMsg) {
			return fmt.Errorf("status %d: %s", statusCode, errMsg)
		}
		writeOpenAIError(w, mapUpstreamStatusToHTTP(statusCode), errMsg, "api_error")
		if h.statsService != nil {
			h.statsService.DecrementRequestsInProgress()
		}
		return nil
	}

	for iteration := 0; iteration < maxToolIterations; iteration++ {
		toolCalls, err := provider.ParseToolCalls(respBody)
		if err != nil || len(toolCalls) == 0 {
			break
		}

		if client.ToolMode == "pass-through" {
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
			json.NewEncoder(w).Encode(OpenAIChatResponse{
				ID:      "chatcmpl-" + randomID(12),
				Object:  "chat.completion",
				Created: time.Now().Unix(),
				Model:   req.Model,
				Choices: []map[string]interface{}{{"index": 0, "message": map[string]interface{}{"role": "assistant", "tool_calls": toolCallsResp}, "finish_reason": "tool_calls"}},
				Usage:   map[string]interface{}{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0},
			})
			return nil
		}

		for _, tc := range toolCalls {
			toolNames = append(toolNames, tc.Name)
			chatReq.Messages = append(chatReq.Messages, providers.ChatMessage{Role: "assistant", ToolCalls: []providers.ToolCall{tc}})
			var args map[string]interface{}
			json.Unmarshal([]byte(tc.Arguments), &args)
			result, _ := h.toolService.Execute(tc.Name, args)
			chatReq.Messages = append(chatReq.Messages, providers.ChatMessage{Role: "tool", ToolCallID: tc.ID, Content: result})
		}

		chatReq.Tools = nil
		respBody, statusCode, _ = provider.ChatCompletion(chatReq)
		if statusCode >= 400 {
			break
		}
	}

	text, it, ot, _ := provider.ParseResponse(respBody)
	h.geminiService.LogRequest(client.ID, chatReq.Model, statusCode, it, ot, latencyMs, "", requestBody, false, len(toolNames) > 0, strings.Join(toolNames, ","))
	RecordRequest(client.ID, chatReq.Model, fmt.Sprintf("%d", statusCode), it, ot, latencyMs)
	if h.statsService != nil {
		h.statsService.DecrementRequestsInProgress()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(OpenAIChatResponse{
		ID:      "chatcmpl-" + randomID(12),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []map[string]interface{}{{"index": 0, "message": map[string]interface{}{"role": "assistant", "content": text}, "finish_reason": "stop"}},
		Usage:   map[string]interface{}{"prompt_tokens": it, "completion_tokens": ot, "total_tokens": it + ot},
	})

	if client.BackendModels == "" {
		h.updateClientModels(client, provider)
	}
	return nil
}

func (h *OpenAIHandler) handleStreamingRequestWithFallback(w http.ResponseWriter, r *http.Request, client *models.Client, req OpenAIChatRequest, provider providers.Provider, chatReq *providers.ChatRequest, requestBody string, fallbackModels []string) {
	err := h.tryStreamingRequest(w, r, client, req, provider, chatReq, requestBody)
	if err == nil || len(fallbackModels) == 0 {
		return
	}

	if !isRetryableError(502, err.Error()) {
		return
	}

	for _, fallbackModel := range fallbackModels {
		log.Printf("[CHAT] Streaming fallback: %s", fallbackModel)
		chatReq.Model = fallbackModel
		err = h.tryStreamingRequest(w, r, client, req, provider, chatReq, requestBody)
		if err == nil {
			return
		}
	}
}

func (h *OpenAIHandler) tryStreamingRequest(w http.ResponseWriter, r *http.Request, client *models.Client, req OpenAIChatRequest, provider providers.Provider, chatReq *providers.ChatRequest, requestBody string) error {
	start := time.Now()
	var toolNames []string

	resp, err := provider.ChatCompletionStream(chatReq)
	if err != nil {
		if isRetryableError(502, err.Error()) {
			return err
		}
		writeOpenAIError(w, http.StatusBadGateway, "Upstream request failed: "+err.Error(), "api_error")
		if h.statsService != nil {
			h.statsService.DecrementRequestsInProgress()
		}
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		errMsg := extractErrorMessage(body)
		if isRetryableError(resp.StatusCode, errMsg) {
			return fmt.Errorf("status %d: %s", resp.StatusCode, errMsg)
		}
		writeOpenAIError(w, mapUpstreamStatusToHTTP(resp.StatusCode), errMsg, "api_error")
		if h.statsService != nil {
			h.statsService.DecrementRequestsInProgress()
		}
		return nil
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	responseID := "chatcmpl-" + randomID(12)
	created := time.Now().Unix()
	w.WriteHeader(http.StatusOK)

	flusher := w.(http.Flusher)
	sendSSEChunk(w, flusher, responseID, req.Model, created, map[string]interface{}{"role": "assistant", "content": ""}, nil)

	scanner := bufio.NewScanner(resp.Body)
	prefix := provider.StreamDataPrefix()
	var it, ot int
	var totalText strings.Builder

	maxToolIterations := 5
toolLoop:
	for iteration := 0; iteration < maxToolIterations; iteration++ {
		var toolCallID, toolCallName, toolCallArgs string
		var hasToolCall bool

		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, prefix) {
				continue
			}
			jsonData := strings.TrimPrefix(line, prefix)
			if jsonData == "" || jsonData == "[DONE]" {
				continue
			}

			var chunk map[string]interface{}
			json.Unmarshal([]byte(jsonData), &chunk)

			finishReason := ""
			if choices, ok := chunk["choices"].([]interface{}); ok && len(choices) > 0 {
				if choice, ok := choices[0].(map[string]interface{}); ok {
					finishReason, _ = choice["finish_reason"].(string)
				}
			} else if doneReason, ok := chunk["done_reason"].(string); ok {
				finishReason = doneReason
			} else if done, ok := chunk["done"].(bool); ok && done {
				finishReason = "stop"
			}

			tcInterface, fr := provider.ParseStreamToolCall([]byte(jsonData))
			if fr != "" && finishReason == "" {
				finishReason = fr
			}
			if tcInterface != nil {
				hasToolCall = true
				if tc, ok := tcInterface.(*providers.StreamToolCall); ok {
					if tc.ID != "" { toolCallID = tc.ID }
					if tc.Name != "" { toolCallName = tc.Name }
					toolCallArgs += tc.Arguments
				}
			}

			if finishReason == "tool_calls" || (finishReason == "stop" && hasToolCall) {
				toolNames = append(toolNames, toolCallName)
				break
			}

			text, cit, cot := provider.ParseStreamChunk([]byte(jsonData))
			if cit > 0 { it = cit }
			if cot > 0 { ot = cot }
			if text != "" {
				totalText.WriteString(text)
				sendSSEChunk(w, flusher, responseID, req.Model, created, map[string]interface{}{"content": text}, nil)
			}
			if finishReason == "stop" || finishReason == "length" {
				break
			}
		}

		if !hasToolCall {
			break toolLoop
		}

		if client.ToolMode == "pass-through" {
			toolCallsChunk := map[string]interface{}{"tool_calls": []map[string]interface{}{{"id": toolCallID, "index": 0, "type": "function", "function": map[string]interface{}{"name": toolCallName, "arguments": toolCallArgs}}}}
			sendSSEChunk(w, flusher, responseID, req.Model, created, toolCallsChunk, "tool_calls")
			fmt.Fprintf(w, "data: [DONE]\n\n")
			flusher.Flush()
			return nil
		}

		chatReq.Messages = append(chatReq.Messages, providers.ChatMessage{Role: "assistant", ToolCalls: []providers.ToolCall{{ID: toolCallID, Name: toolCallName, Arguments: toolCallArgs}}})
		var args map[string]interface{}
		json.Unmarshal([]byte(toolCallArgs), &args)
		result, _ := h.toolService.Execute(toolCallName, args)
		chatReq.Messages = append(chatReq.Messages, providers.ChatMessage{Role: "tool", ToolCallID: toolCallID, Content: result})
		chatReq.Tools = nil
		resp.Body.Close()
		resp, _ = provider.ChatCompletionStream(chatReq)
		scanner = bufio.NewScanner(resp.Body)
	}

	if ot == 0 && totalText.Len() > 0 { ot = totalText.Len() / 4 }
	sendSSEChunk(w, flusher, responseID, req.Model, created, map[string]interface{}{}, "stop")
	// Send usage info in a separate chunk for OpenAI compatibility
	usageChunk := map[string]interface{}{
		"id":      responseID,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   req.Model,
		"choices": []interface{}{},
		"usage": map[string]interface{}{
			"prompt_tokens":     it,
			"completion_tokens": ot,
			"total_tokens":      it + ot,
		},
	}
	usageData, _ := json.Marshal(usageChunk)
	fmt.Fprintf(w, "data: %s\n\n", usageData)
	fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()

	h.geminiService.LogRequest(client.ID, chatReq.Model, resp.StatusCode, it, ot, int(time.Since(start).Milliseconds()), "", requestBody, true, len(toolNames) > 0, strings.Join(toolNames, ","))
	RecordRequest(client.ID, chatReq.Model, fmt.Sprintf("%d", resp.StatusCode), it, ot, int(time.Since(start).Milliseconds()))
	if h.statsService != nil {
		h.statsService.DecrementRequestsInProgress()
	}
	if client.BackendModels == "" {
		h.updateClientModels(client, provider)
	}
	return nil
}

func sendSSEChunk(w http.ResponseWriter, flusher http.Flusher, id, model string, created int64, delta map[string]interface{}, finishReason interface{}) {
	chunk := map[string]interface{}{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []map[string]interface{}{{"index": 0, "delta": delta, "finish_reason": finishReason}},
	}
	data, _ := json.Marshal(chunk)
	fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()
}

func extractErrorMessage(body []byte) string {
	var errObj map[string]interface{}
	if err := json.Unmarshal(body, &errObj); err != nil {
		return "Upstream API error"
	}
	if e, ok := errObj["error"].(map[string]interface{}); ok {
		if msg, ok := e["message"].(string); ok { return msg }
	}
	if msg, ok := errObj["error"].(string); ok { return msg }
	if msg, ok := errObj["message"].(string); ok { return msg }
	return "Upstream API error"
}

func (h *OpenAIHandler) ListModels(w http.ResponseWriter, r *http.Request) {
	client := middleware.GetClientFromContext(r.Context())
	var allModels []OpenAIModel

	if client != nil && client.BackendModels != "" {
		var models []string
		if err := json.Unmarshal([]byte(client.BackendModels), &models); err == nil {
			for _, m := range models {
				allModels = append(allModels, OpenAIModel{ID: m, Object: "model", Created: time.Now().Unix(), OwnedBy: client.Backend})
			}
		}
	}

	if len(allModels) == 0 {
		for _, name := range h.registry.Names() {
			provider, _ := h.registry.Get(name)
			for _, m := range provider.Models() {
				allModels = append(allModels, OpenAIModel{ID: m, Object: "model", Created: time.Now().Unix(), OwnedBy: name})
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(OpenAIModelsResponse{Object: "list", Data: allModels})
}

func (h *OpenAIHandler) CountTokens(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Prompt string `json:"prompt"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"tokens": len(req.Prompt) / 4})
}

func (h *OpenAIHandler) GetModel(w http.ResponseWriter, r *http.Request) {
	model := chi.URLParam(r, "model")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(OpenAIModel{ID: model, Object: "model", Created: time.Now().Unix(), OwnedBy: "ai-gateway"})
}

func randomID(length int) string {
	b := make([]byte, (length+1)/2)
	rand.Read(b)
	return hex.EncodeToString(b)[:length]
}
