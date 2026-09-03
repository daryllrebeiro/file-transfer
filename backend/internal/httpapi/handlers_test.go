package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"file-transfer/backend/internal/transfer"
)

func TestMetricsRequiresToken(t *testing.T) {
	manager := transfer.NewManager(time.Minute, 100, 10)
	server := &Server{Manager: manager, MetricsToken: "metrics-secret"}
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	response := httptest.NewRecorder()
	server.metrics(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.Code)
	}
	request.Header.Set("Authorization", "Bearer metrics-secret")
	response = httptest.NewRecorder()
	server.metrics(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
}

func TestClientIPDoesNotTrustForwardedHeaderByDefault(t *testing.T) {
	server := &Server{}
	request := httptest.NewRequest(http.MethodPost, "/api/transfers", nil)
	request.RemoteAddr = "192.0.2.10:1234"
	request.Header.Set("X-Forwarded-For", "198.51.100.20")
	if got := server.clientIP(request); got != "192.0.2.10" {
		t.Fatalf("unexpected client IP: %s", got)
	}
	server.TrustProxy = true
	if got := server.clientIP(request); got != "198.51.100.20" {
		t.Fatalf("expected trusted forwarded IP, got %s", got)
	}
}

func TestCreationLimiterReportsRemainingAndRetryAfter(t *testing.T) {
	limiter := NewCreationLimiter(2)
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	first := limiter.Check("client", now)
	if !first.Allowed || first.Remaining != 1 || first.Limit != 2 {
		t.Fatalf("unexpected first result: %+v", first)
	}
	second := limiter.Check("client", now.Add(10*time.Second))
	if !second.Allowed || second.Remaining != 0 {
		t.Fatalf("unexpected second result: %+v", second)
	}
	blocked := limiter.Check("client", now.Add(20*time.Second))
	if blocked.Allowed || blocked.RetryAfter != 40*time.Second {
		t.Fatalf("unexpected blocked result: %+v", blocked)
	}
	reset := limiter.Check("client", now.Add(time.Minute))
	if !reset.Allowed || reset.Remaining != 1 {
		t.Fatalf("unexpected reset result: %+v", reset)
	}
}

func TestCreateReturnsRateLimitHeaders(t *testing.T) {
	server := &Server{
		Manager:       transfer.NewManager(time.Minute, 100, 10),
		CreateLimiter: NewCreationLimiter(1),
	}
	requestBody := `{"fileName":"a.txt","fileSize":10,"chunkSize":5}`

	request := httptest.NewRequest(http.MethodPost, "/api/transfers", strings.NewReader(requestBody))
	request.Header.Set("X-Requested-With", "XMLHttpRequest")
	request.RemoteAddr = "192.0.2.10:1234"
	response := httptest.NewRecorder()
	server.create(response, request)
	if response.Code != http.StatusCreated || response.Header().Get("X-RateLimit-Remaining") != "0" {
		t.Fatalf("unexpected first response: %d, headers=%v", response.Code, response.Header())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/transfers", strings.NewReader(requestBody))
	request.Header.Set("X-Requested-With", "XMLHttpRequest")
	request.RemoteAddr = "192.0.2.10:1234"
	response = httptest.NewRecorder()
	server.create(response, request)
	if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") == "" {
		t.Fatalf("unexpected limited response: %d, headers=%v", response.Code, response.Header())
	}
}

func TestLimitsEndpointReturnsManagerLimits(t *testing.T) {
	server := &Server{Manager: transfer.NewManager(time.Minute, 123, 17)}
	request := httptest.NewRequest(http.MethodGet, "/api/limits", nil)
	response := httptest.NewRecorder()
	server.limits(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	var limits transfer.Limits
	if err := json.NewDecoder(response.Body).Decode(&limits); err != nil {
		t.Fatal(err)
	}
	if limits.MaxFileSize != 123 || limits.MaxChunkSize != 17 {
		t.Fatalf("unexpected limits: %+v", limits)
	}
}
