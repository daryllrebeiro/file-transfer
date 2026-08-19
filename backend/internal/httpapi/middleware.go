package httpapi

import "net/http"

func CORS(origins []string, next http.Handler) http.Handler { allowed := map[string]bool{}; for _, origin := range origins { allowed[origin] = true }; return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { origin := r.Header.Get("Origin"); if allowed[origin] { w.Header().Set("Access-Control-Allow-Origin", origin); w.Header().Set("Vary", "Origin"); w.Header().Set("Access-Control-Allow-Headers", "Content-Type"); w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS") }; if r.Method == http.MethodOptions { w.WriteHeader(http.StatusNoContent); return }; next.ServeHTTP(w, r) }) }