package services

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"log"
	"time"

	"ai-gateway/internal/config"
	"ai-gateway/internal/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type ClientService struct {
	db *gorm.DB
}

func NewClientService(db *gorm.DB) *ClientService {
	return &ClientService{db: db}
}

func (s *ClientService) CreateClient(name, description, keyType, keyPrefix string, cfg *config.Config) (*models.Client, string, error) {
	apiKey := GenerateAPIKeyWithPrefix(keyType, keyPrefix)
	apiKeyHash := hashAPIKey(apiKey)

	client := &models.Client{
		ID:                   uuid.New().String(),
		Name:                 name,
		Description:          description,
		APIKeyHash:           apiKeyHash,
		KeyPrefix:            keyPrefix,
		IsActive:             true,
		RateLimitMinute:      cfg.Defaults.RateLimit.RequestsPerMinute,
		RateLimitHour:        cfg.Defaults.RateLimit.RequestsPerHour,
		RateLimitDay:         cfg.Defaults.RateLimit.RequestsPerDay,
		QuotaInputTokensDay:  cfg.Defaults.Quota.MaxInputTokensPerDay,
		QuotaOutputTokensDay: cfg.Defaults.Quota.MaxOutputTokensPerDay,
		QuotaRequestsDay:     cfg.Defaults.Quota.MaxRequestsPerDay,
		MaxInputTokens:       cfg.Defaults.Quota.MaxInputTokens,
		MaxOutputTokens:      cfg.Defaults.Quota.MaxOutputTokens,
		CreatedAt:            time.Now(),
		UpdatedAt:            time.Now(),
	}

	if err := s.db.Create(client).Error; err != nil {
		return nil, "", fmt.Errorf("failed to create client: %w", err)
	}

	return client, apiKey, nil
}

func (s *ClientService) GetClientByAPIKey(apiKey string) (*models.Client, error) {
	apiKeyHash := hashAPIKey(apiKey)
	log.Printf("[CLIENT] Looking up API key: %s... (hash: %x)", apiKey[:min(8, len(apiKey))], apiKeyHash[:8])

	var client models.Client
	err := s.db.Where("api_key_hash = ? AND is_active = ?", apiKeyHash, true).First(&client).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			log.Printf("[CLIENT] No client found for key: %s...", apiKey[:min(8, len(apiKey))])
			return nil, nil
		}
		return nil, err
	}

	log.Printf("[CLIENT] Found client: %s (%s)", client.Name, client.ID)
	return &client, nil
}

func (s *ClientService) GetClientByID(id string) (*models.Client, error) {
	var client models.Client
	err := s.db.Where("id = ?", id).First(&client).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &client, nil
}

func (s *ClientService) GetAllClients() ([]models.Client, error) {
	var clients []models.Client
	err := s.db.Order("created_at DESC").Find(&clients).Error
	return clients, err
}

func (s *ClientService) UpdateClient(client *models.Client) error {
	client.UpdatedAt = time.Now()
	return s.db.Save(client).Error
}

func (s *ClientService) UpdateLastSeen(clientID string) error {
	return s.db.Model(&models.Client{}).Where("id = ?", clientID).Update("last_seen", time.Now()).Error
}

func (s *ClientService) DeleteClient(id string) error {
	return s.db.Delete(&models.Client{}, "id = ?", id).Error
}

func (s *ClientService) RegenerateAPIKey(clientID, keyType, keyPrefix string) (string, error) {
	apiKey := GenerateAPIKeyWithPrefix(keyType, keyPrefix)
	apiKeyHash := hashAPIKey(apiKey)

	err := s.db.Model(&models.Client{}).Where("id = ?", clientID).Updates(map[string]interface{}{
		"api_key_hash": apiKeyHash,
		"key_prefix":   keyPrefix,
		"updated_at":   time.Now(),
	}).Error

	if err != nil {
		return "", err
	}

	return apiKey, nil
}

func (s *ClientService) ValidateAPIKey(apiKey string, storedHash []byte) bool {
	return subtle.ConstantTimeCompare(hashAPIKey(apiKey), storedHash) == 1
}

func (s *ClientService) GetClientsByIDs(ids []string) ([]models.Client, error) {
	var clients []models.Client
	err := s.db.Where("id IN ?", ids).Find(&clients).Error
	return clients, err
}

func GenerateAPIKey(keyType string) string {
	return GenerateAPIKeyWithPrefix(keyType, "")
}

func GenerateAPIKeyWithPrefix(keyType, customPrefix string) string {
	var prefix string
	if customPrefix != "" {
		prefix = customPrefix
	} else {
		switch keyType {
		case "anthropic":
			prefix = "sk-ant-"
		case "openai":
			prefix = "sk-"
		default:
			prefix = "gm_"
		}
	}
	return prefix + uuid.New().String()
}

func generateAPIKey() string {
	return GenerateAPIKey("gemini")
}

func hashAPIKey(apiKey string) []byte {
	hash := sha256.Sum256([]byte(apiKey))
	return hash[:]
}
