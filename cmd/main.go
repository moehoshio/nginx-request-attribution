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

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	store, err := storage.New(cfg.DBPath)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer store.Close()

	// If import mode, process file and exit
	if *importFile != "" {
		p, err := parser.New(parser.FormatConfig{
			Engine:  *importFormatEngine,
			Preset:  *importPreset,
			Pattern: *importPattern,
		})
		if err != nil {
			log.Fatalf("Invalid import format: %v", err)
		}
		count, err := importLogFile(store, *importFile, cfg.Keywords, p)
		if err != nil {
			log.Fatalf("Import failed: %v", err)
		}
		fmt.Printf("Imported %d records from %s\n", count, *importFile)
		return
	}

	// Start log watchers in background
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if cfg.Watch {
		if err := startSources(ctx, cfg, store); err != nil {
			log.Fatalf("Failed to start sources: %v", err)
		}
	}

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
		// Operators who skip bootstrap_admin should know that no
		// account exists yet; otherwise the UI will return 401 for
		// every API call with no way in.
		if n, _ := authSvc.CountUsers(); n == 0 {
			log.Printf("WARNING: no users exist and config.auth.bootstrap_admin is unset; nobody can log in.")
		}
	}
	authH := auth.NewHandler(authSvc)
	authH.RegisterRoutes(mux)

	// API routes (protected behind RequireAuth).
	handler := api.NewHandler(store)
	handler.RegisterRoutesWithMiddleware(mux, authH.RequireAuth)

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
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		server.Shutdown(shutdownCtx)
	}()

	log.Printf("Server starting on %s", cfg.ListenAddr)
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("Server error: %v", err)
	}
}

func startSources(ctx context.Context, cfg *config.Config, store *storage.Store) error {
	if len(cfg.Sources) == 0 {
		log.Printf("No sources configured; server will run without ingestion.")
		return nil
	}
	for i, src := range cfg.Sources {
		p, err := parser.New(src.Format)
		if err != nil {
			return fmt.Errorf("sources[%d] %q: %w", i, src.Name, err)
		}
		switch src.Type {
		case config.SourceFile:
			fw := watcher.NewFileWatcher(store, src.Path, cfg.Keywords, p, src.ReadCompressed)
			go func(name, path string) {
				if err := fw.Watch(ctx); err != nil {
					log.Printf("File watcher %q stopped: %v", name, err)
				}
			}(src.Name, src.Path)
			log.Printf("File watcher started (%s) on %s [parser=%s]", src.Name, src.Path, p.Name())

		case config.SourceSyslog:
			sr := watcher.NewSyslogReceiver(store, src.Addr, src.Proto, cfg.Keywords, p)
			go func(name, addr, proto string) {
				if err := sr.Listen(ctx); err != nil {
					log.Printf("Syslog receiver %q stopped: %v", name, err)
				}
			}(src.Name, src.Addr, src.Proto)
			log.Printf("Syslog receiver started (%s) on %s (%s) [parser=%s]", src.Name, src.Addr, src.Proto, p.Name())

		default:
			return fmt.Errorf("sources[%d] %q: unknown type %q", i, src.Name, src.Type)
		}
	}
	return nil
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
