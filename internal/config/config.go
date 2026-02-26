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
	Server     ServerConfig              `yaml:"server"`
	Admin      AdminConfig               `yaml:"admin"`
	Providers  map[string]ProviderConfig `yaml:"providers"`
	Defaults   DefaultsConfig            `yaml:"defaults"`
	Database   DatabaseConfig            `yaml:"database"`
	Logging    LoggingConfig             `yaml:"logging"`
	Prometheus PrometheusConfig          `yaml:"prometheus"`

	// Deprecated: kept for backward compat with existing config files.
	// On load, this is migrated into Providers["gemini"].
	Gemini *LegacyGeminiConfig `yaml:"gemini,omitempty"`
}

// ProviderConfig is the unified configuration for any upstream AI backend.
type ProviderConfig struct {
	// Type identifies the backend: gemini, openai, anthropic, mistral, ollama, lmstudio
	Type           string   `yaml:"type" json:"type"`
	APIKey         string   `yaml:"api_key,omitempty" json:"api_key,omitempty"`
	BaseURL        string   `yaml:"base_url,omitempty" json:"base_url,omitempty"`
	DefaultModel   string   `yaml:"default_model,omitempty" json:"default_model,omitempty"`
	AllowedModels  []string `yaml:"allowed_models,omitempty" json:"allowed_models,omitempty"`
	TimeoutSeconds int      `yaml:"timeout_seconds,omitempty" json:"timeout_seconds,omitempty"`
}

// LegacyGeminiConfig supports the old config.yaml format with a top-level gemini: key.
type LegacyGeminiConfig struct {
	APIKey         string   `yaml:"api_key"`
	DefaultModel   string   `yaml:"default_model"`
	AllowedModels  []string `yaml:"allowed_models"`
	TimeoutSeconds int      `yaml:"timeout_seconds"`
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

type PrometheusConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

func (c *LoggingConfig) IsDebug() bool {
	return c.Level == "debug"
}

var configPath string

func Load(path string) (*Config, error) {
	configPath = path
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

	if cfg.Providers == nil {
		cfg.Providers = make(map[string]ProviderConfig)
	}

	// Migrate legacy gemini: section into providers map
	if cfg.Gemini != nil {
		if _, exists := cfg.Providers["gemini"]; !exists {
			timeout := cfg.Gemini.TimeoutSeconds
			if timeout == 0 {
				timeout = 120
			}
			cfg.Providers["gemini"] = ProviderConfig{
				Type:           "gemini",
				APIKey:         cfg.Gemini.APIKey,
				DefaultModel:   cfg.Gemini.DefaultModel,
				AllowedModels:  cfg.Gemini.AllowedModels,
				TimeoutSeconds: timeout,
			}
		}
		cfg.Gemini = nil
	}

	// Ensure timeout defaults for all providers
	for name, p := range cfg.Providers {
		if p.TimeoutSeconds == 0 {
			p.TimeoutSeconds = 120
			cfg.Providers[name] = p
		}
		if p.Type == "" {
			p.Type = name
			cfg.Providers[name] = p
		}
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

// GetProvider returns the provider config for a given name, or nil if not found.
func (c *Config) GetProvider(name string) *ProviderConfig {
	p, ok := c.Providers[name]
	if !ok {
		return nil
	}
	return &p
}

// ProviderNames returns a sorted list of configured provider names.
func (c *Config) ProviderNames() []string {
	names := make([]string, 0, len(c.Providers))
	for name := range c.Providers {
		names = append(names, name)
	}
	return names
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
		Providers: map[string]ProviderConfig{
			"gemini": {
				Type:           "gemini",
				DefaultModel:   "gemini-flash-lite-latest",
				AllowedModels:  []string{"gemini-2.0-flash", "gemini-2.0-flash-lite"},
				TimeoutSeconds: 120,
			},
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

	// If password hash is empty, mark for setup wizard
	if cfg.Admin.PasswordHash == "" {
		cfg.Admin.PasswordHash = "__SETUP_REQUIRED__"
		changed = true
	}

	if cfg.Admin.SessionSecret == "" {
		cfg.Admin.SessionSecret = generateRandomString(32)
		changed = true
	}

	if cfg.Prometheus.Enabled && cfg.Prometheus.Username == "" {
		cfg.Prometheus.Username = "prometheus"
		cfg.Prometheus.Password = generateRandomString(20)
		changed = true
		fmt.Printf("\n===========================================\n")
		fmt.Printf("  Prometheus credentials generated!\n")
		fmt.Printf("  Username: %s\n", cfg.Prometheus.Username)
		fmt.Printf("  Password: %s\n", cfg.Prometheus.Password)
		fmt.Printf("===========================================\n\n")
	}

	if cfg.Prometheus.Enabled && cfg.Prometheus.Username != "" && cfg.Prometheus.Password == "" {
		cfg.Prometheus.Password = generateRandomString(20)
		changed = true
		fmt.Printf("\n===========================================\n")
		fmt.Printf("  Prometheus password generated!\n")
		fmt.Printf("  Username: %s\n", cfg.Prometheus.Username)
		fmt.Printf("  Password: %s\n", cfg.Prometheus.Password)
		fmt.Printf("===========================================\n\n")
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

// SaveConfig exports saveConfig for external use
func SaveConfig(cfg *Config, path string) error {
	return saveConfig(cfg, path)
}

func generateRandomString(length int) string {
	b := make([]byte, length)
	rand.Read(b)
	return hex.EncodeToString(b)[:length]
}

func Save(cfg *Config) {
	if configPath == "" {
		return
	}
	saveConfig(cfg, configPath)
}
