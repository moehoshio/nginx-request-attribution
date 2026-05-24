package watcher

import (
	"bufio"
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/moehoshio/nginx-request-attribution/internal/parser"
	"github.com/moehoshio/nginx-request-attribution/internal/storage"
)

// FileWatcher monitors a log file using fsnotify for efficient event-driven watching.
type FileWatcher struct {
	store    *storage.Store
	logPath  string
	keywords []string
}

// NewFileWatcher creates a new fsnotify-based file watcher.
func NewFileWatcher(store *storage.Store, logPath string, keywords []string) *FileWatcher {
	return &FileWatcher{
		store:    store,
		logPath:  logPath,
		keywords: keywords,
	}
}

// Watch starts watching the log file for new entries using fsnotify.
// It handles log rotation by detecting file truncation or recreation.
func (fw *FileWatcher) Watch(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		if err := fw.watchLoop(ctx); err != nil {
			log.Printf("File watcher error: %v, retrying in 5s...", err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(5 * time.Second):
			}
		}
	}
}

func (fw *FileWatcher) watchLoop(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()

	// Watch the directory to detect file recreation (log rotation)
	dir := filepath.Dir(fw.logPath)
	if err := watcher.Add(dir); err != nil {
		return err
	}

	f, err := os.Open(fw.logPath)
	if err != nil {
		return err
	}
	defer f.Close()

	// Seek to end - only process new entries
	offset, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	baseName := filepath.Base(fw.logPath)

	for {
		select {
		case <-ctx.Done():
			return nil

		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}

			eventBase := filepath.Base(event.Name)

			// Handle log rotation: file was removed or renamed, then recreated
			if eventBase == baseName && (event.Has(fsnotify.Create)) {
				// File was recreated (log rotation), reopen it
				f.Close()
				time.Sleep(100 * time.Millisecond) // brief wait for file to be ready
				f, err = os.Open(fw.logPath)
				if err != nil {
					return err
				}
				defer f.Close()
				offset = 0
				scanner = bufio.NewScanner(f)
				scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
				continue
			}

			if eventBase == baseName && event.Has(fsnotify.Write) {
				// Check for truncation (log rotation via copytruncate)
				info, err := f.Stat()
				if err != nil {
					return err
				}
				if info.Size() < offset {
					// File was truncated, seek to beginning
					if _, err := f.Seek(0, io.SeekStart); err != nil {
						return err
					}
					offset = 0
					scanner = bufio.NewScanner(f)
					scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
				}

				// Read new lines
				var batch []*parser.LogEntry
				for scanner.Scan() {
					entry, err := parser.ParseLine(scanner.Text())
					if err != nil {
						continue
					}
					batch = append(batch, entry)
				}
				if scanner.Err() != nil {
					return scanner.Err()
				}

				if len(batch) > 0 {
					if err := fw.store.InsertBatch(batch, fw.keywords); err != nil {
						log.Printf("File watcher insert error: %v", err)
					}
				}

				// Update offset
				newOffset, err := f.Seek(0, io.SeekCurrent)
				if err != nil {
					return err
				}
				offset = newOffset
			}

		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			log.Printf("File watcher fsnotify error: %v", err)
		}
	}
}
