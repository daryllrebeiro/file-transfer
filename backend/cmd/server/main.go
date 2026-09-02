package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"file-transfer/backend/internal/config"
	"file-transfer/backend/internal/httpapi"
	"file-transfer/backend/internal/transfer"
	transferws "file-transfer/backend/internal/websocket"
)

func main() {
	settings := config.Load()
	if err := validateSettings(settings); err != nil {
		log.Fatal(err)
	}
	manager := transfer.NewManager(settings.TransferTTL, settings.MaxFileSize, settings.MaxChunkSize)
	manager.SetLimits(settings.MaxActiveTransfers, settings.MaxConnections)
	api := &httpapi.Server{Manager: manager, BaseURL: settings.PublicBaseURL, CreateLimiter: httpapi.NewCreationLimiter(settings.CreateRatePerMinute), MetricsToken: settings.MetricsToken, TrustProxy: settings.TrustProxy}
	mux := http.NewServeMux()
	mux.Handle("/api/", httpapi.SecurityHeaders(httpapi.CORS(settings.AllowedOrigins, api.Routes())))
	mux.Handle("/healthz", httpapi.SecurityHeaders(api.Routes()))
	mux.Handle("/metrics", httpapi.SecurityHeaders(api.Routes()))
	allowed := map[string]bool{}
	for _, origin := range settings.AllowedOrigins {
		allowed[origin] = true
	}
	mux.Handle("/ws/", httpapi.SecurityHeaders(transferws.NewHandler(manager, allowed)))
	server := &http.Server{Addr: "0.0.0.0:" + settings.Port, Handler: mux, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: settings.WriteTimeout, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 32 << 10}
	log.Printf("file transfer server listening on %s", server.Addr)
	stop, stopSignal := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignal()
	go cleanup(stop, manager)
	go func() {
		<-stop.Done()
		manager.Shutdown()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownContext)
	}()
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
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
