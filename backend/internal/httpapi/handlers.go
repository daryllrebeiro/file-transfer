package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"file-transfer/backend/internal/transfer"
)

type Server struct {
	Manager       *transfer.Manager
	BaseURL       string
	CreateLimiter *CreationLimiter
	MetricsToken  string
	TrustProxy    bool
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
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/metrics", server.metrics)
	mux.HandleFunc("/api/transfers", server.create)
	mux.HandleFunc("/api/limits", server.limits)
	mux.HandleFunc("/api/transfers/", server.get)
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
	writeJSON(w, http.StatusOK, server.Manager.Metrics())
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
