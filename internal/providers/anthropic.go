package providers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"ai-gateway/internal/config"
)

// AnthropicProvider implements the Provider interface for Anthropic's Messages API.
type AnthropicProvider struct {
	cfg config.ProviderConfig
}

func NewAnthropicProvider(cfg config.ProviderConfig) *AnthropicProvider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.anthropic.com/v1"
	}
	return &AnthropicProvider{cfg: cfg}
}

func (p *AnthropicProvider) Name() string { return "anthropic" }

func (p *AnthropicProvider) ChatCompletion(req *ChatRequest) ([]byte, int, error) {
	body := p.buildRequestBody(req, false)
	url := p.cfg.BaseURL + "/messages"

	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("failed to create request: %w", err)
	}
	p.setHeaders(httpReq)

	client := &http.Client{Timeout: time.Duration(p.cfg.TimeoutSeconds) * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("failed to read response: %w", err)
	}

	return respBody, resp.StatusCode, nil
}

func (p *AnthropicProvider) ChatCompletionStream(req *ChatRequest) (*http.Response, error) {
	body := p.buildRequestBody(req, true)
	url := p.cfg.BaseURL + "/messages"

	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	p.setHeaders(httpReq)

	client := &http.Client{Timeout: time.Duration(p.cfg.TimeoutSeconds) * time.Second}
	return client.Do(httpReq)
}

func (p *AnthropicProvider) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	if p.cfg.APIKey != "" {
		req.Header.Set("x-api-key", p.cfg.APIKey)
	}
}

func (p *AnthropicProvider) buildRequestBody(req *ChatRequest, stream bool) []byte {
	model := req.Model
	if model == "" {
		model = p.cfg.DefaultModel
	}

	// Anthropic separates system message from the messages array
	var system string
	messages := make([]map[string]interface{}, 0)
	for _, m := range req.Messages {
		if m.Role == "system" {
			system = m.Content
			continue
		}
		msg := map[string]interface{}{"role": m.Role, "content": m.Content}
		if m.Role == "tool" && m.ToolCallID != "" {
			msg["tool_use_id"] = m.ToolCallID
		}
		messages = append(messages, msg)
	}

	body := map[string]interface{}{
		"model":      model,
		"messages":   messages,
		"max_tokens": 4096,
	}

	if system != "" {
		body["system"] = system
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if req.Temperature > 0 {
		body["temperature"] = req.Temperature
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]interface{}, len(req.Tools))
		for i, tool := range req.Tools {
			if tool.Function != nil {
				tools[i] = map[string]interface{}{
					"name":         tool.Function.Name,
					"description":  tool.Function.Description,
					"input_schema": tool.Function.Parameters,
				}
			}
		}
		body["tools"] = tools
	}
	if stream {
		body["stream"] = true
	}

	data, _ := json.Marshal(body)
	return data
}

func (p *AnthropicProvider) ParseResponse(body []byte) (string, int, int, error) {
	var resp map[string]interface{}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", 0, 0, err
	}

	// Anthropic response: {"content": [{"type":"text","text":"..."}], "usage": {...}}
	text := ""
	if content, ok := resp["content"].([]interface{}); ok && len(content) > 0 {
		if block, ok := content[0].(map[string]interface{}); ok {
			text, _ = block["text"].(string)
		}
	}

	inputTokens, outputTokens := 0, 0
	if usage, ok := resp["usage"].(map[string]interface{}); ok {
		if pt, ok := usage["input_tokens"].(float64); ok {
			inputTokens = int(pt)
		}
		if ct, ok := usage["output_tokens"].(float64); ok {
			outputTokens = int(ct)
		}
	}

	return text, inputTokens, outputTokens, nil
}

func (p *AnthropicProvider) ParseStreamChunk(data []byte) (string, int, int) {
	var event map[string]interface{}
	if err := json.Unmarshal(data, &event); err != nil {
		return "", 0, 0
	}

	text := ""
	eventType, _ := event["type"].(string)

	switch eventType {
	case "content_block_delta":
		if delta, ok := event["delta"].(map[string]interface{}); ok {
			text, _ = delta["text"].(string)
		}
	case "message_delta":
		// Final chunk may contain usage
	}

	inputTokens, outputTokens := 0, 0
	if usage, ok := event["usage"].(map[string]interface{}); ok {
		if pt, ok := usage["input_tokens"].(float64); ok {
			inputTokens = int(pt)
		}
		if ct, ok := usage["output_tokens"].(float64); ok {
			outputTokens = int(ct)
		}
	}

	return text, inputTokens, outputTokens
}

func (p *AnthropicProvider) StreamDataPrefix() string { return "data: " }

func (p *AnthropicProvider) Models() []string     { return p.cfg.AllowedModels }
func (p *AnthropicProvider) DefaultModel() string { return p.cfg.DefaultModel }

func (p *AnthropicProvider) TestConnection() (string, bool, error) {
	if p.cfg.APIKey == "" {
		return "API key not configured", false, nil
	}

	// Anthropic doesn't have a /models endpoint, so we send a minimal request
	httpReq, err := http.NewRequest("POST", p.cfg.BaseURL+"/messages", bytes.NewReader([]byte(`{"model":"claude-3-haiku-20240307","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`)))
	if err != nil {
		return "Failed to create request: " + err.Error(), false, err
	}
	p.setHeaders(httpReq)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return "Failed to connect: " + err.Error(), false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 {
		return "Invalid API key", false, nil
	}
	if resp.StatusCode >= 500 {
		return "API returned status: " + resp.Status, false, nil
	}
	return "Connected successfully", true, nil
}

func (p *AnthropicProvider) FetchModels() ([]string, error) {
	// Anthropic doesn't have a public models endpoint, return known models
	return []string{
		"claude-sonnet-4-20250514",
		"claude-sonnet-4-20250507",
		"claude-3-opus-20240229",
		"claude-3-sonnet-20240229",
		"claude-3-haiku-20240307",
	}, nil
}

func (p *AnthropicProvider) ParseToolCalls(body []byte) ([]ToolCall, error) {
	var resp map[string]interface{}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}

	content, ok := resp["content"].([]interface{})
	if !ok || len(content) == 0 {
		return nil, nil
	}

	var toolCalls []ToolCall
	for _, blockRaw := range content {
		block, ok := blockRaw.(map[string]interface{})
		if !ok {
			continue
		}

		if block["type"] == "tool_use" {
			id, _ := block["id"].(string)
			name, _ := block["name"].(string)
			input, _ := block["input"].(map[string]interface{})
			argsJSON, _ := json.Marshal(input)

			toolCalls = append(toolCalls, ToolCall{
				ID:        id,
				Name:      name,
				Arguments: string(argsJSON),
			})
		}
	}

	return toolCalls, nil
}

func (p *AnthropicProvider) ParseStreamToolCall(data []byte) (interface{}, string) {
	return nil, ""
}
