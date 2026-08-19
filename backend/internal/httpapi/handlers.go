package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"file-transfer/backend/internal/transfer"
)

type Server struct { Manager *transfer.Manager; BaseURL string }
func (server *Server) Routes() http.Handler { mux := http.NewServeMux(); mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }); mux.HandleFunc("/api/transfers", server.create); mux.HandleFunc("/api/transfers/", server.get); return mux }
func (server *Server) create(w http.ResponseWriter, request *http.Request) { if request.Method != http.MethodPost { http.Error(w, "method not allowed", http.StatusMethodNotAllowed); return }; var metadata transfer.Metadata; if json.NewDecoder(http.MaxBytesReader(w, request.Body, 64<<10)).Decode(&metadata) != nil { http.Error(w, "invalid metadata", http.StatusBadRequest); return }; session, err := server.Manager.Create(metadata); if err != nil { http.Error(w, err.Error(), http.StatusBadRequest); return }; writeJSON(w, http.StatusCreated, map[string]interface{}{ "id": session.ID, "expiresAt": session.ExpiresAt, "url": strings.TrimRight(server.BaseURL, "/")+"/receive/"+session.ID }) }
func (server *Server) get(w http.ResponseWriter, request *http.Request) { id := strings.TrimPrefix(request.URL.Path, "/api/transfers/"); session, ok := server.Manager.Get(id); if !ok { http.Error(w, "transfer not found", http.StatusNotFound); return }; if session.Expire(time.Now()) { http.Error(w, "transfer expired", http.StatusGone); return }; writeJSON(w, http.StatusOK, session.Snapshot()) }
func writeJSON(w http.ResponseWriter, status int, value interface{}) { w.Header().Set("Content-Type", "application/json"); w.WriteHeader(status); _ = json.NewEncoder(w).Encode(value) }