package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"

	"golang.org/x/crypto/bcrypt"
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
	MaxInputTokens        int `yaml:"max_input_tokens"`
	MaxOutputTokens       int `yaml:"max_output_tokens"`
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
		if os.IsNotExist(err) {
			return createDefaultConfig(path)
		}
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8090
	}

	if cfg.Gemini.TimeoutSeconds == 0 {
		cfg.Gemini.TimeoutSeconds = 120
	}

	if cfg.Defaults.RateLimit.RequestsPerMinute == 0 {
		cfg.Defaults.RateLimit.RequestsPerMinute = 60
	}

	cfg, err = ensureDefaults(cfg, path)
	if err != nil {
		return nil, err
	}

	return &cfg, nil
}

func createDefaultConfig(path string) (*Config, error) {
	secret := generateRandomString(32)
	defaultPassword := generateRandomString(16)
	hash, err := bcrypt.GenerateFromPassword([]byte(defaultPassword), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("failed to hash password: %w", err)
	}

	cfg := &Config{
		Server: ServerConfig{
			Host: "0.0.0.0",
			Port: 8090,
			HTTPS: HTTPSConfig{
				Enabled: false,
			},
		},
		Admin: AdminConfig{
			Username:      "admin",
			PasswordHash:  string(hash),
			SessionSecret: secret,
		},
		Gemini: GeminiConfig{
			DefaultModel:   "gemini-flash-lite-latest",
			AllowedModels:  []string{"gemini-2.0-flash", "gemini-2.0-flash-lite", "gemini-2.0-pro", "gemini-pro", "gemini-pro-vision"},
			TimeoutSeconds: 120,
		},
		Defaults: DefaultsConfig{
			RateLimit: RateLimitDefaults{
				RequestsPerMinute: 60,
				RequestsPerHour:   1000,
				RequestsPerDay:    10000,
			},
			Quota: QuotaDefaults{
				MaxInputTokensPerDay:  1000000,
				MaxOutputTokensPerDay: 500000,
				MaxRequestsPerDay:     1000,
				MaxInputTokens:        1000000,
				MaxOutputTokens:       8192,
			},
		},
		Database: DatabaseConfig{
			Path: "./data/gateway.db",
		},
		Logging: LoggingConfig{
			Level: "info",
			File:  "./logs/gateway.log",
		},
	}

	if err := saveConfig(cfg, path); err != nil {
		return nil, err
	}

	fmt.Printf("\n===========================================\n")
	fmt.Printf("  Default credentials generated!\n")
	fmt.Printf("===========================================\n")
	fmt.Printf("  Username: admin\n")
	fmt.Printf("  Password: %s\n", defaultPassword)
	fmt.Printf("  (Save this - it will not be shown again)\n")
	fmt.Printf("===========================================\n\n")

	return cfg, nil
}

func ensureDefaults(cfg Config, path string) (Config, error) {
	changed := false

	if cfg.Admin.PasswordHash == "" {
		defaultPassword := generateRandomString(16)
		hash, err := bcrypt.GenerateFromPassword([]byte(defaultPassword), bcrypt.DefaultCost)
		if err != nil {
			return cfg, fmt.Errorf("failed to hash password: %w", err)
		}
		cfg.Admin.PasswordHash = string(hash)
		changed = true
		fmt.Printf("\n===========================================\n")
		fmt.Printf("  Default password generated!\n")
		fmt.Printf("  Username: admin\n")
		fmt.Printf("  Password: %s\n", defaultPassword)
		fmt.Printf("===========================================\n\n")
	}

	if cfg.Admin.SessionSecret == "" {
		cfg.Admin.SessionSecret = generateRandomString(32)
		changed = true
	}

	if cfg.Gemini.AllowedModels == nil || len(cfg.Gemini.AllowedModels) == 0 {
		cfg.Gemini.AllowedModels = []string{"gemini-2.0-flash", "gemini-2.0-flash-lite", "gemini-2.0-pro", "gemini-flash-lite-latest", "gemini-pro", "gemini-pro-vision"}
		changed = true
	}

	if changed {
		if err := saveConfig(&cfg, path); err != nil {
			return cfg, err
		}
	}

	return cfg, nil
}

func saveConfig(cfg *Config, path string) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	dir := ""
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			dir = path[:i]
			break
		}
	}

	if dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create config directory: %w", err)
		}
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	return nil
}

func generateRandomString(length int) string {
	b := make([]byte, length)
	rand.Read(b)
	return hex.EncodeToString(b)[:length]
}
