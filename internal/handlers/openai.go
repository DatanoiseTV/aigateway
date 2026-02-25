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

	"gemini-proxy/internal/middleware"
	"gemini-proxy/internal/models"
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

// mapOpenAIRoleToGemini converts OpenAI message roles to Gemini API roles.
// Gemini only supports "user" and "model" roles; system instructions are handled separately.
func mapOpenAIRoleToGemini(role string) string {
	switch role {
	case "assistant":
		return "model"
	case "user":
		return "user"
	default:
		return "user"
	}
}

// buildGeminiContents converts OpenAI-style messages into Gemini API request format,
// extracting the system instruction and building a proper multi-turn conversation.
func buildGeminiContents(messages []map[string]interface{}) (contents []map[string]interface{}, systemInstruction *map[string]interface{}) {
	for _, msg := range messages {
		role, _ := msg["role"].(string)
		content, _ := msg["content"].(string)
		if content == "" {
			continue
		}

		if role == "system" {
			si := map[string]interface{}{
				"parts": []map[string]interface{}{
					{"text": content},
				},
			}
			systemInstruction = &si
			continue
		}

		geminiRole := mapOpenAIRoleToGemini(role)
		contents = append(contents, map[string]interface{}{
			"role": geminiRole,
			"parts": []map[string]interface{}{
				{"text": content},
			},
		})
	}
	return
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

// mapGeminiStatusToHTTP converts Gemini API status codes to appropriate HTTP status codes
// for the OpenAI-compatible response.
func mapGeminiStatusToHTTP(geminiStatus int) int {
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

	log.Printf("[CHAT] Model: %s, Messages: %d, Stream: %v", req.Model, len(req.Messages), req.Stream)

	model := h.mapModel(req.Model)
	if model == "" {
		model = h.geminiService.GetDefaultModel()
	}
	log.Printf("[CHAT] Mapped to Gemini model: %s", model)

	// Log messages for debugging
	for i, msg := range req.Messages {
		role, _ := msg["role"].(string)
		content, _ := msg["content"].(string)
		if content != "" {
			log.Printf("[CHAT] Message %d: role=%s, content=%s", i, role, content[:min(50, len(content))])
		}
	}

	// Build proper Gemini multi-turn conversation format
	contents, systemInstruction := buildGeminiContents(req.Messages)

	if len(contents) == 0 {
		writeOpenAIError(w, http.StatusBadRequest, "No content in messages", "invalid_request_error")
		return
	}

	log.Printf("[CHAT] Gemini contents: %d turns, has system instruction: %v", len(contents), systemInstruction != nil)

	geminiReq := map[string]interface{}{
		"contents": contents,
	}

	if systemInstruction != nil {
		geminiReq["systemInstruction"] = *systemInstruction
	}

	genConfig := map[string]interface{}{}
	if req.MaxTokens > 0 {
		genConfig["maxOutputTokens"] = req.MaxTokens
	}
	if req.Temperature > 0 {
		genConfig["temperature"] = req.Temperature
	}
	if len(genConfig) > 0 {
		geminiReq["generationConfig"] = genConfig
	}

	geminiBody, _ := json.Marshal(geminiReq)

	if req.Stream {
		h.handleStreamingRequest(w, r, client, req, model, geminiBody)
		return
	}

	h.handleNonStreamingRequest(w, client, req, model, geminiBody)
}

// handleNonStreamingRequest calls Gemini's generateContent endpoint and returns
// the full response as an OpenAI JSON response.
func (h *OpenAIHandler) handleNonStreamingRequest(w http.ResponseWriter, client *models.Client, req OpenAIChatRequest, model string, geminiBody []byte) {
	start := time.Now()
	respBody, statusCode, err := h.geminiService.ForwardRequest(model, geminiBody)
	latencyMs := int(time.Since(start).Milliseconds())

	if err != nil {
		log.Printf("[CHAT] ForwardRequest error: %v", err)
		writeOpenAIError(w, http.StatusBadGateway, "Upstream request failed: "+err.Error(), "api_error")
		return
	}

	log.Printf("[CHAT] Gemini response status: %d, latency: %dms, body: %s", statusCode, latencyMs, string(respBody)[:min(200, len(string(respBody)))])

	if statusCode >= 400 {
		errMsg := extractGeminiErrorMessage(respBody)
		log.Printf("[CHAT] Gemini error: %s", errMsg)
		httpStatus := mapGeminiStatusToHTTP(statusCode)
		writeOpenAIError(w, httpStatus, errMsg, "api_error")
		return
	}

	inputTokens, outputTokens, _ := services.ParseGeminiResponse(respBody)
	h.geminiService.LogRequest(client.ID, model, statusCode, inputTokens, outputTokens, latencyMs, "")

	responseText := extractGeminiText(respBody)
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

// handleStreamingRequest calls Gemini's streamGenerateContent endpoint with alt=sse
// and translates each Gemini SSE chunk into OpenAI-format SSE chunks in real time.
func (h *OpenAIHandler) handleStreamingRequest(w http.ResponseWriter, r *http.Request, client *models.Client, req OpenAIChatRequest, model string, geminiBody []byte) {
	start := time.Now()

	resp, resolvedModel, err := h.geminiService.ForwardStreamRequest(model, geminiBody)
	if err != nil {
		log.Printf("[CHAT] ForwardStreamRequest error: %v", err)
		writeOpenAIError(w, http.StatusBadGateway, "Upstream request failed: "+err.Error(), "api_error")
		return
	}
	defer resp.Body.Close()

	// If Gemini returned an error status, read the body and return error
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		latencyMs := int(time.Since(start).Milliseconds())
		log.Printf("[CHAT] Gemini stream error status: %d, latency: %dms, body: %s", resp.StatusCode, latencyMs, string(body)[:min(200, len(string(body)))])

		errMsg := extractGeminiErrorMessage(body)
		log.Printf("[CHAT] Gemini error: %s", errMsg)
		httpStatus := mapGeminiStatusToHTTP(resp.StatusCode)
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

	// Read Gemini SSE stream and forward chunks
	// With alt=sse, Gemini sends lines like: "data: {json}\n\n"
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 256*1024), 256*1024)

	var inputTokens, outputTokens int
	chunkCount := 0

	for scanner.Scan() {
		line := scanner.Text()

		// SSE format: lines starting with "data: " contain the JSON payload
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		jsonData := strings.TrimPrefix(line, "data: ")
		if jsonData == "" {
			continue
		}

		// Parse the Gemini chunk
		var geminiChunk map[string]interface{}
		if err := json.Unmarshal([]byte(jsonData), &geminiChunk); err != nil {
			log.Printf("[CHAT] Failed to parse Gemini stream chunk: %v", err)
			continue
		}

		// Extract text from this chunk
		text := extractGeminiTextFromChunk(geminiChunk)
		if text != "" {
			chunkCount++
			sendSSEChunk(w, flusher, responseID, req.Model, created, map[string]interface{}{"content": text}, nil)
		}

		// Extract token counts from usageMetadata (present in the last chunk)
		if usage, ok := geminiChunk["usageMetadata"].(map[string]interface{}); ok {
			if pt, ok := usage["promptTokenCount"].(float64); ok {
				inputTokens = int(pt)
			}
			if ct, ok := usage["candidatesTokenCount"].(float64); ok {
				outputTokens = int(ct)
			}
		}
	}

	latencyMs := int(time.Since(start).Milliseconds())
	log.Printf("[CHAT] Stream completed: %d chunks, %d input tokens, %d output tokens, latency: %dms", chunkCount, inputTokens, outputTokens, latencyMs)

	// Send the final stop chunk with usage info
	finishReason := "stop"
	finalChunk := map[string]interface{}{
		"id":      responseID,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   req.Model,
		"choices": []map[string]interface{}{
			{
				"index":         0,
				"delta":         map[string]interface{}{},
				"finish_reason": finishReason,
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
	h.geminiService.LogRequest(client.ID, resolvedModel, resp.StatusCode, inputTokens, outputTokens, latencyMs, "")
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

// extractGeminiTextFromChunk extracts text from a single Gemini streaming chunk.
func extractGeminiTextFromChunk(chunk map[string]interface{}) string {
	candidates, ok := chunk["candidates"].([]interface{})
	if !ok || len(candidates) == 0 {
		return ""
	}
	candidate, ok := candidates[0].(map[string]interface{})
	if !ok {
		return ""
	}
	content, ok := candidate["content"].(map[string]interface{})
	if !ok {
		return ""
	}
	parts, ok := content["parts"].([]interface{})
	if !ok || len(parts) == 0 {
		return ""
	}
	part, ok := parts[0].(map[string]interface{})
	if !ok {
		return ""
	}
	text, _ := part["text"].(string)
	return text
}

// extractGeminiErrorMessage extracts the error message from a Gemini error response body.
func extractGeminiErrorMessage(body []byte) string {
	var geminiErr map[string]interface{}
	if err := json.Unmarshal(body, &geminiErr); err != nil {
		return "Gemini API error"
	}
	if errObj, ok := geminiErr["error"].(map[string]interface{}); ok {
		if msg, ok := errObj["message"].(string); ok {
			return msg
		}
	}
	return "Gemini API error"
}

// extractGeminiText pulls the generated text from a Gemini API response body.
func extractGeminiText(respBody []byte) string {
	var geminiResp map[string]interface{}
	if err := json.Unmarshal(respBody, &geminiResp); err != nil {
		return ""
	}

	candidates, ok := geminiResp["candidates"].([]interface{})
	if !ok || len(candidates) == 0 {
		return ""
	}
	candidate, ok := candidates[0].(map[string]interface{})
	if !ok {
		return ""
	}
	content, ok := candidate["content"].(map[string]interface{})
	if !ok {
		return ""
	}
	parts, ok := content["parts"].([]interface{})
	if !ok || len(parts) == 0 {
		return ""
	}
	part, ok := parts[0].(map[string]interface{})
	if !ok {
		return ""
	}
	text, _ := part["text"].(string)
	return text
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

// mapModel resolves the requested model name to an allowed Gemini model.
// Priority: GPT alias mapping > exact match in allowed list > the model is directly
// a Gemini model name that we should pass through.
func (h *OpenAIHandler) mapModel(model string) string {
	// GPT-to-Gemini aliases for OpenAI client compatibility
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

	allowed := h.geminiService.GetAllowedModels()

	// Exact match against allowed models
	for _, a := range allowed {
		if model == a {
			return model
		}
	}

	// If the requested model is a Gemini model name (starts with "gemini-"), pass it
	// through even if not in the allowed list -- ForwardRequest will fall back to
	// the default model if it is not allowed.
	if strings.HasPrefix(model, "gemini-") || strings.HasPrefix(model, "nano-") {
		return model
	}

	return ""
}

func randomID(length int) string {
	b := make([]byte, (length+1)/2)
	rand.Read(b)
	return hex.EncodeToString(b)[:length]
}
