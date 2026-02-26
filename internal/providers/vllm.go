package providers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"ai-gateway/internal/config"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"golang.org/x/net/http2"
)

var (
	vllmHTTPClient *http.Client
	vllmOnce       sync.Once
)

func getVLLMHTTPClient() *http.Client {
	vllmOnce.Do(func() {
		transport := &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 100,
			IdleConnTimeout:     90 * time.Second,
		}
		if err := http2.ConfigureTransport(transport); err != nil {
			log.Printf("[vllm] Failed to configure HTTP/2: %v", err)
		}
		vllmHTTPClient = &http.Client{
			Transport: transport,
			Timeout:   300 * time.Second,
		}
	})
	return vllmHTTPClient
}

type VLLMProvider struct {
	name string
	cfg  config.ProviderConfig
}

var vllmRequestsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "vllm_requests_total",
		Help: "Total number of vLLM requests",
	},
	[]string{"model", "status"},
)

func NewVLLMProvider(cfg config.ProviderConfig) *VLLMProvider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "http://localhost:8000/v1"
	}
	return &VLLMProvider{name: "vllm", cfg: cfg}
}

func (p *VLLMProvider) Name() string {
	return p.name
}

func (p *VLLMProvider) WithBaseURL(url string) Provider {
	newCfg := p.cfg
	newCfg.BaseURL = url
	return &VLLMProvider{name: p.name, cfg: newCfg}
}

func (p *VLLMProvider) ChatCompletion(req *ChatRequest) ([]byte, int, error) {
	url := p.cfg.BaseURL + "/chat/completions"

	reqBody := map[string]interface{}{
		"model":       req.Model,
		"messages":    p.convertMessages(req.Messages),
		"temperature": req.Temperature,
		"max_tokens":  req.MaxTokens,
		"stream":      false,
	}

	body, _ := json.Marshal(reqBody)

	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("failed to create request: %w", err)
	}

	p.setHeaders(httpReq)

	resp, err := getVLLMHTTPClient().Do(httpReq)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	return respBody, resp.StatusCode, nil
}

func (p *VLLMProvider) ChatCompletionStream(req *ChatRequest) (*http.Response, error) {
	url := p.cfg.BaseURL + "/chat/completions"

	reqBody := map[string]interface{}{
		"model":       req.Model,
		"messages":    p.convertMessages(req.Messages),
		"temperature": req.Temperature,
		"max_tokens":  req.MaxTokens,
		"stream":      true,
	}

	body, _ := json.Marshal(reqBody)

	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	p.setHeaders(httpReq)

	return getVLLMHTTPClient().Do(httpReq)
}

func (p *VLLMProvider) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if p.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	}
	req.Header.Set("User-Agent", "ai-gateway/vllm")
}

func (p *VLLMProvider) convertMessages(messages []ChatMessage) []map[string]interface{} {
	result := make([]map[string]interface{}, len(messages))
	for i, msg := range messages {
		m := map[string]interface{}{
			"role":    msg.Role,
			"content": msg.Content,
		}
		if len(msg.ToolCalls) > 0 {
			toolCalls := make([]map[string]interface{}, len(msg.ToolCalls))
			for j, tc := range msg.ToolCalls {
				toolCalls[j] = map[string]interface{}{
					"id":   tc.ID,
					"type": "function",
					"function": map[string]interface{}{
						"name":      tc.Name,
						"arguments": tc.Arguments,
					},
				}
			}
			m["tool_calls"] = toolCalls
		}
		if msg.ToolCallID != "" {
			m["tool_call_id"] = msg.ToolCallID
		}
		result[i] = m
	}
	return result
}

func (p *VLLMProvider) ParseResponse(respBody []byte) (string, int, int, error) {
	var response struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
				Role    string `json:"role"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(respBody, &response); err != nil {
		return "", 0, 0, fmt.Errorf("failed to parse response: %w", err)
	}

	content := ""
	if len(response.Choices) > 0 {
		content = response.Choices[0].Message.Content
	}

	return content, response.Usage.PromptTokens, response.Usage.CompletionTokens, nil
}

func (p *VLLMProvider) ParseStreamChunk(data []byte) (string, int, int) {
	text := ""
	inputTokens := 0
	outputTokens := 0

	line := strings.TrimSpace(string(data))
	if !strings.HasPrefix(line, "data: ") {
		return text, inputTokens, outputTokens
	}

	line = strings.TrimPrefix(line, "data: ")
	if line == "[DONE]" {
		return text, inputTokens, outputTokens
	}

	var chunk struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
				Role    string `json:"role"`
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal([]byte(line), &chunk); err != nil {
		return text, inputTokens, outputTokens
	}

	if len(chunk.Choices) > 0 {
		text = chunk.Choices[0].Delta.Content
	}

	return text, inputTokens, outputTokens
}

func (p *VLLMProvider) ListModels() ([]string, error) {
	url := p.cfg.BaseURL + "/models"
	log.Printf("[vllm ListModels] GET %s", url)

	// Create a fresh HTTP client with shorter timeout for this request
	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	httpReq, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	p.setHeaders(httpReq)

	log.Printf("[vllm ListModels] Sending request...")
	resp, err := client.Do(httpReq)
	if err != nil {
		log.Printf("[vllm ListModels] HTTP request failed: %v", err)
		return nil, err
	}
	defer resp.Body.Close()

	log.Printf("[vllm ListModels] Response status: %d", resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("[vllm ListModels] Failed to read response body: %v", err)
		return nil, err
	}
	log.Printf("[vllm ListModels] Response body: %s", string(body))

	var response struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &response); err != nil {
		log.Printf("[vllm ListModels] Failed to decode response: %v", err)
		return nil, err
	}

	models := make([]string, 0, len(response.Data))
	for _, m := range response.Data {
		models = append(models, m.ID)
	}

	log.Printf("[vllm ListModels] Found %d models: %v", len(models), models)
	return models, nil
}

func (p *VLLMProvider) DefaultModel() string {
	if p.cfg.DefaultModel != "" {
		return p.cfg.DefaultModel
	}
	return "llama-3.1-8b-instruct"
}

func (p *VLLMProvider) SupportsStreaming() bool {
	return true
}

func (p *VLLMProvider) StreamDataPrefix() string {
	return "data: "
}

func (p *VLLMProvider) ParseToolCalls(respBody []byte) ([]ToolCall, error) {
	var response struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, err
	}

	toolCalls := make([]ToolCall, 0)
	for _, choice := range response.Choices {
		for _, tc := range choice.Message.ToolCalls {
			toolCalls = append(toolCalls, ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			})
		}
	}

	return toolCalls, nil
}

func (p *VLLMProvider) ParseStreamToolCall(data []byte) (interface{}, string) {
	line := strings.TrimSpace(string(data))
	if !strings.HasPrefix(line, "data: ") {
		return nil, ""
	}

	line = strings.TrimPrefix(line, "data: ")
	if line == "[DONE]" {
		return nil, "stop"
	}

	var chunk struct {
		Choices []struct {
			Delta struct {
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
				Content string `json:"content"`
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}

	if err := json.Unmarshal([]byte(line), &chunk); err != nil {
		return nil, ""
	}

	if len(chunk.Choices) == 0 {
		return nil, ""
	}

	finishReason := chunk.Choices[0].FinishReason

	if len(chunk.Choices[0].Delta.ToolCalls) > 0 {
		tc := chunk.Choices[0].Delta.ToolCalls[0]
		return StreamToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		}, finishReason
	}

	return nil, finishReason
}

func (p *VLLMProvider) Models() []string {
	models, err := p.FetchModels()
	if err != nil {
		return []string{p.DefaultModel()}
	}
	return models
}

func (p *VLLMProvider) FetchModels() ([]string, error) {
	return p.ListModels()
}

func (p *VLLMProvider) TestConnection() (string, bool, error) {
	models, err := p.ListModels()
	if err != nil {
		return "", false, err
	}
	return fmt.Sprintf("Connected to vLLM with %d models", len(models)), true, nil
}

func (p *VLLMProvider) CancelRequest(requestID string) error {
	return nil
}

func (p *VLLMProvider) WithContext(ctx interface{}) Provider {
	return p
}

func (p *VLLMProvider) RegisterRoutes(r chi.Router) {}
