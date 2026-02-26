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

func convertResponseFormat(responseFormat any, genConfig map[string]interface{}) {
	if responseFormat == nil {
		return
	}
	rf, ok := responseFormat.(map[string]interface{})
	if !ok {
		return
	}
	rfType, _ := rf["type"].(string)
	if rfType == "json_schema" {
		js, _ := rf["json_schema"].(map[string]interface{})
		if js != nil {
			schema, _ := js["schema"].(map[string]interface{})
			if schema != nil {
				genConfig["responseMimeType"] = "application/json"
				genConfig["responseSchema"] = schema
			}
		}
	} else if rfType == "json_object" {
		genConfig["responseMimeType"] = "application/json"
	}
}

// GeminiProvider implements the Provider interface for Google's Gemini API.
type GeminiProvider struct {
	cfg config.ProviderConfig
}

func NewGeminiProvider(cfg config.ProviderConfig) *GeminiProvider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://generativelanguage.googleapis.com/v1beta"
	}
	return &GeminiProvider{cfg: cfg}
}

func (p *GeminiProvider) Name() string { return "gemini" }

func (p *GeminiProvider) ChatCompletion(req *ChatRequest) ([]byte, int, error) {
	model := req.Model
	if model == "" {
		model = p.cfg.DefaultModel
	}

	body := p.buildRequestBody(req)
	url := fmt.Sprintf("%s/models/%s:generateContent?key=%s", p.cfg.BaseURL, model, p.cfg.APIKey)

	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

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

	return respBody, resp.StatusCode, nil
}

func (p *GeminiProvider) ChatCompletionStream(req *ChatRequest) (*http.Response, error) {
	model := req.Model
	if model == "" {
		model = p.cfg.DefaultModel
	}

	body := p.buildRequestBody(req)
	url := fmt.Sprintf("%s/models/%s:streamGenerateContent?alt=sse&key=%s", p.cfg.BaseURL, model, p.cfg.APIKey)

	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	client := getHTTPClient()
	client.Timeout = time.Duration(p.cfg.TimeoutSeconds) * time.Second
	return client.Do(httpReq)
}

func (p *GeminiProvider) buildRequestBody(req *ChatRequest) []byte {
	contents := make([]map[string]interface{}, 0)
	var systemInstruction *map[string]interface{}

	for _, msg := range req.Messages {
		if msg.Content == "" {
			continue
		}
		if msg.Role == "system" {
			si := map[string]interface{}{
				"parts": []map[string]interface{}{{"text": msg.Content}},
			}
			systemInstruction = &si
			continue
		}
		role := "user"
		if msg.Role == "assistant" {
			role = "model"
		}
		contents = append(contents, map[string]interface{}{
			"role":  role,
			"parts": []map[string]interface{}{{"text": msg.Content}},
		})
	}

	geminiReq := map[string]interface{}{"contents": contents}
	if systemInstruction != nil {
		geminiReq["systemInstruction"] = *systemInstruction
	}

	if len(req.Tools) > 0 {
		tools := make([]map[string]interface{}, len(req.Tools))
		for i, tool := range req.Tools {
			if tool.Function != nil {
				tools[i] = map[string]interface{}{
					"functionDeclarations": []map[string]interface{}{
						{
							"name":        tool.Function.Name,
							"description": tool.Function.Description,
							"parameters":  tool.Function.Parameters,
						},
					},
				}
			}
		}
		geminiReq["tools"] = tools
	}

	genConfig := map[string]interface{}{}
	if req.MaxTokens > 0 {
		genConfig["maxOutputTokens"] = req.MaxTokens
	}
	if req.Temperature > 0 {
		genConfig["temperature"] = req.Temperature
	}
	if req.ResponseFormat != nil {
		convertResponseFormat(req.ResponseFormat, genConfig)
	}
	if len(genConfig) > 0 {
		geminiReq["generationConfig"] = genConfig
	}

	data, _ := json.Marshal(geminiReq)
	return data
}

func (p *GeminiProvider) ParseResponse(body []byte) (string, int, int, error) {
	var resp map[string]interface{}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", 0, 0, err
	}

	text := extractNestedText(resp, "candidates", "content", "parts", "text")
	inputTokens, outputTokens := extractGeminiUsage(resp)
	return text, inputTokens, outputTokens, nil
}

func (p *GeminiProvider) ParseStreamChunk(data []byte) (string, int, int) {
	var chunk map[string]interface{}
	if err := json.Unmarshal(data, &chunk); err != nil {
		return "", 0, 0
	}

	text := extractNestedText(chunk, "candidates", "content", "parts", "text")
	inputTokens, outputTokens := extractGeminiUsage(chunk)
	return text, inputTokens, outputTokens
}

func (p *GeminiProvider) StreamDataPrefix() string { return "data: " }

func (p *GeminiProvider) Models() []string     { return p.cfg.AllowedModels }
func (p *GeminiProvider) DefaultModel() string { return p.cfg.DefaultModel }

func (p *GeminiProvider) TestConnection() (string, bool, error) {
	if p.cfg.APIKey == "" {
		return "API key not configured", false, nil
	}
	url := "https://generativelanguage.googleapis.com/v1/models?key=" + p.cfg.APIKey
	resp, err := http.Get(url)
	if err != nil {
		return "Failed to connect: " + err.Error(), false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "API returned status: " + resp.Status, false, nil
	}
	return "Connected successfully", true, nil
}

func (p *GeminiProvider) FetchModels() ([]string, error) {
	if p.cfg.APIKey == "" {
		return nil, fmt.Errorf("API key not configured")
	}
	url := "https://generativelanguage.googleapis.com/v1/models?key=" + p.cfg.APIKey
	resp, err := http.Get(url)
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
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	var models []string
	for _, m := range result.Models {
		models = append(models, m.Name)
	}
	return models, nil
}

// Helper functions for parsing Gemini's nested JSON structure
func extractNestedText(resp map[string]interface{}, keys ...string) string {
	candidates, ok := resp["candidates"].([]interface{})
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

func extractGeminiUsage(resp map[string]interface{}) (int, int) {
	usage, ok := resp["usageMetadata"].(map[string]interface{})
	if !ok {
		return 0, 0
	}
	inputTokens := 0
	outputTokens := 0
	if pt, ok := usage["promptTokenCount"].(float64); ok {
		inputTokens = int(pt)
	}
	if ct, ok := usage["candidatesTokenCount"].(float64); ok {
		outputTokens = int(ct)
	}
	return inputTokens, outputTokens
}

func (p *GeminiProvider) ParseToolCalls(body []byte) ([]ToolCall, error) {
	var resp map[string]interface{}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}

	candidates, ok := resp["candidates"].([]interface{})
	if !ok || len(candidates) == 0 {
		return nil, nil
	}

	candidate, ok := candidates[0].(map[string]interface{})
	if !ok {
		return nil, nil
	}

	content, ok := candidate["content"].(map[string]interface{})
	if !ok {
		return nil, nil
	}

	parts, ok := content["parts"].([]interface{})
	if !ok || len(parts) == 0 {
		return nil, nil
	}

	var toolCalls []ToolCall
	for _, partRaw := range parts {
		part, ok := partRaw.(map[string]interface{})
		if !ok {
			continue
		}

		fnCall, ok := part["functionCall"].(map[string]interface{})
		if !ok {
			continue
		}

		name, _ := fnCall["name"].(string)
		argsMap, _ := fnCall["args"].(map[string]interface{})
		argsJSON, _ := json.Marshal(argsMap)

		toolCalls = append(toolCalls, ToolCall{
			ID:        "",
			Name:      name,
			Arguments: string(argsJSON),
		})
	}

	return toolCalls, nil
}

func (p *GeminiProvider) ParseStreamToolCall(data []byte) (interface{}, string) {
	return nil, ""
}
