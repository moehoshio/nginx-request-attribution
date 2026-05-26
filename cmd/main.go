package main

import (
	"bufio"
	"compress/gzip"
	"context"
	"embed"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/moehoshio/web-request-attribution/internal/api"
	"github.com/moehoshio/web-request-attribution/internal/auth"
	"github.com/moehoshio/web-request-attribution/internal/config"
	"github.com/moehoshio/web-request-attribution/internal/parser"
	"github.com/moehoshio/web-request-attribution/internal/runtimeconfig"
	"github.com/moehoshio/web-request-attribution/internal/storage"
	"github.com/moehoshio/web-request-attribution/internal/watcher"
)

//go:embed all:static
var staticFiles embed.FS

func main() {
	configPath := flag.String("config", "config.json", "path to config file")
	importFile := flag.String("import", "", "import a log file and exit")
	importFormatEngine := flag.String("import-format", "auto", "parser engine for -import (auto|nginx|apache|custom)")
	importPreset := flag.String("import-preset", "", "parser preset for -import (e.g. combined, common)")
	importPattern := flag.String("import-pattern", "", "custom pattern for -import when -import-format=custom")
	flag.Parse()

	// Auto-generate a starter config file when none exists at the
	// configured path. This makes "run the binary in an empty dir"
	// just work — the operator gets a file they can edit instead of
	// invisible in-memory defaults. config.Load() will write the file
	// itself; we only log here so the operator notices.
	configAutoCreated := false
	if _, err := os.Stat(*configPath); os.IsNotExist(err) {
		configAutoCreated = true
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	if configAutoCreated {
		if _, err := os.Stat(*configPath); err == nil {
			log.Printf("No config file found; wrote starter %s. Configure log sources via the settings panel.", *configPath)
		} else {
			log.Printf("No config file found and could not write %s (%v); running with in-memory defaults.", *configPath, err)
		}
	}

	store, err := storage.New(cfg.DBPath)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer store.Close()

	// If import mode, process file and exit. Keywords come from the
	// (persisted) runtime config so an import shares the same set as
	// the live watcher pipeline.
	if *importFile != "" {
		p, err := parser.New(parser.FormatConfig{
			Engine:  *importFormatEngine,
			Preset:  *importPreset,
			Pattern: *importPattern,
		})
		if err != nil {
			log.Fatalf("Invalid import format: %v", err)
		}
		rc, err := runtimeconfig.New(store.DB(), cfg.RuntimeSeed())
		if err != nil {
			log.Fatalf("Failed to init runtime config: %v", err)
		}
		count, err := importLogFile(store, *importFile, rc.Get().Keywords, p)
		if err != nil {
			log.Fatalf("Import failed: %v", err)
		}
		fmt.Printf("Imported %d records from %s\n", count, *importFile)
		return
	}

	// Start log watchers in background
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Runtime config lives in the database from Phase 3 onwards. The
	// file's sources/keywords/watch fields are only consulted on first
	// launch to seed the row.
	rcStore, err := runtimeconfig.New(store.DB(), cfg.RuntimeSeed())
	if err != nil {
		log.Fatalf("Failed to init runtime config: %v", err)
	}

	// Watcher manager owns source lifecycles. It subscribes to
	// runtime-config changes so the settings panel's "Save" button
	// can start/stop/restart sources without bouncing the process.
	mgr := watcher.NewManager(ctx, store)
	if err := mgr.Apply(rcStore.Get()); err != nil {
		log.Printf("initial watcher apply: %v", err)
	}
	rcStore.Subscribe(func(rc runtimeconfig.Runtime) {
		if err := mgr.Apply(rc); err != nil {
			log.Printf("watcher reload: %v", err)
		}
	})

	// Setup HTTP server
	mux := http.NewServeMux()

	// Auth service + bootstrap admin (Phase 2). Uses the same SQLite
	// database as the request log; tables live in independent
	// namespaces so the schemas don't collide.
	authSvc, err := auth.New(store.DB(), auth.Options{
		BcryptCost:   cfg.Auth.BcryptCost,
		SessionTTL:   time.Duration(cfg.Auth.SessionTTLHours) * time.Hour,
		CookieSecure: cfg.Auth.CookieSecure,
	})
	if err != nil {
		log.Fatalf("Failed to init auth: %v", err)
	}
	if ba := cfg.Auth.BootstrapAdmin; ba != nil && ba.Username != "" && ba.Password != "" {
		created, err := authSvc.BootstrapAdmin(ba.Username, ba.Password)
		if err != nil {
			log.Fatalf("Failed to bootstrap admin: %v", err)
		}
		if created {
			log.Printf("Bootstrap admin user %q created", ba.Username)
		}
	} else {
		// Operators who skip bootstrap_admin should know the server
		// is running in no-account mode: any visitor to the UI acts
		// as administrator until the first user is created.
		if n, _ := authSvc.CountUsers(); n == 0 {
			log.Printf("Running in no-account mode: no users exist, so every request is treated as administrator. Create the first user from the Users tab to require login.")
		}
	}
	authH := auth.NewHandler(authSvc)
	authH.RegisterRoutes(mux)

	// API routes (protected behind RequireAuth).
	handler := api.NewHandler(store)
	handler.RegisterRoutesWithMiddleware(mux, authH.RequireAuth)

	// Settings panel API (admin-only). Includes the one-click restart
	// endpoint used by the "Restart server" button in the UI.
	cfgHandler := api.NewConfigHandler(rcStore, cfg.ListenAddr, cfg.DBPath, cfg.AllowedLogRoots, authSvc)
	cfgHandler.RegisterRoutes(mux, authH.RequireAdmin)

	// Static files (embedded web GUI)
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Fatalf("Failed to load static files: %v", err)
	}
	mux.Handle("/", http.FileServer(http.FS(staticFS)))

	server := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: mux,
	}

	// Graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("Shutting down...")
		cancel()
		mgr.Stop()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		server.Shutdown(shutdownCtx)
	}()

	log.Printf("Server starting on %s", cfg.ListenAddr)
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("Server error: %v", err)
	}
}

func importLogFile(store *storage.Store, path string, keywords []string, p parser.Parser) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	var reader io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return 0, fmt.Errorf("gzip reader: %w", err)
		}
		defer gz.Close()
		reader = gz
	}

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	var batch []*parser.LogEntry
	count := 0
	const batchSize = 1000

	for scanner.Scan() {
		entry, err := p.Parse(scanner.Text())
		if err != nil {
			continue
		}
		batch = append(batch, entry)
		if len(batch) >= batchSize {
			if err := store.InsertBatch(batch, keywords); err != nil {
				return count, err
			}
			count += len(batch)
			batch = batch[:0]
		}
	}

	if len(batch) > 0 {
		if err := store.InsertBatch(batch, keywords); err != nil {
			return count, err
		}
		count += len(batch)
	}

	return count, scanner.Err()
}
