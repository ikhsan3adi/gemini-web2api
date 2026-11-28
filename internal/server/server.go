package server

import (
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/ikhsan3adi/gemini-web2api/internal/config"
	"github.com/ikhsan3adi/gemini-web2api/internal/gemini"
	"github.com/ikhsan3adi/gemini-web2api/internal/multimodal"
)

type App struct {
	Cfg        config.Config
	Gem        *gemini.Client
	Tokens     *multimodal.TokenCache
	Sessions   *SessionManager
	HTTPClient *http.Client
	Logf       func(format string, args ...any)
	Version    string
}

func createHTTPClient(cfg config.Config) *http.Client {
	transport := &http.Transport{}
	if cfg.Proxy != "" {
		if proxyURL, err := url.Parse(cfg.Proxy); err == nil {
			transport.Proxy = http.ProxyURL(proxyURL)
		}
	} else {
		transport.Proxy = http.ProxyFromEnvironment
	}
	return &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
	}
}

func New(cfg config.Config, version string) *App {
	gemClient := gemini.NewClient(cfg)

	logFn := func(format string, args ...any) {
		if cfg.LogRequests {
			log.Printf(format, args...)
		}
	}

	httpClient := createHTTPClient(cfg)

	app := &App{
		Cfg:        cfg,
		Gem:        gemClient,
		Tokens:     multimodal.NewTokenCache(cfg, gemClient.Cookies, httpClient),
		Sessions:   NewSessionManager(cfg),
		HTTPClient: httpClient,
		Logf:       logFn,
		Version:    version,
	}

	return app
}

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", a.handleHealth)
	mux.HandleFunc("GET /v1/models", a.handleModels)
	mux.HandleFunc("POST /v1/chat/completions", a.handleChat)
	mux.HandleFunc("POST /v1/messages", a.handleMessages)
	mux.HandleFunc("POST /v1/messages/count_tokens", a.handleCountTokens)
	mux.HandleFunc("POST /v1/responses", a.handleResponses)

	mux.HandleFunc("GET /v1beta/models", a.handleGoogleModels)
	mux.HandleFunc("POST /v1beta/models/{target}", a.handleGoogleGenerate)

	handler := a.withAuthAndLogging(mux)
	handler = a.withCORS(handler)

	return handler
}
