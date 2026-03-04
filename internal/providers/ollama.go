package providers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"ai-gateway/internal/config"
)

type OllamaProvider struct {
	name string
	cfg  config.ProviderConfig
}

func NewOllamaProvider(name string, cfg config.ProviderConfig) *OllamaProvider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "http://localhost:11434"
	}
	if name == "" {
		name = "ollama"
	}
	return &OllamaProvider{name: name, cfg: cfg}
}

func (p *OllamaProvider) Name() string { return p.name }

func (p *OllamaProvider) WithBaseURL(url string) Provider {
	newCfg := p.cfg
	newCfg.BaseURL = url
	return &OllamaProvider{name: p.name, cfg: newCfg}
}

func (p *OllamaProvider) ChatCompletion(req *ChatRequest) ([]byte, int, error) {
	body := p.buildRequestBody(req, false)
	url := p.cfg.BaseURL + "/api/chat"

	if isDebug() {
		log.Printf("[%s] Request to %s: %s", p.name, url, string(body))
	}

	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("failed to create request: %w", err)
	}
	p.setHeaders(httpReq)

	client := getHTTPClient()
	client.Timeout = time.Duration(p.cfg.TimeoutSeconds) * time.Second
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("failed to read response: %w", err)
	}

	if isDebug() {
		log.Printf("[%s] Response: %d - %s", p.name, resp.StatusCode, string(respBody))
	}

	return respBody, resp.StatusCode, nil
}

func (p *OllamaProvider) ChatCompletionStream(req *ChatRequest) (*http.Response, error) {
	body := p.buildRequestBody(req, true)
	url := p.cfg.BaseURL + "/api/chat"

	log.Printf("[%s] Stream request to %s", p.name, url)

	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	p.setHeaders(httpReq)

	client := getHTTPClient()
	client.Timeout = time.Duration(p.cfg.TimeoutSeconds) * time.Second
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}

	return resp, nil
}

func (p *OllamaProvider) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if p.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	}
}

func (p *OllamaProvider) buildRequestBody(req *ChatRequest, stream bool) []byte {
	model := req.Model
	if model == "" {
		model = p.cfg.DefaultModel
	}

	messages := make([]map[string]interface{}, len(req.Messages))
	for i, m := range req.Messages {
		msg := map[string]interface{}{"role": m.Role, "content": m.Content}
		if m.Role == "tool" && m.ToolCallID != "" {
			// Ollama doesn't explicitly mention tool_call_id in its messages spec, 
			// but for chat history it's often needed.
		}
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			toolCalls := make([]map[string]interface{}, len(m.ToolCalls))
			for j, tc := range m.ToolCalls {
				// Ollama expects tool calls in a specific format
				toolCalls[j] = map[string]interface{}{
					"function": map[string]interface{}{
						"name":      tc.Name,
						"arguments": p.parseArguments(tc.Arguments),
					},
				}
			}
			msg["tool_calls"] = toolCalls
		}
		messages[i] = msg
	}

	options := make(map[string]interface{})
	if req.Temperature > 0 {
		options["temperature"] = req.Temperature
	}
	if req.MaxTokens > 0 {
		options["num_predict"] = req.MaxTokens
	}

	body := map[string]interface{}{
		"model":    model,
		"messages": messages,
		"stream":   stream,
	}

	if len(options) > 0 {
		body["options"] = options
	}

	if len(req.Tools) > 0 {
		ollamaTools := make([]map[string]interface{}, len(req.Tools))
		for i, t := range req.Tools {
			if t.Function != nil {
				ollamaTools[i] = map[string]interface{}{
					"type": "function",
					"function": map[string]interface{}{
						"name":        t.Function.Name,
						"description": t.Function.Description,
						"parameters":  t.Function.Parameters,
					},
				}
			}
		}
		body["tools"] = ollamaTools
	}

	if req.ResponseFormat != nil {
		// Ollama supports "json" or a JSON schema
		body["format"] = req.ResponseFormat
	}

	data, _ := json.Marshal(body)
	return data
}

func (p *OllamaProvider) parseArguments(args string) interface{} {
	var result interface{}
	if err := json.Unmarshal([]byte(args), &result); err != nil {
		return args
	}
	return result
}

func (p *OllamaProvider) ParseResponse(body []byte) (string, int, int, error) {
	var resp struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		PromptEvalCount int `json:"prompt_eval_count"`
		EvalCount       int `json:"eval_count"`
	}

	if err := json.Unmarshal(body, &resp); err != nil {
		return "", 0, 0, err
	}

	return resp.Message.Content, resp.PromptEvalCount, resp.EvalCount, nil
}

func (p *OllamaProvider) ParseStreamChunk(data []byte) (string, int, int) {
	var chunk struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		Done            bool `json:"done"`
		PromptEvalCount int  `json:"prompt_eval_count"`
		EvalCount       int  `json:"eval_count"`
	}

	if err := json.Unmarshal(data, &chunk); err != nil {
		return "", 0, 0
	}

	return chunk.Message.Content, chunk.PromptEvalCount, chunk.EvalCount
}

func (p *OllamaProvider) StreamDataPrefix() string {
	return ""
}

func (p *OllamaProvider) Models() []string {
	return p.cfg.AllowedModels
}

func (p *OllamaProvider) DefaultModel() string {
	return p.cfg.DefaultModel
}

func (p *OllamaProvider) TestConnection() (string, bool, error) {
	url := p.cfg.BaseURL + "/api/tags"
	httpReq, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "Failed to create request", false, err
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return "Failed to connect", false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Sprintf("API returned status: %d", resp.StatusCode), false, nil
	}

	return "Connected", true, nil
}

func (p *OllamaProvider) FetchModels() ([]string, error) {
	url := p.cfg.BaseURL + "/api/tags"
	httpReq, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("API returned status: %d", resp.StatusCode)
	}

	var ollamaResp struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ollamaResp); err != nil {
		return nil, err
	}

	models := make([]string, len(ollamaResp.Models))
	for i, m := range ollamaResp.Models {
		models[i] = m.Name
	}
	return models, nil
}

func (p *OllamaProvider) ParseToolCalls(body []byte) ([]ToolCall, error) {
	var resp struct {
		Message struct {
			ToolCalls []struct {
				Function struct {
					Name      string      `json:"name"`
					Arguments interface{} `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
	}

	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}

	var toolCalls []ToolCall
	for _, tc := range resp.Message.ToolCalls {
		args, _ := json.Marshal(tc.Function.Arguments)
		toolCalls = append(toolCalls, ToolCall{
			ID:        "", // Ollama doesn't seem to provide tool call IDs in native API
			Name:      tc.Function.Name,
			Arguments: string(args),
		})
	}

	return toolCalls, nil
}

func (p *OllamaProvider) ParseStreamToolCall(data []byte) (interface{}, string) {
	var chunk struct {
		Message struct {
			ToolCalls []struct {
				Function struct {
					Name      string      `json:"name"`
					Arguments interface{} `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
		DoneReason string `json:"done_reason"`
	}

	if err := json.Unmarshal(data, &chunk); err != nil {
		return nil, ""
	}

	if len(chunk.Message.ToolCalls) == 0 {
		return nil, chunk.DoneReason
	}

	tc := chunk.Message.ToolCalls[0]
	args, _ := json.Marshal(tc.Function.Arguments)

	return &StreamToolCall{
		ID:        "",
		Name:      tc.Function.Name,
		Arguments: string(args),
		Index:     0,
	}, chunk.DoneReason
}
