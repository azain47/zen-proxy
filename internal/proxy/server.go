package proxy

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
)

func Run(version string) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-h", "--help", "help":
			printUsage(version)
			return
		case "-v", "--version", "version":
			fmt.Printf("zen-proxy %s\n", version)
			return
		}
	}

	cfg := LoadConfig()
	if cfg.Provider == providerOpenRouter && cfg.APIKey == "" {
		log.Printf("warning: OpenRouter requires OPENROUTER_API_KEY or ZEN_API_KEY")
	}
	models := FetchModels(cfg)

	log.Printf("zen-proxy → %s (provider: %s, default model: %s)", cfg.Upstream, cfg.Provider, cfg.Model)

	if len(models) > 0 {
		log.Printf("fetched %d models from upstream", len(models))
		PrintProviderModels(cfg, models)
	} else {
		log.Printf("warning: could not fetch models from upstream")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/messages", handleAnthropic(cfg))
	mux.HandleFunc("POST /v1/messages/count_tokens", handleTokenCount(cfg))
	mux.HandleFunc("POST /v1/responses", handleResponses(cfg))
	mux.HandleFunc("POST /v1/chat/completions", handleCompletions(cfg))
	mux.HandleFunc("GET /v1/models", handleModels(models))
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"ok"}`))
	})

	addr := net.JoinHostPort(cfg.Host, cfg.Port)
	log.Printf("listening on %s", addr)
	if err := http.ListenAndServe(addr, withLogging(withCORS(mux))); err != nil {
		log.Fatal(err)
		os.Exit(1)
	}
}

func printUsage(version string) {
	fmt.Printf(`zen-proxy %s

OpenAI/Anthropic/Responses-compatible proxy for OpenCode Zen and OpenRouter.

Usage:
  zen-proxy
  zen-proxy --version

Environment:
  ZEN_HOST       listen host (default: 127.0.0.1)
  ZEN_PORT       listen port (default: 8788)
  ZEN_PROVIDER   provider preset: zen or openrouter (default: zen)
  ZEN_UPSTREAM   upstream chat completions URL
  ZEN_MODELS_URL upstream models URL
  ZEN_API_KEY    upstream API key (default for zen: public)
  ZEN_MODEL      fallback model when a request omits model

OpenRouter:
  ZEN_PROVIDER=openrouter OPENROUTER_API_KEY=sk-or-v1-... zen-proxy
`, version)
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, x-api-key, anthropic-version, HTTP-Referer, X-Title")
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}
