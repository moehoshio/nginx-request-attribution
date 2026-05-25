// Package watcher provides per-source ingestion plus a Manager that
// owns the lifecycle of all configured sources and hot-reloads them
// when runtimeconfig publishes a new value. See docs/ROADMAP.md
// (Phase 3) for the design.
package watcher

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/moehoshio/web-request-attribution/internal/parser"
	"github.com/moehoshio/web-request-attribution/internal/runtimeconfig"
	"github.com/moehoshio/web-request-attribution/internal/storage"
)

// Manager owns the running sources. It is safe to call Apply
// concurrently with itself; an internal mutex serialises reloads.
type Manager struct {
	store *storage.Store
	ctx   context.Context

	mu      sync.Mutex
	running map[string]*runningSource
	// snapshot of the keywords + watch flag that the currently-running
	// sources were started with; used so a keywords-only change can
	// trigger a clean restart of every source.
	keywords []string
	watch    bool
}

type runningSource struct {
	src    runtimeconfig.Source
	cancel context.CancelFunc
	done   chan struct{}
}

// NewManager constructs a Manager bound to ctx. Sources are started
// with child contexts derived from ctx; cancelling ctx stops them all.
func NewManager(ctx context.Context, store *storage.Store) *Manager {
	return &Manager{
		store:   store,
		ctx:     ctx,
		running: map[string]*runningSource{},
	}
}

// Apply reconciles the set of running sources against rc. Sources that
// disappeared are stopped; new ones are started; sources whose
// definition changed are restarted. A change to `watch` or keywords
// triggers a full restart since both are baked into the per-source
// pipeline at start time.
func (m *Manager) Apply(rc runtimeconfig.Runtime) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Watch disabled → tear everything down.
	if !rc.Watch {
		m.stopAllLocked()
		m.watch = false
		m.keywords = append([]string(nil), rc.Keywords...)
		return nil
	}

	keywordsChanged := !stringsEqual(m.keywords, rc.Keywords)
	watchTurnedOn := !m.watch

	// Build target set keyed by Source.Key().
	target := make(map[string]runtimeconfig.Source, len(rc.Sources))
	for _, s := range rc.Sources {
		k := s.Key()
		if _, dup := target[k]; dup {
			// Skip exact duplicates rather than start two readers on
			// the same file.
			log.Printf("watcher: duplicate source key %q in config; ignoring duplicate", k)
			continue
		}
		target[k] = s
	}

	// Stop sources that disappeared, changed, or that all need to
	// restart because watch just turned on or keywords changed.
	for k, rs := range m.running {
		newSrc, ok := target[k]
		if !ok || !sourcesEqual(rs.src, newSrc) || watchTurnedOn || keywordsChanged {
			m.stopLocked(k)
		}
	}

	// Start anything that isn't running.
	var firstErr error
	for k, s := range target {
		if _, ok := m.running[k]; ok {
			continue
		}
		if err := m.startLocked(s, rc.Keywords); err != nil {
			log.Printf("watcher: start %q failed: %v", k, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
	}

	m.watch = true
	m.keywords = append([]string(nil), rc.Keywords...)
	return firstErr
}

// Stop tears down every running source. Blocks until they exit.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopAllLocked()
}

func (m *Manager) stopAllLocked() {
	for k := range m.running {
		m.stopLocked(k)
	}
}

func (m *Manager) stopLocked(key string) {
	rs, ok := m.running[key]
	if !ok {
		return
	}
	rs.cancel()
	// Best-effort wait so a follow-up start sees the previous reader
	// fully released (matters for syslog listeners that bind a port).
	<-rs.done
	delete(m.running, key)
}

func (m *Manager) startLocked(s runtimeconfig.Source, keywords []string) error {
	p, err := parser.New(s.Format)
	if err != nil {
		return fmt.Errorf("parser: %w", err)
	}
	ctx, cancel := context.WithCancel(m.ctx)
	done := make(chan struct{})

	switch s.Type {
	case runtimeconfig.SourceFile:
		fw := NewFileWatcher(m.store, s.Path, append([]string(nil), keywords...), p, s.ReadCompressed)
		go func() {
			defer close(done)
			if err := fw.Watch(ctx); err != nil {
				log.Printf("file watcher %q stopped: %v", s.Name, err)
			}
		}()
		log.Printf("watcher: started file source %q on %s [parser=%s]", s.Name, s.Path, p.Name())

	case runtimeconfig.SourceSyslog:
		sr := NewSyslogReceiver(m.store, s.Addr, s.Proto, append([]string(nil), keywords...), p)
		go func() {
			defer close(done)
			if err := sr.Listen(ctx); err != nil {
				log.Printf("syslog receiver %q stopped: %v", s.Name, err)
			}
		}()
		log.Printf("watcher: started syslog source %q on %s/%s [parser=%s]", s.Name, s.Addr, s.Proto, p.Name())

	default:
		cancel()
		close(done)
		return fmt.Errorf("unknown source type %q", s.Type)
	}

	m.running[s.Key()] = &runningSource{src: s, cancel: cancel, done: done}
	return nil
}

// RunningKeys returns the keys of currently-running sources. Intended
// for diagnostics and tests.
func (m *Manager) RunningKeys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.running))
	for k := range m.running {
		out = append(out, k)
	}
	return out
}

func sourcesEqual(a, b runtimeconfig.Source) bool {
	return a.Type == b.Type &&
		a.Path == b.Path &&
		a.Addr == b.Addr &&
		a.Proto == b.Proto &&
		a.ReadCompressed == b.ReadCompressed &&
		a.Format == b.Format
}

func stringsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
