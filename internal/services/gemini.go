package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"gemini-proxy/internal/config"
	"gemini-proxy/internal/models"

	"gorm.io/gorm"
)

type GeminiService struct {
	db  *gorm.DB
	cfg *config.Config
}

func NewGeminiService(db *gorm.DB, cfg *config.Config) *GeminiService {
	return &GeminiService{db: db, cfg: cfg}
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

func (s *GeminiService) ForwardRequest(model string, body []byte) ([]byte, int, error) {
	if !s.isModelAllowed(model) {
		if s.cfg.Gemini.DefaultModel != "" {
			model = s.cfg.Gemini.DefaultModel
		}
	}

	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", model, s.cfg.Gemini.APIKey)

	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{
		Timeout: time.Duration(s.cfg.Gemini.TimeoutSeconds) * time.Second,
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

func (s *GeminiService) LogRequest(clientID, model string, statusCode int, inputTokens, outputTokens int, latencyMs int, errMsg string) error {
	log := &models.RequestLog{
		ClientID:     clientID,
		Model:        model,
		StatusCode:   statusCode,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		LatencyMs:    latencyMs,
		ErrorMessage: errMsg,
		CreatedAt:    time.Now(),
	}

	if err := s.db.Create(log).Error; err != nil {
		return fmt.Errorf("failed to log request: %w", err)
	}

	return s.updateDailyUsage(clientID, inputTokens, outputTokens, statusCode)
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
	for _, allowed := range s.cfg.Gemini.AllowedModels {
		if model == allowed {
			return true
		}
	}
	return false
}

func (s *GeminiService) GetAllowedModels() []string {
	return s.cfg.Gemini.AllowedModels
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
