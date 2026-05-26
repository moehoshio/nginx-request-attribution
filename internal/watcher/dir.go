package watcher

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/moehoshio/web-request-attribution/internal/parser"
	"github.com/moehoshio/web-request-attribution/internal/runtimeconfig"
	"github.com/moehoshio/web-request-attribution/internal/storage"
)

// DirWatcher periodically scans a directory tree for files whose
// basename matches a glob pattern (e.g. `access*.log*`) and ingests
// the new bytes of each one. Per-file progress (`inode`, `offset`,
// `size`, `mtime`) is persisted in the `file_state` table so a
// restart resumes where we left off and a rotation (inode change or
// truncation) is detected on the next pass.
//
// Compressed archives (.gz today) are read once end-to-end when
// `read_compressed=true` is set on the source; their final offset is
// recorded as the file size so subsequent scans skip them.
type DirWatcher struct {
	store        *storage.Store
	src          runtimeconfig.Source
	keywords     []string
	parser       parser.Parser
	pollInterval time.Duration

	// missingLogged guards the "root not available yet" log line so
	// we only emit it once per missing-period (and once again when
	// the root reappears).
	missingLogged bool
}

// NewDirWatcher constructs a DirWatcher for src. src.Type must be
// SourceDir; src.Path is the root to scan and src.Pattern is the
// basename glob (empty matches everything).
func NewDirWatcher(store *storage.Store, src runtimeconfig.Source, keywords []string, p parser.Parser) *DirWatcher {
	if p == nil {
		p, _ = parser.New(parser.FormatConfig{Engine: "auto"})
	}
	return &DirWatcher{
		store:        store,
		src:          src,
		keywords:     keywords,
		parser:       p,
		pollInterval: 5 * time.Second,
	}
}

// Watch runs the scan loop until ctx is cancelled. Errors from
// individual files are logged and the scan continues; only ctx
// cancellation stops the loop.
//
// If the configured root directory does not exist yet (or is
// unreadable) we treat the source as "pending configuration": the
// condition is logged once and the loop keeps polling silently until
// the directory appears.
func (dw *DirWatcher) Watch(ctx context.Context) error {
	// Run an initial scan immediately so newly-configured sources
	// don't have to wait a full poll interval to come online.
	dw.scan(ctx)
	t := time.NewTicker(dw.pollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			dw.scan(ctx)
		}
	}
}

// scan walks the configured directory once.
func (dw *DirWatcher) scan(ctx context.Context) {
	root := dw.src.Path
	pattern := dw.src.Pattern

	// Pre-check: if the root is missing or unreadable, stay quiet
	// (log once via the manager-level state) instead of logging once
	// per poll. We test the root with a stat so we can short-circuit
	// before WalkDir produces noisy per-entry errors.
	if _, err := os.Stat(root); err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
			if !dw.missingLogged {
				log.Printf("dir watcher %q: root %s not available yet (%v); will keep polling quietly.", dw.src.Name, root, err)
				dw.missingLogged = true
			}
			return
		}
		log.Printf("dir watcher %q: stat root %s: %v", dw.src.Name, root, err)
		return
	}
	if dw.missingLogged {
		log.Printf("dir watcher %q: root %s is now available; resuming scans.", dw.src.Name, root)
		dw.missingLogged = false
	}
	walkFn := func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// Surface but continue; missing subdirs are normal during
			// rotation.
			log.Printf("dir watcher %q: walk %s: %v", dw.src.Name, path, err)
			return nil
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			if !dw.src.Recursive {
				return filepath.SkipDir
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return filepath.SkipAll
		default:
		}
		base := d.Name()
		if pattern != "" {
			ok, err := filepath.Match(pattern, base)
			if err != nil || !ok {
				return nil
			}
		}
		if err := dw.handleFile(path); err != nil {
			log.Printf("dir watcher %q: %s: %v", dw.src.Name, path, err)
		}
		return nil
	}
	if err := filepath.WalkDir(root, walkFn); err != nil {
		log.Printf("dir watcher %q: walk %s: %v", dw.src.Name, root, err)
	}
}

// handleFile inspects one matched file and ingests any new bytes.
func (dw *DirWatcher) handleFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	inode := fileInode(info)
	size := info.Size()
	mtime := info.ModTime()

	prev, hasPrev, err := dw.store.GetFileState(path)
	if err != nil {
		return fmt.Errorf("get file_state: %w", err)
	}

	if isCompressed(path) {
		// Compressed archives are read once and never re-read. We
		// detect "already imported" by a stored state that matches
		// the current inode/size.
		if !dw.src.ReadCompressed {
			return nil
		}
		if hasPrev && prev.Inode == inode && prev.Offset >= size {
			return nil
		}
		n, err := dw.importCompressed(path)
		if err != nil {
			return err
		}
		if n > 0 {
			log.Printf("dir watcher %q: imported %d records from %s", dw.src.Name, n, path)
		}
		fp, _ := readFingerprint(path)
		return dw.store.UpsertFileState(storage.FileState{
			Path:        path,
			Inode:       inode,
			Size:        size,
			Offset:      size,
			MTime:       mtime,
			Fingerprint: fp,
		})
	}

	// Read the current head of the file once so we can both detect
	// rotation (vs. prev.Fingerprint) and persist a fresh fingerprint
	// in this scan.
	curFP, err := readFingerprint(path)
	if err != nil {
		return err
	}

	// Plain file: determine where to resume reading from.
	var offset int64
	switch {
	case !hasPrev:
		// First time we see this file: ingest everything that's
		// already there. Discovery is treated as a one-shot import,
		// followed by live tailing on subsequent scans.
		offset = 0
	case prev.Inode != inode:
		// Rotated/recreated → start over.
		offset = 0
	case size < prev.Offset:
		// Truncated (e.g. copytruncate rotation) → start over.
		offset = 0
	case len(prev.Fingerprint) > 0 && !bytes.Equal(curFP, prev.Fingerprint):
		// Inode reuse (common on tmpfs) or in-place rewrite: the
		// stored fingerprint no longer matches the file head, so
		// the bytes at our previous offset are not the same stream
		// we left off in. Re-ingest from the beginning.
		offset = 0
	default:
		offset = prev.Offset
	}

	if offset == size {
		// Nothing new; refresh the row so mtime/fingerprint stay
		// current (cheap UPSERT).
		if hasPrev && prev.MTime.Equal(mtime) && prev.Size == size && bytes.Equal(prev.Fingerprint, curFP) {
			return nil
		}
		return dw.store.UpsertFileState(storage.FileState{
			Path:        path,
			Inode:       inode,
			Size:        size,
			Offset:      offset,
			MTime:       mtime,
			Fingerprint: curFP,
		})
	}

	newOffset, err := dw.consumeRange(path, offset)
	if err != nil {
		return err
	}
	return dw.store.UpsertFileState(storage.FileState{
		Path:        path,
		Inode:       inode,
		Size:        size,
		Offset:      newOffset,
		MTime:       mtime,
		Fingerprint: curFP,
	})
}

// readFingerprint returns the leading bytes of path (at most
// storage.FingerprintSize). Files shorter than that produce a
// fingerprint equal to their full content; an empty file produces an
// empty (non-nil) slice.
func readFingerprint(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, storage.FingerprintSize)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	out := make([]byte, n)
	copy(out, buf[:n])
	return out, nil
}

// consumeRange reads from offset to EOF, inserting parsed entries in
// batches, and returns the new file offset.
func (dw *DirWatcher) consumeRange(path string, offset int64) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return offset, err
	}
	defer f.Close()
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return offset, err
		}
	}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	var batch []*parser.LogEntry
	const batchSize = 1000
	for scanner.Scan() {
		entry, err := dw.parser.Parse(scanner.Text())
		if err != nil {
			continue
		}
		batch = append(batch, entry)
		if len(batch) >= batchSize {
			if err := dw.store.InsertBatch(batch, dw.keywords); err != nil {
				return offset, err
			}
			batch = batch[:0]
		}
	}
	if err := scanner.Err(); err != nil {
		return offset, err
	}
	if len(batch) > 0 {
		if err := dw.store.InsertBatch(batch, dw.keywords); err != nil {
			return offset, err
		}
	}
	newOffset, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return offset, err
	}
	return newOffset, nil
}

// importCompressed reads a `.gz` archive end-to-end.
func (dw *DirWatcher) importCompressed(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var r io.Reader = f
	switch {
	case strings.HasSuffix(path, ".gz"):
		gz, err := gzip.NewReader(f)
		if err != nil {
			return 0, fmt.Errorf("gzip reader: %w", err)
		}
		defer gz.Close()
		r = gz
	default:
		// `.bz2` / `.xz` support is tracked in docs/TODO.md; for now
		// reject so we don't silently mis-parse binary data.
		return 0, fmt.Errorf("unsupported compressed format: %s", filepath.Ext(path))
	}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	var batch []*parser.LogEntry
	count := 0
	const batchSize = 1000
	for scanner.Scan() {
		entry, err := dw.parser.Parse(scanner.Text())
		if err != nil {
			continue
		}
		batch = append(batch, entry)
		if len(batch) >= batchSize {
			if err := dw.store.InsertBatch(batch, dw.keywords); err != nil {
				return count, err
			}
			count += len(batch)
			batch = batch[:0]
		}
	}
	if len(batch) > 0 {
		if err := dw.store.InsertBatch(batch, dw.keywords); err != nil {
			return count, err
		}
		count += len(batch)
	}
	return count, scanner.Err()
}

// isCompressed reports whether path is one of the compressed archive
// formats we treat as read-once.
func isCompressed(path string) bool {
	return strings.HasSuffix(path, ".gz") ||
		strings.HasSuffix(path, ".bz2") ||
		strings.HasSuffix(path, ".xz")
}
