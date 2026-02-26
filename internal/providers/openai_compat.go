package providers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"ai-gateway/internal/config"
)

// OpenAICompatProvider implements the Provider interface for any OpenAI-compatible API.
// This covers OpenAI, Mistral, Ollama, LM Studio, and any other backend that
// speaks the OpenAI chat completions protocol.
type OpenAICompatProvider struct {
	name string
	cfg  config.ProviderConfig
}

func NewOpenAIProvider(name string, cfg config.ProviderConfig) *OpenAICompatProvider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.openai.com/v1"
	}
	if name == "" {
		name = "openai"
	}
	return &OpenAICompatProvider{name: name, cfg: cfg}
}

func NewMistralProvider(cfg config.ProviderConfig) *OpenAICompatProvider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.mistral.ai/v1"
	}
	return &OpenAICompatProvider{name: "mistral", cfg: cfg}
}

func NewOllamaProvider(name string, cfg config.ProviderConfig) *OpenAICompatProvider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "http://localhost:11434"
	}
	if name == "" {
		name = "ollama"
	}
	return &OpenAICompatProvider{name: name, cfg: cfg}
}

func NewLMStudioProvider(name string, cfg config.ProviderConfig) *OpenAICompatProvider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "http://localhost:1234/v1"
	}
	if name == "" {
		name = "lmstudio"
	}
	return &OpenAICompatProvider{name: name, cfg: cfg}
}

func NewPerplexityProvider(cfg config.ProviderConfig) *OpenAICompatProvider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.perplexity.ai"
	}
	return &OpenAICompatProvider{name: "perplexity", cfg: cfg}
}

func NewXAIProvider(cfg config.ProviderConfig) *OpenAICompatProvider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.x.ai/v1"
	}
	return &OpenAICompatProvider{name: "xai", cfg: cfg}
}

func NewCohereProvider(cfg config.ProviderConfig) *OpenAICompatProvider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.cohere.com/v2"
	}
	return &OpenAICompatProvider{name: "cohere", cfg: cfg}
}

func (p *OpenAICompatProvider) Name() string { return p.name }

// WithBaseURL returns a new provider instance with the given base URL, keeping
// all other configuration the same.
func (p *OpenAICompatProvider) WithBaseURL(url string) Provider {
	newCfg := p.cfg
	newCfg.BaseURL = url
	return &OpenAICompatProvider{name: p.name, cfg: newCfg}
}

func (p *OpenAICompatProvider) ChatCompletion(req *ChatRequest) ([]byte, int, error) {
	body := p.buildRequestBody(req, false)

	// Determine the correct endpoint based on provider type
	var url string
	switch p.name {
	case "ollama":
		url = p.cfg.BaseURL + "/api/chat"
	case "lmstudio":
		// LM Studio base URL already includes /v1
		url = p.cfg.BaseURL + "/chat/completions"
	default:
		// OpenAI, Mistral, etc. - base URL already includes /v1
		url = p.cfg.BaseURL + "/chat/completions"
	}

	log.Printf("[%s] Request to %s: %s", p.name, url, string(body))

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

	log.Printf("[%s] Response: %d - %s", p.name, resp.StatusCode, string(respBody))

	return respBody, resp.StatusCode, nil
}

func (p *OpenAICompatProvider) ChatCompletionStream(req *ChatRequest) (*http.Response, error) {
	body := p.buildRequestBody(req, true)

	// Determine the correct endpoint based on provider type
	var url string
	switch p.name {
	case "ollama":
		url = p.cfg.BaseURL + "/api/chat"
	case "lmstudio":
		// LM Studio base URL already includes /v1
		url = p.cfg.BaseURL + "/chat/completions"
	default:
		// OpenAI, Mistral, etc. - base URL already includes /v1
		url = p.cfg.BaseURL + "/chat/completions"
	}

	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	p.setHeaders(httpReq)

	client := &http.Client{Timeout: time.Duration(p.cfg.TimeoutSeconds) * time.Second}
	return client.Do(httpReq)
}

func (p *OpenAICompatProvider) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if p.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	}
}

func (p *OpenAICompatProvider) buildRequestBody(req *ChatRequest, stream bool) []byte {
	model := req.Model
	if model == "" {
		model = p.cfg.DefaultModel
	}

	messages := make([]map[string]string, len(req.Messages))
	for i, m := range req.Messages {
		messages[i] = map[string]string{"role": m.Role, "content": m.Content}
	}

	body := map[string]interface{}{
		"model":    model,
		"messages": messages,
		"stream":   stream,
	}

	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if req.Temperature > 0 {
		body["temperature"] = req.Temperature
	}

	data, _ := json.Marshal(body)
	return data
}

func (p *OpenAICompatProvider) ParseResponse(body []byte) (string, int, int, error) {
	var resp map[string]interface{}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", 0, 0, err
	}

	text := ""
	if choices, ok := resp["choices"].([]interface{}); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]interface{}); ok {
			if msg, ok := choice["message"].(map[string]interface{}); ok {
				text, _ = msg["content"].(string)
			}
		}
	}

	inputTokens, outputTokens := 0, 0
	if usage, ok := resp["usage"].(map[string]interface{}); ok {
		if pt, ok := usage["prompt_tokens"].(float64); ok {
			inputTokens = int(pt)
		}
		if ct, ok := usage["completion_tokens"].(float64); ok {
			outputTokens = int(ct)
		}
	}

	return text, inputTokens, outputTokens, nil
}

func (p *OpenAICompatProvider) ParseStreamChunk(data []byte) (string, int, int) {
	var chunk map[string]interface{}
	if err := json.Unmarshal(data, &chunk); err != nil {
		return "", 0, 0
	}

	text := ""
	if choices, ok := chunk["choices"].([]interface{}); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]interface{}); ok {
			if delta, ok := choice["delta"].(map[string]interface{}); ok {
				text, _ = delta["content"].(string)
			}
		}
	}

	inputTokens, outputTokens := 0, 0
	if usage, ok := chunk["usage"].(map[string]interface{}); ok {
		if pt, ok := usage["prompt_tokens"].(float64); ok {
			inputTokens = int(pt)
		}
		if ct, ok := usage["completion_tokens"].(float64); ok {
			outputTokens = int(ct)
		}
	}

	return text, inputTokens, outputTokens
}

func (p *OpenAICompatProvider) StreamDataPrefix() string { return "data: " }

func (p *OpenAICompatProvider) Models() []string     { return p.cfg.AllowedModels }
func (p *OpenAICompatProvider) DefaultModel() string { return p.cfg.DefaultModel }

func (p *OpenAICompatProvider) TestConnection() (string, bool, error) {
	// Determine the correct endpoint based on provider type
	var url string
	switch p.name {
	case "ollama":
		url = p.cfg.BaseURL + "/api/tags"
	case "lmstudio":
		// LM Studio base URL already includes /v1
		url = p.cfg.BaseURL + "/models"
	default:
		// OpenAI, Mistral, etc. - base URL already includes /v1
		url = p.cfg.BaseURL + "/models"
	}

	httpReq, err := http.NewRequest("GET", url, nil)
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

	if resp.StatusCode != 200 {
		return "API returned status: " + resp.Status, false, nil
	}
	return "Connected successfully", true, nil
}

func (p *OpenAICompatProvider) FetchModels() ([]string, error) {
	// Determine the models endpoint based on provider type
	var url string
	switch p.name {
	case "ollama":
		url = p.cfg.BaseURL + "/api/tags"
	case "lmstudio":
		// LM Studio - append /v1/models to ensure correct endpoint
		baseURL := p.cfg.BaseURL
		if !strings.HasSuffix(baseURL, "/v1") {
			baseURL = strings.TrimSuffix(baseURL, "/") + "/v1"
		}
		url = baseURL + "/models"
	default:
		// OpenAI, Mistral, etc. - base URL already includes /v1
		url = p.cfg.BaseURL + "/models"
	}

	log.Printf("[%s] FetchModels URL: %s", p.name, url)

	httpReq, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	p.setHeaders(httpReq)

	log.Printf("[%s] FetchModels headers: %v", p.name, httpReq.Header)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	log.Printf("[%s] FetchModels response status: %s", p.name, resp.Status)

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("API returned status: %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	log.Printf("[%s] FetchModels response body: %s", p.name, string(body))

	// Parse the response based on provider type
	var models []string

	switch p.name {
	case "ollama":
		// Ollama: {"models": [{"name": "llama3.2:latest", ...}]}
		var ollamaResp struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		if err := json.Unmarshal(body, &ollamaResp); err != nil {
			return nil, err
		}
		for _, m := range ollamaResp.Models {
			models = append(models, m.Name)
		}
	default:
		// OpenAI-compatible: {"data": [{"id": "gpt-4", ...}]}
		var openaiResp struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &openaiResp); err != nil {
			return nil, err
		}
		for _, m := range openaiResp.Data {
			models = append(models, m.ID)
		}
	}

	return models, nil
}
