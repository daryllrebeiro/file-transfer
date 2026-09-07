package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"file-transfer/backend/internal/config"
	"file-transfer/backend/internal/httpapi"
	"file-transfer/backend/internal/signaling"
	"file-transfer/backend/internal/tracing"
	"file-transfer/backend/internal/transfer"
	transferws "file-transfer/backend/internal/websocket"
)

func main() {
	settings := config.Load()
	slog.SetDefault(newLogger(settings.LogFormat, settings.LogLevel))
	if err := validateSettings(settings); err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	shutdownTracing := tracing.Init("file-transfer-server")
	defer shutdownTracing()

	manager := transfer.NewManager(settings.TransferTTL, settings.MaxFileSize, settings.MaxChunkSize)
	manager.SetLimits(settings.MaxActiveTransfers, settings.MaxConnections)
	api := &httpapi.Server{Manager: manager, BaseURL: settings.PublicBaseURL, CreateLimiter: httpapi.NewCreationLimiter(settings.CreateRatePerMinute), MetricsToken: settings.MetricsToken, MetricsFormat: settings.MetricsFormat, TrustProxy: settings.TrustProxy, Settings: settings}

	var signalPublisher transferws.SignalPublisher
	if settings.RedisEnabled {
		redisSignaling, err := signaling.NewRedisSignaling(settings.RedisURL, slog.Default())
		if err != nil {
			slog.Error("Failed to initialize Redis signaling", "error", err)
			os.Exit(1)
		}
		defer redisSignaling.Close()
		signalPublisher = redisSignaling
		slog.Info("Redis signaling enabled", "url", settings.RedisURL)
	}

	mux := http.NewServeMux()
	mux.Handle("/api/", httpapi.SecurityHeaders(httpapi.CORS(settings.AllowedOrigins, api.Routes())))
	mux.Handle("/healthz", httpapi.SecurityHeaders(api.Routes()))
	mux.Handle("/metrics", httpapi.SecurityHeaders(api.Routes()))
	allowed := map[string]bool{}
	for _, origin := range settings.AllowedOrigins {
		allowed[origin] = true
	}
	mux.Handle("/ws/", httpapi.SecurityHeaders(transferws.NewHandler(manager, allowed, signalPublisher)))
	server := &http.Server{Addr: "0.0.0.0:" + settings.Port, Handler: mux, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: settings.WriteTimeout, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 32 << 10}
	slog.Info("server listening", "addr", server.Addr)
	stop, stopSignal := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignal()
	go cleanup(stop, manager)
	go func() {
		<-stop.Done()
		slog.Info("shutting down: entering draining mode")
		manager.SetDraining(true)

		slog.Info("waiting for active transfers to complete")
		drainCtx, drainCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer drainCancel()
		if err := manager.WaitForDrain(drainCtx); err != nil {
			slog.Warn("drain timeout or cancelled", "error", err)
		}

		slog.Info("draining connections")
		manager.Shutdown()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownContext)
		slog.Info("shutdown complete")
	}()
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server failed", "error", err)
		os.Exit(1)
	}
}

func newLogger(format, level string) *slog.Logger {
	options := &slog.HandlerOptions{Level: slog.LevelInfo}
	switch strings.ToLower(level) {
	case "debug":
		options.Level = slog.LevelDebug
	case "warn":
		options.Level = slog.LevelWarn
	case "error":
		options.Level = slog.LevelError
	}
	var handler slog.Handler
	if strings.ToLower(format) == "json" {
		handler = slog.NewJSONHandler(os.Stderr, options)
	} else {
		handler = slog.NewTextHandler(os.Stderr, options)
	}
	return slog.New(handler)
}
func validateSettings(settings config.Config) error {
	parsed, err := url.Parse(settings.PublicBaseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return errors.New("PUBLIC_BASE_URL must be an absolute URL")
	}
	if parsed.Scheme == "https" {
		for _, origin := range settings.AllowedOrigins {
			if parsedOrigin, parseErr := url.Parse(origin); parseErr != nil || parsedOrigin.Scheme != "https" {
				return errors.New("HTTPS PUBLIC_BASE_URL requires HTTPS ALLOWED_ORIGINS")
			}
		}
	}
	for _, origin := range settings.AllowedOrigins {
		if origin == "*" {
			return errors.New("wildcard ALLOWED_ORIGINS is not permitted")
		}
	}
	return nil
}
func cleanup(ctx context.Context, manager *transfer.Manager) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case now := <-ticker.C:
			manager.Cleanup(now)
		case <-ctx.Done():
			return
		}
	}
}
