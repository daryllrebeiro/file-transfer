package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Port                string
	PublicBaseURL       string
	AllowedOrigins      []string
	TransferTTL         time.Duration
	MaxFileSize         int64
	MaxChunkSize        int
	MaxActiveTransfers  int
	MaxConnections      int
	CreateRatePerMinute int
	MetricsToken        string
	TrustProxy          bool
	WriteTimeout        time.Duration
	LogFormat           string
	LogLevel            string
	MetricsFormat       string
}

func Load() Config {
	return Config{
		Port: env("PORT", "8080"), PublicBaseURL: env("PUBLIC_BASE_URL", "http://localhost:5173"),
		AllowedOrigins: split(env("ALLOWED_ORIGINS", "http://localhost:5173")),
		TransferTTL:    duration("TRANSFER_TTL", 15*time.Minute), MaxFileSize: integer("MAX_FILE_SIZE", 10<<30), MaxChunkSize: int(integer("MAX_CHUNK_SIZE", 2<<20)),
		MaxActiveTransfers: int(integer("MAX_ACTIVE_TRANSFERS", 1000)), MaxConnections: int(integer("MAX_CONNECTIONS", 2000)), CreateRatePerMinute: int(integer("CREATE_RATE_PER_MINUTE", 20)), MetricsToken: os.Getenv("METRICS_TOKEN"), TrustProxy: boolean("TRUST_PROXY", false), WriteTimeout: duration("WRITE_TIMEOUT", 5*time.Minute), LogFormat: env("LOG_FORMAT", "text"), LogLevel: env("LOG_LEVEL", "info"), MetricsFormat: env("METRICS_FORMAT", "json"),
	}
}
func boolean(key string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if value == "" {
		return fallback
	}
	return value == "1" || value == "true" || value == "yes"
}
func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
func split(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}
func duration(key string, fallback time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if parsed, err := time.ParseDuration(value); err == nil {
			return parsed
		}
		if seconds, err := strconv.Atoi(value); err == nil {
			return time.Duration(seconds) * time.Second
		}
	}
	return fallback
}
func integer(key string, fallback int64) int64 {
	if value := os.Getenv(key); value != "" {
		if parsed, err := strconv.ParseInt(value, 10, 64); err == nil {
			return parsed
		}
	}
	return fallback
}
