package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/moehoshio/web-request-attribution/internal/storage"
)

type Handler struct {
	store *storage.Store
}

func NewHandler(store *storage.Store) *Handler {
	return &Handler{store: store}
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/stats", h.handleStats)
	mux.HandleFunc("/api/requests", h.handleRequests)
}

// RegisterRoutesWithMiddleware registers the API routes wrapped with the
// supplied middleware (e.g. auth.RequireAuth). Use this when the server
// has authentication enabled.
func (h *Handler) RegisterRoutesWithMiddleware(mux *http.ServeMux, mw func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("/api/stats", mw(h.handleStats))
	mux.HandleFunc("/api/requests", mw(h.handleRequests))
}

func (h *Handler) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	f := parseFilter(r)
	result, err := h.store.Stats(f)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, result)
}

func (h *Handler) handleRequests(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	f := parseFilter(r)
	result, err := h.store.Query(f)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, result)
}

func parseFilter(r *http.Request) storage.QueryFilter {
	q := r.URL.Query()
	f := storage.QueryFilter{
		IP:      q.Get("ip"),
		Path:    q.Get("path"),
		Domain:  q.Get("domain"),
		Method:  q.Get("method"),
		OS:      q.Get("os"),
		Browser: q.Get("browser"),
		Query:   q.Get("query"),
		Keyword: q.Get("keyword"),
	}

	if v := q.Get("status"); v != "" {
		f.Status, _ = strconv.Atoi(v)
	}
	if v := q.Get("limit"); v != "" {
		f.Limit, _ = strconv.Atoi(v)
	}
	if v := q.Get("offset"); v != "" {
		f.Offset, _ = strconv.Atoi(v)
	}
	if v := q.Get("start"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			f.StartTime = &t
		}
	}
	if v := q.Get("end"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			end := t.Add(24*time.Hour - time.Second)
			f.EndTime = &end
		}
	}

	return f
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(v)
}
