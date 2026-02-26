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

// AzureOpenAIProvider implements the Provider interface for Azure OpenAI Service.
// Azure uses a different URL scheme and api-key header compared to standard OpenAI.
// URL pattern: {base_url}/openai/deployments/{model}/chat/completions?api-version={version}
type AzureOpenAIProvider struct {
	cfg        config.ProviderConfig
	apiVersion string
}

func NewAzureOpenAIProvider(cfg config.ProviderConfig) *AzureOpenAIProvider {
	return &AzureOpenAIProvider{
		cfg:        cfg,
		apiVersion: "2024-10-21",
	}
}

func (p *AzureOpenAIProvider) Name() string { return "azure-openai" }

func (p *AzureOpenAIProvider) WithBaseURL(url string) Provider {
	newCfg := p.cfg
	newCfg.BaseURL = url
	return &AzureOpenAIProvider{cfg: newCfg, apiVersion: p.apiVersion}
}

func (p *AzureOpenAIProvider) endpointURL(model string, stream bool) string {
	return fmt.Sprintf("%s/openai/deployments/%s/chat/completions?api-version=%s", p.cfg.BaseURL, model, p.apiVersion)
}

func (p *AzureOpenAIProvider) ChatCompletion(req *ChatRequest) ([]byte, int, error) {
	model := req.Model
	if model == "" {
		model = p.cfg.DefaultModel
	}

	body := p.buildRequestBody(req, false)
	url := p.endpointURL(model, false)

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

func (p *AzureOpenAIProvider) ChatCompletionStream(req *ChatRequest) (*http.Response, error) {
	model := req.Model
	if model == "" {
		model = p.cfg.DefaultModel
	}

	body := p.buildRequestBody(req, true)
	url := p.endpointURL(model, true)

	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	p.setHeaders(httpReq)

	client := &http.Client{Timeout: time.Duration(p.cfg.TimeoutSeconds) * time.Second}
	return client.Do(httpReq)
}

func (p *AzureOpenAIProvider) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if p.cfg.APIKey != "" {
		req.Header.Set("api-key", p.cfg.APIKey)
	}
}

func (p *AzureOpenAIProvider) buildRequestBody(req *ChatRequest, stream bool) []byte {
	messages := make([]map[string]string, len(req.Messages))
	for i, m := range req.Messages {
		messages[i] = map[string]string{"role": m.Role, "content": m.Content}
	}

	// Azure does not accept the model field in the body (it's in the URL path)
	body := map[string]interface{}{
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

func (p *AzureOpenAIProvider) ParseResponse(body []byte) (string, int, int, error) {
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

func (p *AzureOpenAIProvider) ParseStreamChunk(data []byte) (string, int, int) {
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

func (p *AzureOpenAIProvider) StreamDataPrefix() string { return "data: " }

func (p *AzureOpenAIProvider) Models() []string     { return p.cfg.AllowedModels }
func (p *AzureOpenAIProvider) DefaultModel() string { return p.cfg.DefaultModel }

func (p *AzureOpenAIProvider) TestConnection() (string, bool, error) {
	if p.cfg.APIKey == "" {
		return "API key not configured", false, nil
	}
	if p.cfg.BaseURL == "" {
		return "Base URL not configured (requires Azure resource endpoint)", false, nil
	}
	// Azure doesn't have a simple /models endpoint -- just confirm reachability
	url := fmt.Sprintf("%s/openai/models?api-version=%s", p.cfg.BaseURL, p.apiVersion)
	httpReq, _ := http.NewRequest("GET", url, nil)
	p.setHeaders(httpReq)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return "Failed to connect: " + err.Error(), false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return "Invalid API key", false, nil
	}
	if resp.StatusCode >= 500 {
		return "API returned status: " + resp.Status, false, nil
	}
	return "Connected successfully", true, nil
}

func (p *AzureOpenAIProvider) FetchModels() ([]string, error) {
	if p.cfg.BaseURL == "" {
		return nil, fmt.Errorf("Base URL not configured")
	}
	url := fmt.Sprintf("%s/openai/models?api-version=%s", p.cfg.BaseURL, p.apiVersion)
	httpReq, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	p.setHeaders(httpReq)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("API returned status: %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"value"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	var models []string
	for _, m := range result.Data {
		models = append(models, m.ID)
	}
	return models, nil
}
