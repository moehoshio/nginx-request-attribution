package watcher

import (
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/moehoshio/web-request-attribution/internal/parser"
	"github.com/moehoshio/web-request-attribution/internal/runtimeconfig"
	"github.com/moehoshio/web-request-attribution/internal/storage"
)

const sampleLine = `127.0.0.1 - - [10/Oct/2000:13:55:36 -0700] "GET /index.html HTTP/1.0" 200 2326 "-" "Mozilla/5.0"`

func dirSource(root, pattern string, recursive, readCompressed bool) runtimeconfig.Source {
	return runtimeconfig.Source{
		Name:           "dirsrc",
		Type:           runtimeconfig.SourceDir,
		Path:           root,
		Pattern:        pattern,
		Recursive:      recursive,
		ReadCompressed: readCompressed,
		Format:         parser.FormatConfig{Engine: "nginx", Preset: "combined"},
	}
}

func writeFile(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
}

func appendFile(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("append %s: %v", path, err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
}

func writeGzip(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()
	gw := gzip.NewWriter(f)
	defer gw.Close()
	for _, l := range lines {
		if _, err := gw.Write([]byte(l + "\n")); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
}

func rowCount(t *testing.T, st *storage.Store) int {
	t.Helper()
	var n int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM requests`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func waitForCount(t *testing.T, st *storage.Store, want int, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if rowCount(t, st) >= want {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return rowCount(t, st) >= want
}

func TestDirWatcherIngestsAndResumes(t *testing.T) {
	st := newTestStore(t)
	dir := t.TempDir()
	logPath := filepath.Join(dir, "access.log")
	writeFile(t, logPath, sampleLine, sampleLine)

	p, _ := parser.New(parser.FormatConfig{Engine: "nginx", Preset: "combined"})
	dw := NewDirWatcher(st, dirSource(dir, "access*.log*", false, false), nil, p)
	dw.pollInterval = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = dw.Watch(ctx)
	}()

	if !waitForCount(t, st, 2, 2*time.Second) {
		t.Fatalf("expected 2 rows after initial scan, got %d", rowCount(t, st))
	}

	appendFile(t, logPath, sampleLine, sampleLine)
	if !waitForCount(t, st, 4, 2*time.Second) {
		t.Fatalf("expected 4 rows after append, got %d", rowCount(t, st))
	}

	cancel()
	<-done
	fs, ok, err := st.GetFileState(logPath)
	if err != nil || !ok {
		t.Fatalf("file_state missing: ok=%v err=%v", ok, err)
	}
	if fs.Offset == 0 || fs.Offset != fs.Size {
		t.Fatalf("unexpected file_state: %+v", fs)
	}
}

func TestDirWatcherDetectsRotation(t *testing.T) {
	st := newTestStore(t)
	dir := t.TempDir()
	logPath := filepath.Join(dir, "access.log")
	writeFile(t, logPath, sampleLine)

	p, _ := parser.New(parser.FormatConfig{Engine: "nginx", Preset: "combined"})
	dw := NewDirWatcher(st, dirSource(dir, "access*.log*", false, false), nil, p)
	dw.pollInterval = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = dw.Watch(ctx)
	}()
	if !waitForCount(t, st, 1, 2*time.Second) {
		t.Fatalf("expected 1 row after initial scan, got %d", rowCount(t, st))
	}

	// Simulate a logrotate-style rotation: rename the live file
	// aside, then create a new empty file that subsequently grows.
	// This is the typical case real watchers must survive: the new
	// file is seen at size 0 first, then content is appended.
	if err := os.Rename(logPath, logPath+".1"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, logPath) // empty
	// Give the watcher a poll to notice the rotation.
	time.Sleep(60 * time.Millisecond)
	// Now append two lines to the fresh file.
	appendFile(t, logPath, sampleLine, sampleLine)
	if !waitForCount(t, st, 3, 2*time.Second) {
		t.Fatalf("expected 3 rows after rotation (1 old + 2 new), got %d", rowCount(t, st))
	}
	cancel()
	<-done
}

func TestDirWatcherDetectsTruncate(t *testing.T) {
	st := newTestStore(t)
	dir := t.TempDir()
	logPath := filepath.Join(dir, "access.log")
	writeFile(t, logPath, sampleLine, sampleLine)

	p, _ := parser.New(parser.FormatConfig{Engine: "nginx", Preset: "combined"})
	dw := NewDirWatcher(st, dirSource(dir, "access*.log*", false, false), nil, p)
	dw.pollInterval = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = dw.Watch(ctx)
	}()
	if !waitForCount(t, st, 2, 2*time.Second) {
		t.Fatalf("expected 2 rows after initial scan, got %d", rowCount(t, st))
	}

	// copytruncate-style rotation: same inode, size shrinks back to 0.
	if err := os.Truncate(logPath, 0); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	appendFile(t, logPath, sampleLine)
	if !waitForCount(t, st, 3, 2*time.Second) {
		t.Fatalf("expected 3 rows after truncate (2 old + 1 new), got %d", rowCount(t, st))
	}
	cancel()
	<-done
}

func TestDirWatcherCompressedOneShot(t *testing.T) {
	st := newTestStore(t)
	dir := t.TempDir()
	gzPath := filepath.Join(dir, "access.log.1.gz")
	writeGzip(t, gzPath, sampleLine, sampleLine, sampleLine)

	p, _ := parser.New(parser.FormatConfig{Engine: "nginx", Preset: "combined"})
	dw := NewDirWatcher(st, dirSource(dir, "access*.log*", false, true), nil, p)
	dw.pollInterval = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = dw.Watch(ctx)
	}()
	if !waitForCount(t, st, 3, 2*time.Second) {
		t.Fatalf("expected 3 rows imported from gzip, got %d", rowCount(t, st))
	}
	// Allow several poll intervals to confirm the archive is not
	// re-imported.
	time.Sleep(120 * time.Millisecond)
	if got := rowCount(t, st); got != 3 {
		t.Fatalf("compressed archive re-imported: got %d rows", got)
	}
	cancel()
	<-done
}

func TestDirWatcherRecursive(t *testing.T) {
	p, _ := parser.New(parser.FormatConfig{Engine: "nginx", Preset: "combined"})

	// Non-recursive: only top-level file is ingested.
	t.Run("non-recursive", func(t *testing.T) {
		st := newTestStore(t)
		dir := t.TempDir()
		sub := filepath.Join(dir, "vhost1")
		if err := os.MkdirAll(sub, 0755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, "access.log"), sampleLine)
		writeFile(t, filepath.Join(sub, "access.log"), sampleLine, sampleLine)

		dw := NewDirWatcher(st, dirSource(dir, "access*.log*", false, false), nil, p)
		dw.pollInterval = 20 * time.Millisecond
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan struct{})
		go func() { defer close(done); _ = dw.Watch(ctx) }()
		if !waitForCount(t, st, 1, 2*time.Second) {
			t.Fatalf("non-recursive: expected 1 row, got %d", rowCount(t, st))
		}
		time.Sleep(80 * time.Millisecond)
		if got := rowCount(t, st); got != 1 {
			t.Fatalf("non-recursive: descended into subdir, got %d rows", got)
		}
		cancel()
		<-done
	})

	// Recursive: top-level + subdir.
	t.Run("recursive", func(t *testing.T) {
		st := newTestStore(t)
		dir := t.TempDir()
		sub := filepath.Join(dir, "vhost1")
		if err := os.MkdirAll(sub, 0755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, "access.log"), sampleLine)
		writeFile(t, filepath.Join(sub, "access.log"), sampleLine, sampleLine)

		dw := NewDirWatcher(st, dirSource(dir, "access*.log*", true, false), nil, p)
		dw.pollInterval = 20 * time.Millisecond
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan struct{})
		go func() { defer close(done); _ = dw.Watch(ctx) }()
		if !waitForCount(t, st, 3, 2*time.Second) {
			t.Fatalf("recursive: expected 3 rows, got %d", rowCount(t, st))
		}
		cancel()
		<-done
	})
}

func TestFileStateCRUD(t *testing.T) {
	st := newTestStore(t)
	_, ok, err := st.GetFileState("/missing")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if ok {
		t.Fatal("expected missing state")
	}
	fs := storage.FileState{
		Path:        "/var/log/x.log",
		Inode:       12345,
		Size:        1024,
		Offset:      512,
		MTime:       time.Now().UTC().Truncate(time.Second),
		Fingerprint: []byte("abcdef"),
	}
	if err := st.UpsertFileState(fs); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, ok, err := st.GetFileState(fs.Path)
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if got.Inode != fs.Inode || got.Size != fs.Size || got.Offset != fs.Offset {
		t.Fatalf("round-trip mismatch: %+v vs %+v", got, fs)
	}
	// Upsert updates.
	fs.Offset = 2048
	fs.Size = 2048
	if err := st.UpsertFileState(fs); err != nil {
		t.Fatalf("upsert 2: %v", err)
	}
	got, _, _ = st.GetFileState(fs.Path)
	if got.Offset != 2048 {
		t.Fatalf("upsert did not update: %+v", got)
	}
}

