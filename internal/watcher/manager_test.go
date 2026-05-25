package watcher

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/moehoshio/web-request-attribution/internal/parser"
	"github.com/moehoshio/web-request-attribution/internal/runtimeconfig"
	"github.com/moehoshio/web-request-attribution/internal/storage"
)

func newTestStore(t *testing.T) *storage.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := storage.New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func fileSource(path string) runtimeconfig.Source {
	return runtimeconfig.Source{
		Name: filepath.Base(path),
		Type: runtimeconfig.SourceFile,
		Path: path,
		Format: parser.FormatConfig{Engine: "nginx", Preset: "combined"},
	}
}

func TestManagerDiff(t *testing.T) {
	st := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := NewManager(ctx, st)
	defer mgr.Stop()

	dir := t.TempDir()
	pA := filepath.Join(dir, "a.log")
	pB := filepath.Join(dir, "b.log")
	pC := filepath.Join(dir, "c.log")
	for _, p := range []string{pA, pB, pC} {
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
	}

	apply := func(rc runtimeconfig.Runtime, want []string) {
		t.Helper()
		if err := mgr.Apply(rc); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		// give goroutines a tick to spin up
		time.Sleep(20 * time.Millisecond)
		got := mgr.RunningKeys()
		sort.Strings(got)
		sort.Strings(want)
		if len(got) != len(want) {
			t.Fatalf("running %v want %v", got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("running %v want %v", got, want)
			}
		}
	}

	apply(runtimeconfig.Runtime{Watch: true, Sources: []runtimeconfig.Source{fileSource(pA), fileSource(pB)}},
		[]string{"file|" + pA, "file|" + pB})

	apply(runtimeconfig.Runtime{Watch: true, Sources: []runtimeconfig.Source{fileSource(pB), fileSource(pC)}},
		[]string{"file|" + pB, "file|" + pC})

	apply(runtimeconfig.Runtime{Watch: false},
		[]string{})

	apply(runtimeconfig.Runtime{Watch: true, Sources: []runtimeconfig.Source{fileSource(pA)}},
		[]string{"file|" + pA})
}
