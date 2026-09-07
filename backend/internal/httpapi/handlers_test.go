package httpapi

import (
	"encoding/json"
	"errors"
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

func TestMetricsPrometheusFormat(t *testing.T) {
	server := &Server{Manager: transfer.NewManager(time.Minute, 100, 10), MetricsToken: "metrics-secret", MetricsFormat: "prometheus"}
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	request.Header.Set("Authorization", "Bearer metrics-secret")
	response := httptest.NewRecorder()
	server.metrics(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	body := response.Body.String()
	if !strings.Contains(body, "relay_active_transfers 0") || !strings.Contains(body, "relay_transfers_created_total") {
		t.Fatalf("unexpected prometheus body: %s", body)
	}
	if !strings.Contains(response.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("unexpected content type: %s", response.Header().Get("Content-Type"))
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

func TestExtendRequiresSenderToken(t *testing.T) {
	manager := transfer.NewManager(time.Minute, 100, 10)
	server := &Server{Manager: manager, CreateLimiter: NewCreationLimiter(10)}
	session, _, err := manager.CreateWithTokens(transfer.Metadata{FileName: "a.txt", FileSize: 10, ChunkSize: 5})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/transfers/"+session.ID+"/extend", nil)
	request.RemoteAddr = "192.0.2.10:1234"
	response := httptest.NewRecorder()
	server.transferRoutes(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/transfers/"+session.ID+"/extend", nil)
	request.Header.Set("X-Sender-Token", "wrong")
	request.RemoteAddr = "192.0.2.10:1234"
	response = httptest.NewRecorder()
	server.transferRoutes(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for bad token, got %d", response.Code)
	}
}

func TestExtendAdvancesExpiryAndCapsAtTwoTTLs(t *testing.T) {
	manager := transfer.NewManager(time.Minute, 100, 10)
	server := &Server{Manager: manager, CreateLimiter: NewCreationLimiter(10)}
	session, tokens, err := manager.CreateWithTokens(transfer.Metadata{FileName: "a.txt", FileSize: 10, ChunkSize: 5})
	if err != nil {
		t.Fatal(err)
	}
	original := session.Snapshot().ExpiresAt

	request := httptest.NewRequest(http.MethodPost, "/api/transfers/"+session.ID+"/extend", nil)
	request.Header.Set("X-Sender-Token", tokens.SenderToken)
	request.RemoteAddr = "192.0.2.10:1234"
	response := httptest.NewRecorder()
	server.transferRoutes(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	var body struct {
		ExpiresAt time.Time `json:"expiresAt"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.ExpiresAt.After(original) {
		t.Fatal("expected expiry to advance after extend")
	}

	cap := session.Snapshot().CreatedAt.Add(2 * time.Minute)
	if body.ExpiresAt.After(cap) {
		t.Fatalf("expiry %v exceeded cap %v", body.ExpiresAt, cap)
	}
}

func TestManagerExtendRejectsTerminalState(t *testing.T) {
	manager := transfer.NewManager(time.Minute, 100, 10)
	session, tokens, err := manager.CreateWithTokens(transfer.Metadata{FileName: "a.txt", FileSize: 10, ChunkSize: 5})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Cancel(session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Extend(session.ID, time.Now()); !errors.Is(err, transfer.ErrInvalidState) {
		t.Fatalf("expected terminal rejection, got %v", err)
	}
	_ = tokens
}

func TestCreationLimiterBoundaryAtLimit(t *testing.T) {
	limiter := NewCreationLimiter(3)
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

	// First request
	r1 := limiter.Check("client", now)
	if !r1.Allowed || r1.Remaining != 2 || r1.Limit != 3 {
		t.Fatalf("first: %+v", r1)
	}

	// Second request
	r2 := limiter.Check("client", now.Add(1*time.Second))
	if !r2.Allowed || r2.Remaining != 1 {
		t.Fatalf("second: %+v", r2)
	}

	// Third request - exactly at limit
	r3 := limiter.Check("client", now.Add(2*time.Second))
	if !r3.Allowed || r3.Remaining != 0 {
		t.Fatalf("third (at limit): %+v", r3)
	}

	// Fourth request - over limit
	r4 := limiter.Check("client", now.Add(3*time.Second))
	if r4.Allowed || r4.Remaining != 0 || r4.RetryAfter <= 0 {
		t.Fatalf("fourth (over limit): %+v", r4)
	}
}

func TestCreationLimiterWindowReset(t *testing.T) {
	limiter := NewCreationLimiter(2)
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

	// Use up the limit
	r1 := limiter.Check("client", now)
	if !r1.Allowed {
		t.Fatalf("first should be allowed: %+v", r1)
	}
	r2 := limiter.Check("client", now.Add(1*time.Second))
	if !r2.Allowed {
		t.Fatalf("second should be allowed: %+v", r2)
	}

	// Third should be blocked
	r3 := limiter.Check("client", now.Add(2*time.Second))
	if r3.Allowed {
		t.Fatalf("third should be blocked: %+v", r3)
	}

	// After window resets (1 minute + 1 second), should be allowed again
	r4 := limiter.Check("client", now.Add(time.Minute+1*time.Second))
	if !r4.Allowed || r4.Remaining != 1 {
		t.Fatalf("after window reset: %+v", r4)
	}
}

func TestCreationLimiterSeparateClients(t *testing.T) {
	limiter := NewCreationLimiter(1)
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

	// Client A uses up limit
	r1 := limiter.Check("client-a", now)
	if !r1.Allowed {
		t.Fatalf("client-a first: %+v", r1)
	}
	r2 := limiter.Check("client-a", now.Add(1*time.Second))
	if r2.Allowed {
		t.Fatalf("client-a second should be blocked: %+v", r2)
	}

	// Client B should have independent limit
	r3 := limiter.Check("client-b", now.Add(1*time.Second))
	if !r3.Allowed || r3.Remaining != 0 {
		t.Fatalf("client-b first should be allowed: %+v", r3)
	}
}

func TestCreationLimiterNilAndZeroLimit(t *testing.T) {
	// nil limiter should allow everything
	if !NewCreationLimiter(0).Check("client", time.Now()).Allowed {
		t.Fatal("zero limit should allow all")
	}
	if !NewCreationLimiter(-1).Check("client", time.Now()).Allowed {
		t.Fatal("negative limit should allow all")
	}
}
