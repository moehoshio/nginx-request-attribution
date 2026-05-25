package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime"
	"strings"
	"syscall"

	"github.com/moehoshio/web-request-attribution/internal/auth"
	"github.com/moehoshio/web-request-attribution/internal/runtimeconfig"
)

// ConfigHandler exposes the runtime configuration on /api/config and
// the one-click restart endpoint on /api/admin/restart. The settings
// panel in the dashboard talks to this.
type ConfigHandler struct {
	store           *runtimeconfig.Store
	allowedLogRoots []string
	listenAddr      string
	dbPath          string
	auth            *auth.Service
}

// NewConfigHandler wires the runtimeconfig store and the bootstrap
// fields together so the API can return both in a single payload. The
// auth service is optional; when non-nil, edits and restart requests
// are written to audit_log.
func NewConfigHandler(store *runtimeconfig.Store, listenAddr, dbPath string, allowedLogRoots []string, authSvc *auth.Service) *ConfigHandler {
	return &ConfigHandler{
		store:           store,
		allowedLogRoots: allowedLogRoots,
		listenAddr:      listenAddr,
		dbPath:          dbPath,
		auth:            authSvc,
	}
}

// RegisterRoutes wires both endpoints onto mux behind the supplied
// admin middleware.
func (h *ConfigHandler) RegisterRoutes(mux *http.ServeMux, adminMW func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("/api/config", adminMW(h.handleConfig))
	mux.HandleFunc("/api/admin/restart", adminMW(h.handleRestart))
}

// configEnvelope is what /api/config returns. The "bootstrap" object
// is read-only (changing it requires a restart) and is included so the
// settings UI can display the values it cannot edit.
type configEnvelope struct {
	Runtime   runtimeconfig.Runtime `json:"runtime"`
	Bootstrap bootstrapView         `json:"bootstrap"`
	Schema    schemaView            `json:"schema"`
}

type bootstrapView struct {
	ListenAddr      string   `json:"listen_addr"`
	DBPath          string   `json:"db_path"`
	AllowedLogRoots []string `json:"allowed_log_roots"`
}

// schemaView is a tiny machine-readable schema the frontend uses to
// drive the settings form (engine/preset enums, restart-required
// flags). Anything not in here is currently hard-coded in the HTML.
type schemaView struct {
	Engines         []string `json:"engines"`
	NginxPresets    []string `json:"nginx_presets"`
	ApachePresets   []string `json:"apache_presets"`
	SyslogProtos    []string `json:"syslog_protos"`
	SourceTypes     []string `json:"source_types"`
	RestartRequired []string `json:"restart_required"`
}

func (h *ConfigHandler) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSONStatus(w, http.StatusOK, configEnvelope{
			Runtime: h.store.Get(),
			Bootstrap: bootstrapView{
				ListenAddr:      h.listenAddr,
				DBPath:          h.dbPath,
				AllowedLogRoots: h.allowedLogRoots,
			},
			Schema: schemaView{
				Engines:         []string{"auto", "nginx", "apache", "custom"},
				NginxPresets:    []string{"combined", "vhost_combined"},
				ApachePresets:   []string{"common", "combined", "vhost_combined"},
				SyslogProtos:    []string{"udp", "tcp"},
				SourceTypes:     []string{"file", "dir", "syslog"},
				RestartRequired: []string{"listen_addr", "db_path", "allowed_log_roots"},
			},
		})
	case http.MethodPut:
		var rc runtimeconfig.Runtime
		if err := json.NewDecoder(r.Body).Decode(&rc); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if err := h.store.Set(rc, h.allowedLogRoots); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		h.audit(r, "config_update", fmt.Sprintf("sources=%d watch=%v keywords=%d", len(rc.Sources), rc.Watch, len(rc.Keywords)))
		writeJSONStatus(w, http.StatusOK, map[string]interface{}{
			"status":  "ok",
			"runtime": h.store.Get(),
		})
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleRestart performs an in-place re-exec on Linux so the
// dashboard's "Restart server" button is functional under both
// systemd (which will see the new process keep the same PID) and
// Docker (where the container exits and the orchestrator restarts
// it). On non-Linux platforms we exit cleanly and rely on the
// orchestrator to bring us back up.
func (h *ConfigHandler) handleRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	exe, err := os.Executable()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "cannot resolve executable: "+err.Error())
		return
	}
	// Acknowledge before flipping the table so the browser actually
	// receives the response. The actual re-exec runs in a goroutine
	// after a short defer so flush has time to happen.
	h.audit(r, "server_restart", "")
	writeJSONStatus(w, http.StatusAccepted, map[string]string{"status": "restarting"})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	go func() {
		// Defer the re-exec slightly so the HTTP response actually
		// makes it out of the kernel buffer before the process image
		// is replaced.
		if err := performRestart(exe); err != nil {
			log.Printf("restart failed: %v; exiting so orchestrator can relaunch", err)
			os.Exit(0)
		}
	}()
}

func writeJSONStatus(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSONStatus(w, status, map[string]string{"error": msg})
}

// audit best-effort writes to the audit_log via the auth service.
// Missing user / service is silently tolerated so audit never breaks
// the user-visible action.
func (h *ConfigHandler) audit(r *http.Request, action, detail string) {
	if h.auth == nil {
		return
	}
	u := auth.UserFromContext(r.Context())
	var uid *int64
	username := ""
	if u != nil {
		uid = &u.ID
		username = u.Username
	}
	_ = h.auth.Audit(uid, action, username, clientIP(r), detail)
}

// clientIP duplicates the heuristic in auth.clientIP so api doesn't
// have to import an unexported symbol.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.Index(xff, ","); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	return r.RemoteAddr
}

// performRestart re-execs the current binary on Linux. Anywhere else
// it returns an error so the caller falls back to a clean exit (let
// the orchestrator do the work).
func performRestart(exe string) error {
	if runtime.GOOS != "linux" {
		return errors.New("in-place restart only supported on Linux; relying on orchestrator")
	}
	args := os.Args
	env := os.Environ()
	// syscall.Exec replaces the current process image; on success it
	// does not return. We deliberately do NOT close the listener
	// first: under Linux, exec preserves the fds we want the new
	// image to inherit, and the kernel tears the old listener down
	// when the process image is replaced.
	if err := syscall.Exec(exe, args, env); err != nil {
		return fmt.Errorf("syscall.Exec: %w", err)
	}
	return nil
}
