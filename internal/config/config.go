package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type Config struct {
	Port                   int      `json:"port"`
	Host                   string   `json:"host"`
	RetryAttempts          int      `json:"retry_attempts"`
	RetryDelaySec          int      `json:"retry_delay_sec"`
	RequestTimeoutSec      int      `json:"request_timeout_sec"`
	GeminiBL               string   `json:"gemini_bl"`
	AuthUser               string   `json:"auth_user"`
	XSRFToken              string   `json:"xsrf_token"`
	DefaultModel           string   `json:"default_model"`
	LogRequests            bool     `json:"log_requests"`
	CookieFile             string   `json:"cookie_file"`
	Proxy                  string   `json:"proxy"`
	APIKeys                []string `json:"api_keys"`
	Impersonate            string   `json:"impersonate"`
	TemporaryChats         bool     `json:"temporary_chats"`
	SessionEnabled         bool     `json:"session_enabled"`
	MaxContextTokens       int      `json:"max_context_tokens"`
	SessionRolloverRatio   float64  `json:"session_rollover_ratio"`
	CompressThresholdRatio float64  `json:"compress_threshold_ratio"`
	KeepRecentMessages     int      `json:"keep_recent_messages"`
	SessionTTLSec          int      `json:"session_ttl_sec"`
	MaxSessions            int      `json:"max_sessions"`
}

func Default() Config {
	return Config{
		Port:                   8081,
		Host:                   "0.0.0.0",
		RetryAttempts:          3,
		RetryDelaySec:          2,
		RequestTimeoutSec:      180,
		GeminiBL:               "boq_assistant-bard-web-server_20260716.08_p0",
		AuthUser:               "",
		XSRFToken:              "",
		DefaultModel:           "gemini-3.6-flash",
		LogRequests:            true,
		CookieFile:             "",
		Proxy:                  "",
		APIKeys:                []string{},
		Impersonate:            "",
		TemporaryChats:         false,
		SessionEnabled:         true,
		MaxContextTokens:       1000000,
		SessionRolloverRatio:   0.85,
		CompressThresholdRatio: 0.60,
		KeepRecentMessages:     6,
		SessionTTLSec:          3600,
		MaxSessions:            1024,
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}

	// Use temporary struct with pointer fields or interface to handle null values from JSON if needed,
	// but standard unmarshal over struct fields handles JSON null for strings by leaving existing or setting zero-value if unmarshaling map/struct.
	// Let's ensure string fields default properly if JSON has explicitly `null`.
	var aux struct {
		Port                   *int      `json:"port"`
		Host                   *string   `json:"host"`
		RetryAttempts          *int      `json:"retry_attempts"`
		RetryDelaySec          *int      `json:"retry_delay_sec"`
		RequestTimeoutSec      *int      `json:"request_timeout_sec"`
		GeminiBL               *string   `json:"gemini_bl"`
		AuthUser               *string   `json:"auth_user"`
		XSRFToken              *string   `json:"xsrf_token"`
		DefaultModel           *string   `json:"default_model"`
		LogRequests            *bool     `json:"log_requests"`
		CookieFile             *string   `json:"cookie_file"`
		Proxy                  *string   `json:"proxy"`
		APIKeys                *[]string `json:"api_keys"`
		Impersonate            *string   `json:"impersonate"`
		TemporaryChats         *bool     `json:"temporary_chats"`
		SessionEnabled         *bool     `json:"session_enabled"`
		MaxContextTokens       *int      `json:"max_context_tokens"`
		SessionRolloverRatio   *float64  `json:"session_rollover_ratio"`
		CompressThresholdRatio *float64  `json:"compress_threshold_ratio"`
		KeepRecentMessages     *int      `json:"keep_recent_messages"`
		SessionTTLSec          *int      `json:"session_ttl_sec"`
		MaxSessions            *int      `json:"max_sessions"`
	}

	if err := json.Unmarshal(data, &aux); err != nil {
		return cfg, err
	}

	if aux.Port != nil {
		cfg.Port = *aux.Port
	}
	if aux.Host != nil {
		cfg.Host = *aux.Host
	}
	if aux.RetryAttempts != nil {
		cfg.RetryAttempts = *aux.RetryAttempts
	}
	if aux.RetryDelaySec != nil {
		cfg.RetryDelaySec = *aux.RetryDelaySec
	}
	if aux.RequestTimeoutSec != nil {
		cfg.RequestTimeoutSec = *aux.RequestTimeoutSec
	}
	if aux.GeminiBL != nil {
		cfg.GeminiBL = *aux.GeminiBL
	}
	if aux.AuthUser != nil {
		cfg.AuthUser = *aux.AuthUser
	}
	if aux.XSRFToken != nil {
		cfg.XSRFToken = *aux.XSRFToken
	}
	if aux.DefaultModel != nil {
		cfg.DefaultModel = *aux.DefaultModel
	}
	if aux.LogRequests != nil {
		cfg.LogRequests = *aux.LogRequests
	}
	if aux.CookieFile != nil {
		cfg.CookieFile = *aux.CookieFile
	}
	if aux.Proxy != nil {
		cfg.Proxy = *aux.Proxy
	}
	if aux.APIKeys != nil {
		cfg.APIKeys = *aux.APIKeys
	}
	if aux.Impersonate != nil {
		cfg.Impersonate = *aux.Impersonate
	}
	if aux.TemporaryChats != nil {
		cfg.TemporaryChats = *aux.TemporaryChats
	}
	if aux.SessionEnabled != nil {
		cfg.SessionEnabled = *aux.SessionEnabled
	}
	if aux.MaxContextTokens != nil && *aux.MaxContextTokens > 0 {
		cfg.MaxContextTokens = *aux.MaxContextTokens
	}
	if aux.SessionRolloverRatio != nil && *aux.SessionRolloverRatio > 0 && *aux.SessionRolloverRatio <= 1 {
		cfg.SessionRolloverRatio = *aux.SessionRolloverRatio
	}
	if aux.CompressThresholdRatio != nil && *aux.CompressThresholdRatio > 0 && *aux.CompressThresholdRatio <= 1 {
		cfg.CompressThresholdRatio = *aux.CompressThresholdRatio
	}
	if cfg.CompressThresholdRatio >= cfg.SessionRolloverRatio {
		cfg.CompressThresholdRatio = cfg.SessionRolloverRatio * 0.75
	}
	if aux.KeepRecentMessages != nil && *aux.KeepRecentMessages > 0 {
		cfg.KeepRecentMessages = *aux.KeepRecentMessages
	}
	if aux.SessionTTLSec != nil && *aux.SessionTTLSec > 0 {
		cfg.SessionTTLSec = *aux.SessionTTLSec
	}
	if aux.MaxSessions != nil && *aux.MaxSessions > 0 {
		cfg.MaxSessions = *aux.MaxSessions
	}

	return cfg, nil
}

func findFirstExisting(filename string) string {
	paths := []string{filepath.Join(".", filename)}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".config", "gemini-web2api", filename))
	}

	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func Find() string {
	return findFirstExisting("config.json")
}

// FindCookie returns the default cookie.json path if present in standard locations.
func FindCookie() string {
	return findFirstExisting("cookie.json")
}
