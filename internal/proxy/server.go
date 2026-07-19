package proxy

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"
)

type runOptions struct {
	Verbose bool
	TUI     bool
}

func Run(version string) {
	options, command, err := parseRunOptions(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "zen-proxy: %v\n\n", err)
		printUsage(version)
		return
	}
	switch command {
	case "help":
		printUsage(version)
		return
	case "version":
		fmt.Printf("zen-proxy %s\n", version)
		return
	}

	cfg := LoadConfig()
	if options.Verbose || options.TUI {
		cfg.Debugger = NewDebugger(options.Verbose && !options.TUI)
	}
	if cfg.Provider == providerOpenRouter && cfg.APIKey == "" {
		log.Printf("warning: OpenRouter requires OPENROUTER_API_KEY or ZEN_API_KEY")
	}
	modelsCh := make(chan []ModelInfo, 1)
	metadataCh := make(chan map[string]liteLLMModelMetadata, 1)
	go func() { modelsCh <- FetchModels(cfg) }()
	go func() { metadataCh <- FetchModelMetadata(cfg) }()
	models := <-modelsCh
	metadata := <-metadataCh
	matchedMetadata := EnrichModels(models, metadata, cfg.Provider)

	log.Printf("zen-proxy → %s (provider: %s, default model: %s)", cfg.Upstream, cfg.Provider, cfg.Model)

	if len(models) > 0 && !options.TUI {
		log.Printf("fetched %d models from upstream", len(models))
		if len(metadata) > 0 {
			log.Printf("%s", modelMetadataSummary(matchedMetadata, len(models)))
		}
		PrintProviderModels(cfg, models)
	} else if len(models) == 0 {
		log.Printf("warning: could not fetch models from upstream")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/messages", handleAnthropic(cfg))
	mux.HandleFunc("POST /v1/messages/count_tokens", handleTokenCount(cfg))
	mux.HandleFunc("POST /v1/responses", handleResponses(cfg))
	mux.HandleFunc("POST /v1/chat/completions", handleCompletions(cfg))
	mux.HandleFunc("GET /v1/models", handleModels(models, cfg.CodexInstructions))
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"ok"}`))
	})

	addr := net.JoinHostPort(cfg.Host, cfg.Port)
	server := &http.Server{
		Addr:              addr,
		Handler:           withDebug(withLogging(withCORS(mux, cfg.CORSOrigins), !options.TUI), cfg.Debugger),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("server error: %v", err)
		return
	}
	log.Printf("listening on %s", listener.Addr())
	if !options.TUI {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("server error: %v", err)
		}
		return
	}

	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- server.Serve(listener)
	}()

	if err := RunDashboard(dashboardConfig{
		Address:    listener.Addr().String(),
		Provider:   cfg.Provider,
		ModelCount: len(models),
	}, cfg.Debugger); err != nil {
		log.Printf("dashboard error: %v", err)
	}

	shutdownDeadline := time.Now().Add(5 * time.Second)
	if err := server.Close(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Printf("server shutdown error: %v", err)
	}
	select {
	case err := <-serverErrors:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("server error: %v", err)
		}
	case <-time.After(time.Until(shutdownDeadline)):
		log.Printf("server did not stop before shutdown deadline")
	}
}

func parseRunOptions(args []string) (runOptions, string, error) {
	if len(args) == 1 {
		switch args[0] {
		case "help":
			return runOptions{}, "help", nil
		case "version":
			return runOptions{}, "version", nil
		}
	}

	options := runOptions{Verbose: envBool("ZEN_VERBOSE") || envBool("ZEN_DEBUG"), TUI: envBool("ZEN_TUI")}
	flags := flag.NewFlagSet("zen-proxy", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.BoolVar(&options.Verbose, "verbose", options.Verbose, "log sanitized request traces")
	flags.BoolVar(&options.Verbose, "debug", options.Verbose, "alias for --verbose")
	flags.BoolVar(&options.TUI, "tui", options.TUI, "start the live request dashboard")
	help := flags.Bool("help", false, "show usage")
	flags.BoolVar(help, "h", false, "show usage")
	showVersion := flags.Bool("version", false, "show version")
	flags.BoolVar(showVersion, "v", false, "show version")
	if err := flags.Parse(args); err != nil {
		return runOptions{}, "", err
	}
	if flags.NArg() > 0 {
		return runOptions{}, "", fmt.Errorf("unexpected argument %q", flags.Arg(0))
	}
	if *help {
		return options, "help", nil
	}
	if *showVersion {
		return options, "version", nil
	}
	return options, "", nil
}

func printUsage(version string) {
	fmt.Printf(`zen-proxy %s

OpenAI/Anthropic/Responses-compatible proxy for OpenCode Zen and OpenRouter.

Usage:
  zen-proxy
  zen-proxy --debug
  zen-proxy --verbose
  zen-proxy --tui
  zen-proxy --version

Environment:
  ZEN_HOST       listen host (default: 127.0.0.1)
  ZEN_PORT       listen port (default: 8788)
  ZEN_PROVIDER   provider preset: zen or openrouter (default: zen)
  ZEN_UPSTREAM   upstream chat completions URL
  ZEN_MODELS_URL upstream models URL
  ZEN_MODEL_METADATA_URL optional LiteLLM-compatible metadata catalog URL (use off to disable)
  ZEN_CORS_ORIGINS comma-separated browser origins allowed to call the proxy
  ZEN_DEBUG      alias for ZEN_VERBOSE
  ZEN_VERBOSE    log redacted request and upstream payloads (default: false)
  ZEN_TUI        start the live request dashboard (default: false)
  ZEN_API_KEY    upstream API key (default for zen: public)
  ZEN_MODEL      fallback model when a request omits model

OpenRouter:
  ZEN_PROVIDER=openrouter OPENROUTER_API_KEY=sk-or-v1-... zen-proxy
`, version)
}

func withCORS(next http.Handler, allowedOrigins []string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			if !slices.Contains(allowedOrigins, origin) {
				http.Error(w, "browser origin is not allowed", http.StatusForbidden)
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, x-api-key, anthropic-version, HTTP-Referer, X-Title")
		}
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func withLogging(next http.Handler, enabled bool) http.Handler {
	if !enabled {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

func envBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
