package proxy

import (
	"context"
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
	NoColor bool
}

type startupInfo struct {
	Version         string
	Provider        string
	Upstream        string
	Model           string
	Address         string
	Models          modelStats
	MetadataEnabled bool
	MetadataMatched int
	MetadataReady   bool
}

func Run(version string) int {
	options, command, err := parseRunOptions(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "zen-proxy: %v\n\n", err)
		printUsage(version)
		return 2
	}
	switch command {
	case "help":
		printUsage(version)
		return 0
	case "version":
		fmt.Printf("zen-proxy %s\n", version)
		return 0
	case "check-update":
		return printUpdateStatus(version)
	}
	if options.TUI && (!isTerminalFile(os.Stdin) || !isTerminalFile(os.Stdout)) {
		fmt.Fprintln(os.Stderr, "zen-proxy: --tui requires an interactive terminal")
		return 2
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
	cfg.Model = resolveFallbackModel(cfg, models)

	if command == "models" {
		if len(models) == 0 {
			fmt.Fprintln(os.Stderr, "zen-proxy: no models available from upstream")
			return 1
		}
		writeModelList(os.Stdout, cfg.Provider, models)
		return 0
	}
	if len(models) == 0 {
		log.Printf("warning: could not fetch models from upstream")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/messages", handleAnthropic(cfg))
	mux.HandleFunc("POST /v1/messages/count_tokens", handleTokenCount(cfg))
	mux.HandleFunc("POST /v1/responses", handleResponses(cfg))
	mux.HandleFunc("POST /v1/chat/completions", handleCompletions(cfg))
	mux.HandleFunc("GET /v1/models", handleModels(models, cfg.CodexInstructions))
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("{\"status\":\"ok\"}"))
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
		fmt.Fprintf(os.Stderr, "zen-proxy: listen %s: %v\n", addr, err)
		return 1
	}

	if !options.TUI {
		writeStartupSummary(os.Stderr, startupInfo{
			Version:         version,
			Provider:        cfg.Provider,
			Upstream:        cfg.Upstream,
			Model:           cfg.Model,
			Address:         "http://" + listener.Addr().String(),
			Models:          summarizeModels(cfg.Provider, models),
			MetadataEnabled: metadataConfigured(cfg.ModelMetadataURL),
			MetadataMatched: matchedMetadata,
			MetadataReady:   len(metadata) > 0,
		})
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("server error: %v", err)
		}
		return 0
	}

	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.Serve(listener) }()
	if err := RunDashboard(dashboardConfig{
		Address:    listener.Addr().String(),
		Provider:   cfg.Provider,
		Model:      cfg.Model,
		ModelCount: len(models),
		NoColor:    options.NoColor,
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
	return 0
}

func writeStartupSummary(w io.Writer, info startupInfo) {
	fmt.Fprintf(w, "zen-proxy %s\n", info.Version)
	fmt.Fprintf(w, "  ready    %s\n", info.Address)
	fmt.Fprintf(w, "  route    %s -> %s\n", info.Provider, info.Upstream)
	fmt.Fprintf(w, "  model    %s\n", info.Model)
	if info.Models.Total == 0 {
		fmt.Fprintln(w, "  catalog  unavailable")
	} else {
		freeLabel := "free"
		if info.Provider == providerOpenRouter {
			freeLabel = "free text"
		}
		fmt.Fprintf(w, "  catalog  %d models (%d %s, %d other)\n", info.Models.Total, info.Models.Free, freeLabel, info.Models.Other)
	}
	if info.MetadataEnabled {
		if info.MetadataReady {
			fmt.Fprintf(w, "  metadata %d/%d matched\n", info.MetadataMatched, info.Models.Total)
		} else {
			fmt.Fprintln(w, "  metadata unavailable")
		}
	}
	fmt.Fprintln(w, "  inspect  zen-proxy --tui")
}

func metadataConfigured(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && !strings.EqualFold(value, "off")
}

func isTerminalFile(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func parseRunOptions(args []string) (runOptions, string, error) {
	if len(args) == 1 {
		switch args[0] {
		case "help":
			return runOptions{}, "help", nil
		case "version":
			return runOptions{}, "version", nil
		case "check-update":
			return runOptions{}, "check-update", nil
		case "models":
			return runOptions{}, "models", nil
		}
	}

	_, noColorEnv := os.LookupEnv("NO_COLOR")
	options := runOptions{
		Verbose: envBool("ZEN_VERBOSE") || envBool("ZEN_DEBUG"),
		TUI:     envBool("ZEN_TUI"),
		NoColor: noColorEnv || strings.EqualFold(strings.TrimSpace(os.Getenv("TERM")), "dumb"),
	}
	flags := flag.NewFlagSet("zen-proxy", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.BoolVar(&options.Verbose, "verbose", options.Verbose, "log sanitized request traces")
	flags.BoolVar(&options.Verbose, "debug", options.Verbose, "alias for --verbose")
	flags.BoolVar(&options.TUI, "tui", options.TUI, "start the live request dashboard")
	flags.BoolVar(&options.NoColor, "no-color", options.NoColor, "disable dashboard colors")
	help := flags.Bool("help", false, "show usage")
	flags.BoolVar(help, "h", false, "show usage")
	showVersion := flags.Bool("version", false, "show version")
	flags.BoolVar(showVersion, "v", false, "show version")
	checkUpdate := flags.Bool("check-update", false, "check whether a newer release is available")
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
	if *checkUpdate {
		return options, "check-update", nil
	}
	return options, "", nil
}
func printUpdateStatus(version string) int {
	result, err := checkForUpdate(context.Background(), version)
	if err != nil {
		fmt.Fprintf(os.Stderr, "zen-proxy: update check failed: %v\n", err)
		return 2
	}
	switch result.Comparison {
	case comparisonDevelopment:
		fmt.Printf("zen-proxy %s is a development build; latest release is %s\n", version, result.Latest)
		return 1
	case comparisonCurrent:
		fmt.Printf("zen-proxy %s is up to date (latest %s)\n", version, result.Latest)
		return 0
	case comparisonAhead:
		fmt.Printf("zen-proxy %s is newer than the latest release %s\n", version, result.Latest)
		return 0
	case comparisonBehind:
		fmt.Printf("zen-proxy %s is out of date; update available: %s\n", version, result.Latest)
		return 1
	default:
		fmt.Printf("zen-proxy %s does not match the latest release %s\n", version, result.Latest)
		return 2
	}
}

func printUsage(version string) {
	fmt.Printf(`zen-proxy %s

OpenAI/Anthropic/Responses-compatible proxy for OpenCode Zen and OpenRouter.

Usage:
  zen-proxy                  start the proxy
  zen-proxy models           list current upstream models
  zen-proxy --tui            start the live request inspector
  zen-proxy --verbose        log sanitized request traces
  zen-proxy --check-update   compare with the latest release
  zen-proxy --version        show version

Options:
  --no-color     disable dashboard colors
  --debug        alias for --verbose
  -h, --help     show this help
  -v, --version  show version

Environment:
  ZEN_HOST       listen host (default: 127.0.0.1)
  ZEN_PORT       listen port (default: 8788)
  ZEN_PROVIDER   provider preset: zen or openrouter (default: zen)
  ZEN_UPSTREAM   upstream chat completions URL
  ZEN_MODELS_URL upstream models URL
  ZEN_MODEL_METADATA_URL optional LiteLLM-compatible metadata catalog URL (use off to disable)
  ZEN_CORS_ORIGINS comma-separated browser origins allowed to call the proxy
  ZEN_VERBOSE    log redacted request and upstream payloads
  ZEN_TUI        start the live request inspector
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
