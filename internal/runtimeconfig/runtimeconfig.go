// Package runtimeconfig persists the parts of the application
// configuration that operators are expected to edit at runtime
// (sources, keywords, watch toggle). Bootstrap fields such as the
// listen address and database path live in config.json and are not
// managed here — see docs/ROADMAP.md (Phase 3) for the split.
//
// The store is a single-row JSON blob in SQLite. We keep it as one
// blob instead of a key/value table because runtime config is small,
// is always read and written as a whole document, and the JSON shape
// has to round-trip a nested `sources[].format` object anyway.
package runtimeconfig

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/moehoshio/web-request-attribution/internal/parser"
)

// SourceType identifies the kind of log source. Duplicated from the
// (bootstrap) config package so callers don't have to import both
// packages when they only deal with runtime config.
type SourceType string

const (
	SourceFile   SourceType = "file"
	SourceSyslog SourceType = "syslog"
)

// Source describes one place to ingest log lines from. The fields
// mirror the bootstrap `config.Source` so existing JSON config files
// can be loaded as a seed on first launch.
type Source struct {
	Name           string              `json:"name,omitempty"`
	Type           SourceType          `json:"type"`
	Format         parser.FormatConfig `json:"format"`
	Path           string              `json:"path,omitempty"`
	ReadCompressed bool                `json:"read_compressed,omitempty"`
	Addr           string              `json:"addr,omitempty"`
	Proto          string              `json:"proto,omitempty"`
}

// Key returns a stable identifier used by the watcher manager to diff
// running sources against newly-applied configuration.
func (s Source) Key() string {
	switch s.Type {
	case SourceFile:
		return "file|" + s.Path
	case SourceSyslog:
		return "syslog|" + s.Proto + "|" + s.Addr
	default:
		return string(s.Type) + "|" + s.Name
	}
}

// Runtime is the mutable, UI-editable configuration document.
type Runtime struct {
	Watch    bool     `json:"watch"`
	Keywords []string `json:"keywords"`
	Sources  []Source `json:"sources"`
}

// Clone returns a deep copy of r so subscribers can mutate the value
// without racing the store.
func (r Runtime) Clone() Runtime {
	out := Runtime{
		Watch:    r.Watch,
		Keywords: append([]string(nil), r.Keywords...),
		Sources:  append([]Source(nil), r.Sources...),
	}
	return out
}

// Validate performs basic structural checks. Each field has a clear
// error so the settings UI can surface them inline.
func (r *Runtime) Validate(allowedRoots []string) error {
	for i, s := range r.Sources {
		if s.Type == "" {
			return fmt.Errorf("sources[%d]: missing \"type\"", i)
		}
		switch s.Type {
		case SourceFile:
			if strings.TrimSpace(s.Path) == "" {
				return fmt.Errorf("sources[%d]: file source requires \"path\"", i)
			}
			if !pathAllowed(s.Path, allowedRoots) {
				return fmt.Errorf("sources[%d]: path %q is not under any allowed_log_roots entry", i, s.Path)
			}
		case SourceSyslog:
			if strings.TrimSpace(s.Addr) == "" {
				return fmt.Errorf("sources[%d]: syslog source requires \"addr\"", i)
			}
			if s.Proto == "" {
				r.Sources[i].Proto = "udp"
			} else if s.Proto != "udp" && s.Proto != "tcp" {
				return fmt.Errorf("sources[%d]: syslog \"proto\" must be \"udp\" or \"tcp\"", i)
			}
		default:
			return fmt.Errorf("sources[%d]: unknown type %q", i, s.Type)
		}
		if _, err := parser.New(s.Format); err != nil {
			return fmt.Errorf("sources[%d] format: %w", i, err)
		}
	}
	for _, kw := range r.Keywords {
		if strings.ContainsAny(kw, "\x00\n\r") {
			return fmt.Errorf("keywords: control characters not allowed")
		}
	}
	return nil
}

// pathAllowed reports whether p is inside one of roots after symbolic
// cleanup. Empty roots disables the check (operators who haven't set
// allowed_log_roots accept the risk).
func pathAllowed(p string, roots []string) bool {
	if len(roots) == 0 {
		return true
	}
	cleaned := filepath.Clean(p)
	for _, root := range roots {
		root = filepath.Clean(root)
		if root == "" || root == "." {
			continue
		}
		rel, err := filepath.Rel(root, cleaned)
		if err != nil {
			continue
		}
		// `..` anywhere in the relative path means we escaped the root.
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		return true
	}
	return false
}

// Store persists and broadcasts Runtime changes.
type Store struct {
	db *sql.DB

	mu      sync.RWMutex
	current Runtime

	subMu sync.Mutex
	subs  map[int]func(Runtime)
	subID int
}

// New opens (or creates) the runtime_config row in db. If no row
// exists yet, seed is persisted as the initial value.
func New(db *sql.DB, seed Runtime) (*Store, error) {
	s := &Store{db: db, subs: map[int]func(Runtime){}}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	cur, ok, err := s.load()
	if err != nil {
		return nil, err
	}
	if !ok {
		if err := s.persist(seed); err != nil {
			return nil, fmt.Errorf("seed runtime_config: %w", err)
		}
		cur = seed
	}
	s.current = cur
	return s, nil
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS runtime_config (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			value TEXT NOT NULL,
			updated_at DATETIME NOT NULL
		)`)
	return err
}

func (s *Store) load() (Runtime, bool, error) {
	var blob string
	err := s.db.QueryRow(`SELECT value FROM runtime_config WHERE id = 1`).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return Runtime{}, false, nil
	}
	if err != nil {
		return Runtime{}, false, err
	}
	var rc Runtime
	if err := json.Unmarshal([]byte(blob), &rc); err != nil {
		return Runtime{}, false, fmt.Errorf("decode runtime_config: %w", err)
	}
	return rc, true, nil
}

func (s *Store) persist(rc Runtime) error {
	blob, err := json.Marshal(rc)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO runtime_config (id, value, updated_at) VALUES (1, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		string(blob), time.Now().UTC(),
	)
	return err
}

// Get returns a deep copy of the current runtime config.
func (s *Store) Get() Runtime {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current.Clone()
}

// Set validates rc, persists it, and notifies subscribers. allowedRoots
// is checked at validation time; pass nil to skip the path-allow-list
// check.
func (s *Store) Set(rc Runtime, allowedRoots []string) error {
	if err := rc.Validate(allowedRoots); err != nil {
		return err
	}
	rc = rc.Clone()
	s.mu.Lock()
	if err := s.persist(rc); err != nil {
		s.mu.Unlock()
		return err
	}
	s.current = rc
	snapshot := rc.Clone()
	s.mu.Unlock()
	s.notify(snapshot)
	return nil
}

// Subscribe registers fn to be invoked (synchronously, in
// registration order) after every successful Set. The returned
// function deregisters the subscriber.
func (s *Store) Subscribe(fn func(Runtime)) func() {
	s.subMu.Lock()
	id := s.subID
	s.subID++
	s.subs[id] = fn
	s.subMu.Unlock()
	return func() {
		s.subMu.Lock()
		delete(s.subs, id)
		s.subMu.Unlock()
	}
}

func (s *Store) notify(rc Runtime) {
	s.subMu.Lock()
	fns := make([]func(Runtime), 0, len(s.subs))
	for _, fn := range s.subs {
		fns = append(fns, fn)
	}
	s.subMu.Unlock()
	for _, fn := range fns {
		fn(rc.Clone())
	}
}
