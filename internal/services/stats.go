package services

import (
	"time"

	"ai-gateway/internal/models"

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

	// Count clients that were active in the last 5 seconds
	fiveSecondsAgo := time.Now().Add(-5 * time.Second)
	s.db.Model(&models.Client{}).
		Where("is_active = ? AND last_seen > ?", true, fiveSecondsAgo).
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
		MaxInputTokens:    client.MaxInputTokens,
		MaxOutputTokens:   client.MaxOutputTokens,
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

type DailyStats struct {
	Date              time.Time `json:"date"`
	TotalRequests     int       `json:"total_requests"`
	TotalInputTokens  int       `json:"total_input_tokens"`
	TotalOutputTokens int       `json:"total_output_tokens"`
	UniqueClients     int       `json:"unique_clients"`
}

func (s *StatsService) GetHistoricalStats(days int) ([]DailyStats, error) {
	startDate := time.Now().AddDate(0, 0, -days).Truncate(24 * time.Hour)

	var results []DailyStats
	err := s.db.Model(&models.DailyUsage{}).
		Select("date, COALESCE(SUM(total_requests), 0) as total_requests, COALESCE(SUM(total_input_tokens), 0) as total_input_tokens, COALESCE(SUM(total_output_tokens), 0) as total_output_tokens, COUNT(DISTINCT client_id) as unique_clients").
		Where("date >= ?", startDate).
		Group("date").
		Order("date ASC").
		Scan(&results).Error

	if err != nil {
		return nil, err
	}

	return results, nil
}

type HourlyStats struct {
	Hour          time.Time `json:"hour"`
	TotalRequests int       `json:"total_requests"`
	AvgLatencyMs  float64   `json:"avg_latency_ms"`
	ErrorCount    int       `json:"error_count"`
}

func (s *StatsService) GetHourlyStats(hours int) ([]HourlyStats, error) {
	startTime := time.Now().Add(-time.Duration(hours) * time.Hour)

	var results []HourlyStats
	err := s.db.Model(&models.RequestLog{}).
		Select("date_trunc('hour', created_at) as hour, COUNT(*) as total_requests, COALESCE(AVG(latency_ms), 0) as avg_latency_ms, SUM(CASE WHEN status_code >= 400 THEN 1 ELSE 0 END) as error_count").
		Where("created_at >= ?", startTime).
		Group("hour").
		Order("hour ASC").
		Scan(&results).Error

	if err != nil {
		return nil, err
	}

	return results, nil
}

type ModelStats struct {
	Model         string  `json:"model"`
	TotalRequests int     `json:"total_requests"`
	TotalTokens   int     `json:"total_tokens"`
	AvgLatencyMs  float64 `json:"avg_latency_ms"`
	SuccessRate   float64 `json:"success_rate"`
}

func (s *StatsService) GetModelStats(days int) ([]ModelStats, error) {
	startDate := time.Now().AddDate(0, 0, -days)

	type Result struct {
		Model         string
		TotalRequests int
		TotalTokens   int
		AvgLatency    float64
		ErrorCount    int
	}

	var results []Result
	err := s.db.Model(&models.RequestLog{}).
		Select("model, COUNT(*) as total_requests, COALESCE(SUM(input_tokens + output_tokens), 0) as total_tokens, COALESCE(AVG(latency_ms), 0) as avg_latency, SUM(CASE WHEN status_code >= 400 THEN 1 ELSE 0 END) as error_count").
		Where("created_at >= ?", startDate).
		Group("model").
		Order("total_requests DESC").
		Scan(&results).Error

	if err != nil {
		return nil, err
	}

	var modelStats []ModelStats
	for _, r := range results {
		successRate := 100.0
		if r.TotalRequests > 0 {
			successRate = float64(r.TotalRequests-r.ErrorCount) / float64(r.TotalRequests) * 100
		}
		modelStats = append(modelStats, ModelStats{
			Model:         r.Model,
			TotalRequests: r.TotalRequests,
			TotalTokens:   r.TotalTokens,
			AvgLatencyMs:  r.AvgLatency,
			SuccessRate:   successRate,
		})
	}

	return modelStats, nil
}

type ClientStats2 struct {
	ClientID      string  `json:"client_id"`
	ClientName    string  `json:"client_name"`
	TotalRequests int     `json:"total_requests"`
	TotalTokens   int     `json:"total_tokens"`
	SuccessRate   float64 `json:"success_rate"`
}

func (s *StatsService) GetClientStats2(days int) ([]ClientStats2, error) {
	startDate := time.Now().AddDate(0, 0, -days)

	type Result struct {
		ClientID      string
		TotalRequests int
		TotalTokens   int
		ErrorCount    int
	}

	var results []Result
	err := s.db.Model(&models.RequestLog{}).
		Select("client_id, COUNT(*) as total_requests, COALESCE(SUM(input_tokens + output_tokens), 0) as total_tokens, SUM(CASE WHEN status_code >= 400 THEN 1 ELSE 0 END) as error_count").
		Where("created_at >= ?", startDate).
		Group("client_id").
		Order("total_requests DESC").
		Scan(&results).Error

	if err != nil {
		return nil, err
	}

	var clientStats []ClientStats2
	for _, r := range results {
		client, _ := NewClientService(s.db).GetClientByID(r.ClientID)
		clientName := r.ClientID
		if client != nil {
			clientName = client.Name
		}

		successRate := 100.0
		if r.TotalRequests > 0 {
			successRate = float64(r.TotalRequests-r.ErrorCount) / float64(r.TotalRequests) * 100
		}

		clientStats = append(clientStats, ClientStats2{
			ClientID:      r.ClientID,
			ClientName:    clientName,
			TotalRequests: r.TotalRequests,
			TotalTokens:   r.TotalTokens,
			SuccessRate:   successRate,
		})
	}

	return clientStats, nil
}

type MinuteStats struct {
	Timestamp     string `json:"timestamp"`
	TotalRequests int    `json:"total_requests"`
	InputTokens   int    `json:"input_tokens"`
	OutputTokens  int    `json:"output_tokens"`
	UniqueClients int    `json:"unique_clients"`
}

func (s *StatsService) GetRecentStats(minutes int) ([]MinuteStats, error) {
	startTime := time.Now().Add(-time.Duration(minutes) * time.Minute).Truncate(time.Minute)

	var results []MinuteStats
	err := s.db.Model(&models.RequestLog{}).
		Select("strftime('%Y-%m-%dT%H:%M', created_at) as timestamp, COUNT(*) as total_requests, COALESCE(SUM(input_tokens), 0) as input_tokens, COALESCE(SUM(output_tokens), 0) as output_tokens, COUNT(DISTINCT client_id) as unique_clients").
		Where("created_at >= ?", startTime).
		Group("strftime('%Y-%m-%dT%H:%M', created_at)").
		Order("timestamp ASC").
		Scan(&results).Error

	if err != nil {
		return nil, err
	}

	return results, nil
}
