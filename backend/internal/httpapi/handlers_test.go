package httpapi

import (
	"net/http"
	"net/http/httptest"
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
