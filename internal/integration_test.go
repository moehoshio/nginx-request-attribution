// Package integration_test boots the application in-process and
// exercises the full request lifecycle: import a fixture log, log in,
// query the dashboard API, and edit a runtime setting.
package integration_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/moehoshio/web-request-attribution/internal/api"
	"github.com/moehoshio/web-request-attribution/internal/auth"
	"github.com/moehoshio/web-request-attribution/internal/parser"
	"github.com/moehoshio/web-request-attribution/internal/runtimeconfig"
	"github.com/moehoshio/web-request-attribution/internal/storage"
	"github.com/moehoshio/web-request-attribution/internal/watcher"

	"context"
)

// fixtureLog is a minimal multi-line Nginx combined access log used by
// the integration tests.
const fixtureLog = `192.168.1.1 - - [10/Oct/2023:10:00:00 +0000] "GET /index.html HTTP/1.1" 200 5120 "https://example.com/" "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/91.0"
192.168.1.2 - admin [10/Oct/2023:10:00:01 +0000] "POST /api/login HTTP/1.1" 302 0 "-" "curl/7.68.0"
10.0.0.5 - - [10/Oct/2023:10:00:02 +0000] "GET /search?q=admin HTTP/1.1" 200 1024 "-" "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Safari/537.36"
10.0.0.5 - - [10/Oct/2023:10:00:03 +0000] "GET /api/users HTTP/1.1" 200 2048 "-" "Mozilla/5.0 (X11; Linux x86_64) Firefox/89.0"
172.16.0.1 - - [10/Oct/2023:10:00:04 +0000] "DELETE /api/users/5 HTTP/1.1" 204 0 "-" "Mozilla/5.0 (iPhone; CPU iPhone OS 14_0) Safari/604.1"
`

// setupServer constructs a fully-wired test server: storage, auth,
// runtime config, watcher manager, API handlers. It returns the
// httptest server and a cleanup function.
func setupServer(t *testing.T) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	// Runtime config seed (no file sources; import is done manually).
	seed := runtimeconfig.Runtime{
		Watch:    false,
		Keywords: []string{"admin", "login"},
		Sources:  nil,
	}
	rcStore, err := runtimeconfig.New(store.DB(), seed)
	if err != nil {
		t.Fatalf("runtimeconfig.New: %v", err)
	}

	// Watcher manager (idle; watch=false).
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mgr := watcher.NewManager(ctx, store)
	if err := mgr.Apply(rcStore.Get()); err != nil {
		t.Fatalf("mgr.Apply: %v", err)
	}
	rcStore.Subscribe(func(rc runtimeconfig.Runtime) {
		_ = mgr.Apply(rc)
	})

	// Auth service with a test admin (low bcrypt cost for speed).
	authSvc, err := auth.New(store.DB(), auth.Options{
		BcryptCost: 4,
		SessionTTL: 1 * time.Hour,
	})
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}
	created, err := authSvc.BootstrapAdmin("admin", "testpassword123")
	if err != nil {
		t.Fatalf("BootstrapAdmin: %v", err)
	}
	if !created {
		t.Fatal("expected bootstrap admin to be created")
	}

	// Wire HTTP.
	mux := http.NewServeMux()
	authH := auth.NewHandler(authSvc)
	authH.RegisterRoutes(mux)

	apiH := api.NewHandler(store)
	apiH.RegisterRoutesWithMiddleware(mux, authH.RequireAuth)

	cfgH := api.NewConfigHandler(rcStore, ":8080", dbPath, []string{dir}, authSvc)
	cfgH.RegisterRoutes(mux, authH.RequireAdmin)

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	// Import fixture lines directly into the store (simulates the
	// -import workflow without needing the file on disk to be tailed).
	p, _ := parser.New(parser.FormatConfig{Engine: "nginx", Preset: "combined"})
	importFixture(t, store, p, rcStore.Get().Keywords)

	return ts
}

func importFixture(t *testing.T, store *storage.Store, p parser.Parser, keywords []string) {
	t.Helper()
	lines := bytes.Split([]byte(fixtureLog), []byte("\n"))
	var batch []*parser.LogEntry
	for _, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		entry, err := p.Parse(string(line))
		if err != nil {
			t.Fatalf("parse fixture line: %v", err)
		}
		batch = append(batch, entry)
	}
	if err := store.InsertBatch(batch, keywords); err != nil {
		t.Fatalf("InsertBatch: %v", err)
	}
}

// newClient returns an HTTP client with a cookie jar so session
// cookies are automatically persisted across requests.
func newClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}
}

// login authenticates against the test server and returns the CSRF
// token for subsequent non-GET requests.
func login(t *testing.T, client *http.Client, baseURL string) string {
	t.Helper()

	// First, get a CSRF cookie.
	resp, err := client.Get(baseURL + "/api/auth/csrf")
	if err != nil {
		t.Fatalf("GET /api/auth/csrf: %v", err)
	}
	resp.Body.Close()

	body, _ := json.Marshal(map[string]string{
		"username": "admin",
		"password": "testpassword123",
	})
	resp, err = client.Post(baseURL+"/api/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/auth/login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("login failed: %d %s", resp.StatusCode, string(b))
	}
	var result struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	return result.CSRFToken
}

// TestIntegrationFullLifecycle exercises the end-to-end flow: import →
// login → query stats → query requests → edit runtime config.
func TestIntegrationFullLifecycle(t *testing.T) {
	ts := setupServer(t)
	client := newClient()

	// 1. Unauthenticated request should be 401.
	resp, err := client.Get(ts.URL + "/api/stats")
	if err != nil {
		t.Fatalf("GET /api/stats: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without auth, got %d", resp.StatusCode)
	}

	// 2. Login as admin.
	csrf := login(t, client, ts.URL)
	if csrf == "" {
		t.Fatal("expected non-empty CSRF token")
	}

	// 3. GET /api/stats — should return our fixture data.
	resp, err = client.Get(ts.URL + "/api/stats")
	if err != nil {
		t.Fatalf("GET /api/stats: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET /api/stats: %d %s", resp.StatusCode, string(b))
	}
	var stats map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	total, _ := stats["total_requests"].(float64)
	if total != 5 {
		t.Errorf("expected total_requests=5, got %v", total)
	}

	// 4. GET /api/requests — verify pagination.
	resp, err = client.Get(ts.URL + "/api/requests?limit=2&offset=0")
	if err != nil {
		t.Fatalf("GET /api/requests: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/requests: %d", resp.StatusCode)
	}
	var reqResult struct {
		Total int           `json:"total"`
		Rows  []interface{} `json:"rows"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&reqResult); err != nil {
		t.Fatalf("decode requests: %v", err)
	}
	if len(reqResult.Rows) != 2 {
		t.Errorf("expected 2 rows in page, got %d", len(reqResult.Rows))
	}
	if reqResult.Total != 5 {
		t.Errorf("expected total=5, got %d", reqResult.Total)
	}

	// 5. GET /api/config — verify bootstrap and runtime sections.
	resp, err = client.Get(ts.URL + "/api/config")
	if err != nil {
		t.Fatalf("GET /api/config: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/config: %d", resp.StatusCode)
	}
	var cfgResp struct {
		Runtime   runtimeconfig.Runtime `json:"runtime"`
		Bootstrap struct {
			ListenAddr string `json:"listen_addr"`
		} `json:"bootstrap"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&cfgResp); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if cfgResp.Bootstrap.ListenAddr != ":8080" {
		t.Errorf("bootstrap listen_addr = %q, want :8080", cfgResp.Bootstrap.ListenAddr)
	}
	if cfgResp.Runtime.Watch != false {
		t.Errorf("expected watch=false in initial runtime config")
	}

	// 6. PUT /api/config — add a keyword and enable watch.
	newRuntime := cfgResp.Runtime
	newRuntime.Keywords = append(newRuntime.Keywords, "search")
	newRuntime.Watch = true
	body, _ := json.Marshal(newRuntime)
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(auth.CSRFHeaderName, csrf)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("PUT /api/config: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("PUT /api/config: %d %s", resp.StatusCode, string(b))
	}

	// 7. Verify the update persisted.
	resp, err = client.Get(ts.URL + "/api/config")
	if err != nil {
		t.Fatalf("GET /api/config (2): %v", err)
	}
	defer resp.Body.Close()
	var cfgResp2 struct {
		Runtime runtimeconfig.Runtime `json:"runtime"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&cfgResp2); err != nil {
		t.Fatalf("decode config (2): %v", err)
	}
	if !cfgResp2.Runtime.Watch {
		t.Error("expected watch=true after PUT")
	}
	found := false
	for _, kw := range cfgResp2.Runtime.Keywords {
		if kw == "search" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'search' in keywords after PUT")
	}

	// 8. GET /api/auth/me — confirm session still valid.
	resp, err = client.Get(ts.URL + "/api/auth/me")
	if err != nil {
		t.Fatalf("GET /api/auth/me: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/auth/me: %d (session lost?)", resp.StatusCode)
	}
}

// TestIntegrationDirSourceIngest verifies that configuring a dir source
// via the runtime config triggers ingestion of fixture files.
func TestIntegrationDirSourceIngest(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	logDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write a fixture log file.
	logPath := filepath.Join(logDir, "access.log")
	if err := os.WriteFile(logPath, []byte(fixtureLog), 0644); err != nil {
		t.Fatal(err)
	}

	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	seed := runtimeconfig.Runtime{
		Watch:    true,
		Keywords: []string{"admin"},
		Sources: []runtimeconfig.Source{{
			Name:    "test-dir",
			Type:    runtimeconfig.SourceDir,
			Path:    logDir,
			Pattern: "access*.log*",
			Format:  parser.FormatConfig{Engine: "nginx", Preset: "combined"},
		}},
	}
	rcStore, err := runtimeconfig.New(store.DB(), seed)
	if err != nil {
		t.Fatalf("runtimeconfig.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := watcher.NewManager(ctx, store)
	if err := mgr.Apply(rcStore.Get()); err != nil {
		t.Fatalf("mgr.Apply: %v", err)
	}

	// Wait for the dir watcher to ingest.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		if err := store.DB().QueryRow(`SELECT COUNT(*) FROM requests`).Scan(&count); err == nil && count >= 5 {
			// Success: all 5 lines ingested.
			cancel()
			mgr.Stop()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	var count int
	store.DB().QueryRow(`SELECT COUNT(*) FROM requests`).Scan(&count)
	cancel()
	mgr.Stop()
	t.Fatalf("expected >= 5 rows from dir source, got %d", count)
}
