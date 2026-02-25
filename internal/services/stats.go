package services

import (
	"time"

	"gemini-proxy/internal/models"

	"gorm.io/gorm"
)

type StatsService struct {
	db *gorm.DB
}

func NewStatsService(db *gorm.DB) *StatsService {
	return &StatsService{db: db}
}

func (s *StatsService) GetGlobalStats() (*models.Stats, error) {
	today := time.Now().Truncate(24 * time.Hour)

	var stats models.Stats

	err := s.db.Model(&models.DailyUsage{}).
		Select(`COALESCE(SUM(total_requests), 0) as total_requests_today,
				COALESCE(SUM(total_input_tokens), 0) as total_input_tokens_today,
				COALESCE(SUM(total_output_tokens), 0) as total_output_tokens_today`).
		Where("date = ?", today).
		Scan(&stats).Error

	if err != nil {
		return nil, err
	}

	s.db.Model(&models.Client{}).
		Where("is_active = ?", true).
		Count(&stats.ActiveClients)

	s.db.Model(&models.Client{}).Count(&stats.TotalClients)

	var errorCount int64
	s.db.Model(&models.RequestLog{}).
		Where("created_at >= ? AND status_code >= 400", today).
		Count(&errorCount)

	var totalCount int64
	s.db.Model(&models.RequestLog{}).
		Where("created_at >= ?", today).
		Count(&totalCount)

	if totalCount > 0 {
		stats.ErrorRate = float64(errorCount) / float64(totalCount) * 100
	} else {
		stats.ErrorRate = 0
	}

	return &stats, nil
}

func (s *StatsService) GetClientStats(clientID string) (*models.ClientStats, error) {
	today := time.Now().Truncate(24 * time.Hour)

	var client models.Client
	if err := s.db.Where("id = ?", clientID).First(&client).Error; err != nil {
		return nil, err
	}

	var usage models.DailyUsage
	err := s.db.Where("client_id = ? AND date = ?", clientID, today).First(&usage).Error

	clientStats := &models.ClientStats{
		ClientID:          client.ID,
		ClientName:        client.Name,
		RequestsToday:     0,
		InputTokensToday:  0,
		OutputTokensToday: 0,
		RequestsLimit:     client.QuotaRequestsDay,
		InputTokensLimit:  client.QuotaInputTokensDay,
		OutputTokensLimit: client.QuotaOutputTokensDay,
	}

	if err == nil {
		clientStats.RequestsToday = usage.TotalRequests
		clientStats.InputTokensToday = usage.TotalInputTokens
		clientStats.OutputTokensToday = usage.TotalOutputTokens
	}

	return clientStats, nil
}

func (s *StatsService) GetAllClientStats() ([]models.ClientStats, error) {
	clients, err := NewClientService(s.db).GetAllClients()
	if err != nil {
		return nil, err
	}

	today := time.Now().Truncate(24 * time.Hour)

	var result []models.ClientStats
	for _, client := range clients {
		var usage models.DailyUsage
		err := s.db.Where("client_id = ? AND date = ?", client.ID, today).First(&usage).Error

		stats := models.ClientStats{
			ClientID:          client.ID,
			ClientName:        client.Name,
			RequestsToday:     0,
			InputTokensToday:  0,
			OutputTokensToday: 0,
			RequestsLimit:     client.QuotaRequestsDay,
			InputTokensLimit:  client.QuotaInputTokensDay,
			OutputTokensLimit: client.QuotaOutputTokensDay,
		}

		if err == nil {
			stats.RequestsToday = usage.TotalRequests
			stats.InputTokensToday = usage.TotalInputTokens
			stats.OutputTokensToday = usage.TotalOutputTokens
		}

		result = append(result, stats)
	}

	return result, nil
}

func (s *StatsService) GetRecentRequests(clientID string, limit int) ([]models.RequestLog, error) {
	var logs []models.RequestLog
	query := s.db.Order("created_at DESC").Limit(limit)

	if clientID != "" {
		query = query.Where("client_id = ?", clientID)
	}

	err := query.Find(&logs).Error
	return logs, err
}

func (s *StatsService) GetModelUsage() (map[string]int, error) {
	today := time.Now().Truncate(24 * time.Hour)

	type Result struct {
		Model string
		Count int
	}

	var results []Result
	err := s.db.Model(&models.RequestLog{}).
		Select("model, COUNT(*) as count").
		Where("created_at >= ?", today).
		Group("model").
		Scan(&results).Error

	if err != nil {
		return nil, err
	}

	usage := make(map[string]int)
	for _, r := range results {
		usage[r.Model] = r.Count
	}

	return usage, nil
}
