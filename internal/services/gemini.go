package services

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
	"ai-gateway/internal/models"

	"gorm.io/gorm"
)

type GeminiService struct {
	db              *gorm.DB
	cfg             *config.Config
	onRequestLogged func() // called after each request is logged, used for live dashboard updates
}

func NewGeminiService(db *gorm.DB, cfg *config.Config) *GeminiService {
	return &GeminiService{db: db, cfg: cfg}
}

// GetConfig returns the service's config for access to provider configurations.
func (s *GeminiService) GetConfig() *config.Config {
	return s.cfg
}

// geminiProvider returns the gemini provider config, or a zero-value if not configured.
func (s *GeminiService) geminiProvider() config.ProviderConfig {
	if p := s.cfg.GetProvider("gemini"); p != nil {
		return *p
	}
	return config.ProviderConfig{TimeoutSeconds: 120}
}

// SetOnRequestLogged registers a callback that fires after each request is logged.
// Used by the dashboard WebSocket hub to push live updates.
func (s *GeminiService) SetOnRequestLogged(fn func()) {
	s.onRequestLogged = fn
}

type GeminiRequest struct {
	Contents          []Content         `json:"contents"`
	GenerationConfig  *GenerationConfig `json:"generationConfig,omitempty"`
	SystemInstruction *Content          `json:"systemInstruction,omitempty"`
}

type Content struct {
	Role  string `json:"role,omitempty"`
	Parts []Part `json:"parts"`
}

type Part struct {
	Text string `json:"text,omitempty"`
}

type GenerationConfig struct {
	MaxOutputTokens int      `json:"maxOutputTokens,omitempty"`
	Temperature     float64  `json:"temperature,omitempty"`
	TopP            float64  `json:"topP,omitempty"`
	TopK            int      `json:"topK,omitempty"`
	CandidateCount  int      `json:"candidateCount,omitempty"`
	StopSequences   []string `json:"stopSequences,omitempty"`
}

type GeminiResponse struct {
	Candidates     []Candidate     `json:"candidates"`
	PromptFeedback *PromptFeedback `json:"promptFeedback,omitempty"`
	UsageMetadata  *UsageMetadata  `json:"usageMetadata,omitempty"`
}

type Candidate struct {
	Content       Content        `json:"content"`
	FinishReason  string         `json:"finishReason"`
	Index         int            `json:"index"`
	SafetyRatings []SafetyRating `json:"safetyRatings"`
}

type PromptFeedback struct {
	SafetyRatings []SafetyRating `json:"safetyRatings"`
}

type SafetyRating struct {
	Category    string `json:"category"`
	Probability string `json:"probability"`
}

type UsageMetadata struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}

// resolveModel applies the allowed model check and falls back to the default if needed.
func (s *GeminiService) resolveModel(model string) string {
	gp := s.geminiProvider()
	if !s.isModelAllowed(model) {
		if gp.DefaultModel != "" {
			model = gp.DefaultModel
		}
	}
	return model
}

func (s *GeminiService) ForwardRequest(model string, body []byte) ([]byte, int, error) {
	gp := s.geminiProvider()
	model = s.resolveModel(model)

	log.Printf("[GEMINI] ForwardRequest model=%s, default=%s, allowed=%v", model, gp.DefaultModel, gp.AllowedModels)

	baseURL := gp.BaseURL
	if baseURL == "" {
		baseURL = "https://generativelanguage.googleapis.com/v1beta"
	}
	url := fmt.Sprintf("%s/models/%s:generateContent?key=%s", baseURL, model, gp.APIKey)
	log.Printf("[GEMINI] URL: %s", url)

	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{
		Timeout: time.Duration(gp.TimeoutSeconds) * time.Second,
	}

	resp, err := client.Do(req)
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

// ForwardStreamRequest calls Gemini's streamGenerateContent endpoint and returns
// the raw HTTP response. The caller is responsible for closing the response body.
func (s *GeminiService) ForwardStreamRequest(model string, body []byte) (*http.Response, string, error) {
	gp := s.geminiProvider()
	model = s.resolveModel(model)

	log.Printf("[GEMINI] ForwardStreamRequest model=%s, default=%s, allowed=%v", model, gp.DefaultModel, gp.AllowedModels)

	baseURL := gp.BaseURL
	if baseURL == "" {
		baseURL = "https://generativelanguage.googleapis.com/v1beta"
	}
	url := fmt.Sprintf("%s/models/%s:streamGenerateContent?alt=sse&key=%s", baseURL, model, gp.APIKey)
	log.Printf("[GEMINI] Stream URL: %s", url)

	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, model, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{
		Timeout: time.Duration(gp.TimeoutSeconds) * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, model, fmt.Errorf("failed to send request: %w", err)
	}

	return resp, model, nil
}

func (s *GeminiService) LogRequest(clientID, model string, statusCode int, inputTokens, outputTokens int, latencyMs int, errMsg string, requestBody string) error {
	log := &models.RequestLog{
		ClientID:     clientID,
		Model:        model,
		StatusCode:   statusCode,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		LatencyMs:    latencyMs,
		ErrorMessage: errMsg,
		RequestBody:  requestBody,
		CreatedAt:    time.Now(),
	}

	if err := s.db.Create(log).Error; err != nil {
		return fmt.Errorf("failed to log request: %w", err)
	}

	err := s.updateDailyUsage(clientID, inputTokens, outputTokens, statusCode)

	// Notify dashboard hub about the new request
	if s.onRequestLogged != nil {
		s.onRequestLogged()
	}

	return err
}

func (s *GeminiService) updateDailyUsage(clientID string, inputTokens, outputTokens, statusCode int) error {
	today := time.Now().Truncate(24 * time.Hour)

	var usage models.DailyUsage
	err := s.db.Where("client_id = ? AND date = ?", clientID, today).First(&usage).Error

	if err != nil && err != gorm.ErrRecordNotFound {
		return err
	}

	if err == gorm.ErrRecordNotFound {
		usage = models.DailyUsage{
			ClientID: clientID,
			Date:     today,
		}
	}

	usage.TotalRequests++
	usage.TotalInputTokens += inputTokens
	usage.TotalOutputTokens += outputTokens

	if err := s.db.Save(&usage).Error; err != nil {
		return fmt.Errorf("failed to update daily usage: %w", err)
	}

	return nil
}

func (s *GeminiService) isModelAllowed(model string) bool {
	for _, allowed := range s.geminiProvider().AllowedModels {
		if model == allowed {
			return true
		}
	}
	return false
}

func (s *GeminiService) GetAllowedModels() []string {
	return s.geminiProvider().AllowedModels
}

func (s *GeminiService) GetDefaultModel() string {
	return s.geminiProvider().DefaultModel
}

func ParseGeminiResponse(body []byte) (int, int, error) {
	var resp GeminiResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0, 0, nil
	}

	if resp.UsageMetadata == nil {
		return 0, 0, nil
	}

	return resp.UsageMetadata.PromptTokenCount, resp.UsageMetadata.CandidatesTokenCount, nil
}

func (s *GeminiService) GetBaseURL() string {
	return "https://generativelanguage.googleapis.com/v1beta"
}

func (s *GeminiService) TestConnection() (string, bool, error) {
	gp := s.geminiProvider()
	if gp.APIKey == "" {
		return "API key not configured", false, nil
	}

	url := "https://generativelanguage.googleapis.com/v1/models?key=" + gp.APIKey

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

func (s *GeminiService) FetchAvailableModels() ([]string, error) {
	gp := s.geminiProvider()
	if gp.APIKey == "" {
		return nil, fmt.Errorf("API key not configured")
	}

	models := make([]string, 0)

	for _, baseURL := range []string{"https://generativelanguage.googleapis.com/v1", "https://generativelanguage.googleapis.com/v1beta"} {
		url := baseURL + "/models?key=" + gp.APIKey

		resp, err := http.Get(url)
		if err != nil {
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			continue
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			continue
		}

		var result struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}

		if err := json.Unmarshal(body, &result); err != nil {
			continue
		}

		for _, m := range result.Models {
			modelName := m.Name
			if strings.HasPrefix(modelName, "models/") {
				modelName = strings.TrimPrefix(modelName, "models/")
			}
			found := false
			for _, existing := range models {
				if existing == modelName {
					found = true
					break
				}
			}
			if !found {
				models = append(models, modelName)
			}
		}
	}

	if len(models) == 0 {
		return nil, fmt.Errorf("no models found - check API key")
	}

	return models, nil
}
