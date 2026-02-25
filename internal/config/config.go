package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Admin    AdminConfig    `yaml:"admin"`
	Gemini   GeminiConfig   `yaml:"gemini"`
	Defaults DefaultsConfig `yaml:"defaults"`
	Database DatabaseConfig `yaml:"database"`
	Logging  LoggingConfig  `yaml:"logging"`
}

type ServerConfig struct {
	Host  string      `yaml:"host"`
	Port  int         `yaml:"port"`
	HTTPS HTTPSConfig `yaml:"https"`
}

type HTTPSConfig struct {
	Enabled  bool   `yaml:"enabled"`
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
}

type AdminConfig struct {
	Username      string `yaml:"username"`
	PasswordHash  string `yaml:"password_hash"`
	SessionSecret string `yaml:"session_secret"`
}

type GeminiConfig struct {
	APIKey         string   `yaml:"api_key"`
	DefaultModel   string   `yaml:"default_model"`
	AllowedModels  []string `yaml:"allowed_models"`
	TimeoutSeconds int      `yaml:"timeout_seconds"`
}

type DefaultsConfig struct {
	RateLimit RateLimitDefaults `yaml:"rate_limit"`
	Quota     QuotaDefaults     `yaml:"quota"`
}

type RateLimitDefaults struct {
	RequestsPerMinute int `yaml:"requests_per_minute"`
	RequestsPerHour   int `yaml:"requests_per_hour"`
	RequestsPerDay    int `yaml:"requests_per_day"`
}

type QuotaDefaults struct {
	MaxInputTokensPerDay  int `yaml:"max_input_tokens_per_day"`
	MaxOutputTokensPerDay int `yaml:"max_output_tokens_per_day"`
	MaxRequestsPerDay     int `yaml:"max_requests_per_day"`
}

type DatabaseConfig struct {
	Path string `yaml:"path"`
}

type LoggingConfig struct {
	Level string `yaml:"level"`
	File  string `yaml:"file"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8080
	}

	if cfg.Gemini.TimeoutSeconds == 0 {
		cfg.Gemini.TimeoutSeconds = 120
	}

	if cfg.Defaults.RateLimit.RequestsPerMinute == 0 {
		cfg.Defaults.RateLimit.RequestsPerMinute = 60
	}

	return &cfg, nil
}
