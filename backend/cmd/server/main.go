package main

import (
	"log"
	"net/http"
	"time"

	"file-transfer/backend/internal/config"
	"file-transfer/backend/internal/httpapi"
	"file-transfer/backend/internal/transfer"
	transferws "file-transfer/backend/internal/websocket"
)

func main() { settings := config.Load(); manager := transfer.NewManager(settings.TransferTTL, settings.MaxFileSize, settings.MaxChunkSize); api := &httpapi.Server{Manager:manager, BaseURL:settings.PublicBaseURL}; mux := http.NewServeMux(); mux.Handle("/api/", httpapi.CORS(settings.AllowedOrigins, api.Routes())); mux.Handle("/healthz", api.Routes()); allowed := map[string]bool{}; for _, origin := range settings.AllowedOrigins { allowed[origin] = true }; mux.Handle("/ws/", &transferws.Handler{Manager:manager, AllowedOrigins:allowed}); go cleanup(manager); server := &http.Server{Addr:"0.0.0.0:"+settings.Port, Handler:mux, ReadHeaderTimeout:10*time.Second}; log.Printf("file transfer server listening on %s", server.Addr); log.Fatal(server.ListenAndServe()) }
func cleanup(manager *transfer.Manager) { ticker := time.NewTicker(time.Minute); defer ticker.Stop(); for now := range ticker.C { manager.Cleanup(now) } }