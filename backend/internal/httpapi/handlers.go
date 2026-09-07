package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"file-transfer/backend/internal/config"
	"file-transfer/backend/internal/transfer"
	"file-transfer/backend/internal/turn"
)

type Server struct {
	Manager       *transfer.Manager
	BaseURL       string
	CreateLimiter *CreationLimiter
	MetricsToken  string
	MetricsFormat string
	TrustProxy    bool
	TurnHandler   *turn.TurnHandler
	Settings      config.Config
}
type CreationLimiter struct {
	mu      sync.Mutex
	limit   int
	windows map[string]rateWindow
}
type rateWindow struct {
	started time.Time
	count   int
}

type RateLimitResult struct {
	Allowed    bool
	Limit      int
	Remaining  int
	RetryAfter time.Duration
}

func NewCreationLimiter(limit int) *CreationLimiter {
	return &CreationLimiter{limit: limit, windows: make(map[string]rateWindow)}
}
func (limiter *CreationLimiter) Allow(key string, now time.Time) bool {
	return limiter.Check(key, now).Allowed
}

func (limiter *CreationLimiter) Check(key string, now time.Time) RateLimitResult {
	if limiter == nil || limiter.limit <= 0 {
		return RateLimitResult{Allowed: true}
	}
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	window := limiter.windows[key]
	if now.Sub(window.started) >= time.Minute {
		window = rateWindow{started: now}
	}
	if window.count >= limiter.limit {
		limiter.windows[key] = window
		retryAfter := time.Minute - now.Sub(window.started)
		if retryAfter < 0 {
			retryAfter = 0
		}
		return RateLimitResult{Limit: limiter.limit, Remaining: 0, RetryAfter: retryAfter}
	}
	window.count++
	limiter.windows[key] = window
	return RateLimitResult{Allowed: true, Limit: limiter.limit, Remaining: limiter.limit - window.count}
}
func (server *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", server.healthCheck)
	mux.HandleFunc("/metrics", server.metrics)
	mux.HandleFunc("/api/transfers", server.create)
	mux.HandleFunc("/api/limits", server.limits)
	mux.HandleFunc("/api/transfers/", server.transferRoutes)
	if server.TurnHandler != nil {
		mux.HandleFunc("/api/turn-credentials", server.turnCredentials)
	}
	return mux
}
func (server *Server) metrics(w http.ResponseWriter, request *http.Request) {
	if server.MetricsToken == "" {
		http.Error(w, "metrics unavailable", http.StatusNotFound)
		return
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")), []byte(server.MetricsToken)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	metrics := server.Manager.Metrics()
	if server.MetricsFormat == "prometheus" {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		fmt.Fprintf(w, "# HELP relay_active_transfers Current in-memory transfer sessions.\n# TYPE relay_active_transfers gauge\nrelay_active_transfers %d\n", metrics.ActiveTransfers)
		fmt.Fprintf(w, "# HELP relay_active_connections Current WebSocket connections.\n# TYPE relay_active_connections gauge\nrelay_active_connections %d\n", metrics.ActiveConnections)
		fmt.Fprintf(w, "# HELP relay_transfers_created_total Transfers created.\n# TYPE relay_transfers_created_total counter\nrelay_transfers_created_total %d\n", metrics.CreatedTransfers)
		fmt.Fprintf(w, "# TYPE relay_transfers_completed_total counter\nrelay_transfers_completed_total %d\n", metrics.CompletedTransfers)
		fmt.Fprintf(w, "# TYPE relay_transfers_cancelled_total counter\nrelay_transfers_cancelled_total %d\n", metrics.CancelledTransfers)
		fmt.Fprintf(w, "# TYPE relay_transfers_expired_total counter\nrelay_transfers_expired_total %d\n", metrics.ExpiredTransfers)
		fmt.Fprintf(w, "# TYPE relay_transfers_failed_total counter\nrelay_transfers_failed_total %d\n", metrics.FailedTransfers)
		fmt.Fprintf(w, "# TYPE relay_bytes_relayed_total counter\nrelay_bytes_relayed_total %d\n", metrics.BytesRelayed)
		fmt.Fprintf(w, "# TYPE relay_queue_saturated_total counter\nrelay_queue_saturated_total %d\n", metrics.QueueSaturated)
		return
	}
	writeJSON(w, http.StatusOK, metrics)
}

func (server *Server) healthCheck(w http.ResponseWriter, request *http.Request) {
	type healthCheckItem struct {
		Name    string
		Healthy bool
		Details string
	}
	checks := []healthCheckItem{
		{"Configuration", true, "loaded"},
	}

	// Check Redis if enabled
	if server.Settings.RedisEnabled {
		checks = append(checks, healthCheckItem{"Redis", true, "enabled via configuration"})
	}

	// Check manager stats
	metrics := server.Manager.Metrics()
	checks = append(checks,
		healthCheckItem{"active_transfers", metrics.ActiveTransfers < server.Settings.MaxActiveTransfers, fmt.Sprintf("count=%d limit=%d", metrics.ActiveTransfers, server.Settings.MaxActiveTransfers)},
		healthCheckItem{"active_connections", metrics.ActiveConnections < server.Settings.MaxConnections, fmt.Sprintf("count=%d limit=%d", metrics.ActiveConnections, server.Settings.MaxConnections)},
	)

	allHealthy := true
	for _, c := range checks {
		if !c.Healthy {
			allHealthy = false
		}
	}

	status := http.StatusOK
	if !allHealthy {
		status = http.StatusServiceUnavailable
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"healthy": allHealthy,
		"checks":  checks,
	})
}
func (server *Server) create(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if request.Header.Get("X-Requested-With") != "XMLHttpRequest" {
		http.Error(w, "invalid request origin", http.StatusForbidden)
		return
	}
	rateLimit := server.CreateLimiter.Check(server.clientIP(request), time.Now())
	if rateLimit.Limit > 0 {
		w.Header().Set("X-RateLimit-Limit", strconv.Itoa(rateLimit.Limit))
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(rateLimit.Remaining))
	}
	if !rateLimit.Allowed {
		retryAfter := int(rateLimit.RetryAfter / time.Second)
		if retryAfter < 1 {
			retryAfter = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		http.Error(w, "creation rate limit exceeded", http.StatusTooManyRequests)
		return
	}
	var metadata transfer.Metadata
	decoder := json.NewDecoder(http.MaxBytesReader(w, request.Body, 64<<10))
	if err := decoder.Decode(&metadata); err != nil {
		http.Error(w, "invalid metadata", http.StatusBadRequest)
		return
	}
	session, tokens, err := server.Manager.CreateWithTokens(metadata)
	if err != nil {
		message := "invalid transfer metadata"
		switch {
		case errors.Is(err, transfer.ErrInvalidFileName):
			message = "invalid file name"
		case errors.Is(err, transfer.ErrInvalidMIME):
			message = "invalid MIME type"
		case errors.Is(err, transfer.ErrInvalidSHA256):
			message = "invalid SHA-256"
		case errors.Is(err, transfer.ErrInvalidFileSize):
			message = "file size exceeds limit"
		case errors.Is(err, transfer.ErrTransferLimit):
			http.Error(w, "active transfer limit reached", http.StatusServiceUnavailable)
			return
		}
		http.Error(w, message, http.StatusBadRequest)
		return
	}
	receiverURL := strings.TrimRight(server.BaseURL, "/") + "/receive/" + session.ID + "#token=" + tokens.ReceiverToken
	writeJSON(w, http.StatusCreated, map[string]interface{}{"id": session.ID, "expiresAt": session.ExpiresAt, "url": receiverURL, "senderToken": tokens.SenderToken})
}

func (server *Server) limits(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, server.Manager.Limits())
}
func (server *Server) transferRoutes(w http.ResponseWriter, request *http.Request) {
	path := strings.TrimPrefix(request.URL.Path, "/api/transfers/")
	if strings.HasSuffix(path, "/extend") {
		if request.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id := strings.TrimSuffix(path, "/extend")
		if id == "" || strings.Contains(id, "/") {
			http.Error(w, "transfer not found", http.StatusNotFound)
			return
		}
		server.extend(w, request, id)
		return
	}
	if request.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	server.get(w, request)
}

func (server *Server) extend(w http.ResponseWriter, request *http.Request, id string) {
	token := request.Header.Get("X-Sender-Token")
	if token == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err := server.Manager.ValidateToken(id, transfer.SenderRole, token); err != nil {
		switch {
		case errors.Is(err, transfer.ErrTransferNotFound):
			http.Error(w, "transfer not found", http.StatusNotFound)
		case errors.Is(err, transfer.ErrInvalidToken), errors.Is(err, transfer.ErrInvalidRole):
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		default:
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		}
		return
	}
	rateLimit := server.CreateLimiter.Check(server.clientIP(request), time.Now())
	if rateLimit.Limit > 0 {
		w.Header().Set("X-RateLimit-Limit", strconv.Itoa(rateLimit.Limit))
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(rateLimit.Remaining))
	}
	if !rateLimit.Allowed {
		retryAfter := int(rateLimit.RetryAfter / time.Second)
		if retryAfter < 1 {
			retryAfter = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}
	expiresAt, err := server.Manager.Extend(id, time.Now())
	if err != nil {
		switch {
		case errors.Is(err, transfer.ErrTransferNotFound):
			http.Error(w, "transfer not found", http.StatusNotFound)
		case errors.Is(err, transfer.ErrTransferExpired):
			http.Error(w, "transfer expired", http.StatusGone)
		default:
			http.Error(w, "transfer is no longer active", http.StatusConflict)
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"expiresAt": expiresAt})
}

func (server *Server) clientIP(request *http.Request) string {
	if server.TrustProxy {
		if forwarded := request.Header.Get("X-Forwarded-For"); forwarded != "" {
			return strings.TrimSpace(strings.Split(forwarded, ",")[0])
		}
	}
	if host, _, err := net.SplitHostPort(request.RemoteAddr); err == nil {
		return host
	}
	return request.RemoteAddr
}

func (server *Server) turnCredentials(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Validate origin if configured
	origin := r.Header.Get("Origin")
	if origin != "" {
		allowed := false
		for _, o := range server.TurnHandler.Cfg.AllowedOrigins {
			if o == "*" || o == origin {
				allowed = true
				break
			}
		}
		if !allowed {
			http.Error(w, "Origin not allowed", http.StatusForbidden)
			return
		}
	}

	server.TurnHandler.HandleCredentials(w, r)
}
func (server *Server) get(w http.ResponseWriter, request *http.Request) {
	id := strings.TrimPrefix(request.URL.Path, "/api/transfers/")
	session, ok := server.Manager.Get(id)
	if !ok {
		http.Error(w, "transfer not found", http.StatusNotFound)
		return
	}
	if session.Expire(time.Now()) {
		http.Error(w, "transfer expired", http.StatusGone)
		return
	}
	writeJSON(w, http.StatusOK, session.Snapshot())
}
func writeJSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
