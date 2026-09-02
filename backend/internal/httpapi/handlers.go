package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"net"
	"net/http"
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

func NewCreationLimiter(limit int) *CreationLimiter {
	return &CreationLimiter{limit: limit, windows: make(map[string]rateWindow)}
}
func (limiter *CreationLimiter) Allow(key string, now time.Time) bool {
	if limiter == nil || limiter.limit <= 0 {
		return true
	}
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	window := limiter.windows[key]
	if now.Sub(window.started) >= time.Minute {
		window = rateWindow{started: now}
	}
	if window.count >= limiter.limit {
		limiter.windows[key] = window
		return false
	}
	window.count++
	limiter.windows[key] = window
	return true
}
func (server *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/metrics", server.metrics)
	mux.HandleFunc("/api/transfers", server.create)
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
	if !server.CreateLimiter.Allow(server.clientIP(request), time.Now()) {
		http.Error(w, "creation rate limit exceeded", http.StatusTooManyRequests)
		return
	}
	var metadata transfer.Metadata
	decoder := json.NewDecoder(http.MaxBytesReader(w, request.Body, 64<<10))
	if decoder.Decode(&metadata) != nil {
		http.Error(w, "invalid metadata", http.StatusBadRequest)
		return
	}
	session, tokens, err := server.Manager.CreateWithTokens(metadata)
	if err != nil {
		http.Error(w, "invalid transfer metadata", http.StatusBadRequest)
		return
	}
	receiverURL := strings.TrimRight(server.BaseURL, "/") + "/receive/" + session.ID + "#token=" + tokens.ReceiverToken
	writeJSON(w, http.StatusCreated, map[string]interface{}{"id": session.ID, "expiresAt": session.ExpiresAt, "url": receiverURL, "senderToken": tokens.SenderToken})
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
