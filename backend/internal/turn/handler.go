package turn

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"file-transfer/backend/internal/config"
)

type TurnCredentials struct {
	Username string `json:"username"`
	Credential string `json:"credential"`
	TTL      int    `json:"ttl"`
	URIs     []string `json:"uris"`
}

type TurnHandler struct {
	Cfg *config.Config
}

func NewTurnHandler(cfg *config.Config) *TurnHandler {
	return &TurnHandler{Cfg: cfg}
}

func (h *TurnHandler) HandleCredentials(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Generate time-limited credentials
	username := h.generateUsername()
	password := h.generatePassword()
	
	creds := TurnCredentials{
		Username:   username,
		Credential: password,
		TTL:        h.Cfg.TurnCredentialTTL,
		URIs:       h.Cfg.TurnURIs,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(creds)
}

func (h *TurnHandler) generateUsername() string {
	// Format: timestamp:random
	bytes := make([]byte, 8)
	rand.Read(bytes)
	return fmt.Sprintf("%d:%s", time.Now().Unix(), base64.RawURLEncoding.EncodeToString(bytes))
}

func (h *TurnHandler) generatePassword() string {
	bytes := make([]byte, 16)
	rand.Read(bytes)
	return base64.RawURLEncoding.EncodeToString(bytes)
}

// ValidateTURNOrigin checks if the origin is allowed for TURN credentials
func ValidateTURNOrigin(origin string, allowedOrigins []string) bool {
	for _, allowed := range allowedOrigins {
		if allowed == "*" || allowed == origin {
			return true
		}
		// Support wildcard subdomains
		if strings.HasPrefix(allowed, "*.") {
			suffix := strings.TrimPrefix(allowed, "*.")
			if strings.HasSuffix(origin, suffix) {
				return true
			}
		}
	}
	return false
}